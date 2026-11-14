package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// command is one entry of the panel's command catalog: an operation on a
// resource, with the arguments it takes. The catalog is built once, at the end
// of Build, from the compiled resources, and is the same for every caller —
// whether a call is allowed is decided when it runs, by the same handlers a
// browser reaches. The CLI's data commands and the MCP tools are both read
// from it.
type command struct {
	// Name is the CLI spelling ("posts:list"); Tool the MCP one ("posts_list").
	Name        string
	Tool        string
	Title       string
	Description string
	Params      []param
	// ReadOnly and Destructive feed the MCP tool annotations.
	ReadOnly    bool
	Destructive bool

	// request turns validated arguments into the panel request that performs
	// the operation: method, path relative to the prefix, query, form.
	request func(args map[string]any) (method, path string, query, form url.Values, err error)
}

// param is one argument of a command.
type param struct {
	Name        string
	Type        string // string, integer, number, boolean, array, object
	Description string
	Required    bool
	Enum        []string
	// Items is the element type of an array.
	Items string
}

// toolNameRe is what MCP accepts as a tool name.
var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// commandBase is the slug in a form both surfaces accept: "auth/users" becomes
// "auth_users".
func commandBase(slug string) string { return strings.ReplaceAll(slug, "/", "_") }

// mcpResource is a resource's half of the catalog, behind the type-erased
// registry.
type mcpResource interface {
	commands() []*command
}

// DisableMCP leaves the resource out of the command catalog: no CLI data
// commands and no MCP tools. The panel itself is unaffected.
func (r *Resource[T]) DisableMCP() *Resource[T] { r.noMCP = true; return r }

// Describe sets the sentence an agent reads about this resource, at the head of
// each of its commands' descriptions. Without it the title is used.
func (r *Resource[T]) Describe(text string) *Resource[T] { r.description = text; return r }

// buildCommands compiles the catalog. It runs at the end of build, after every
// resource has compiled, and reports names that collide.
func (a *Panel) buildCommands() {
	a.commands = nil
	a.commandIndex = map[string]*command{}
	seen := map[string]bool{}
	add := func(c *command) {
		switch {
		case slices.Contains(cliCommands, c.Name):
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"command %q collides with a built-in command; give the resource another slug", c.Name))
			return
		case !toolNameRe.MatchString(c.Tool):
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"command %q: tool name %q is not a valid MCP tool name", c.Name, c.Tool))
			return
		case seen[c.Tool]:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf("command %q is declared twice", c.Name))
			return
		}
		seen[c.Tool] = true
		a.commands = append(a.commands, c)
		a.commandIndex[c.Name] = c
		a.commandIndex[c.Tool] = c
	}
	for _, entry := range a.registry {
		if r, ok := entry.(mcpResource); ok {
			for _, c := range r.commands() {
				add(c)
			}
		}
	}
}

// lookupCommand finds a command by either spelling.
func (a *Panel) lookupCommand(name string) (*command, bool) {
	c, ok := a.commandIndex[name]
	return c, ok
}

// commandNames lists the catalog's CLI names, for help and suggestions.
func (a *Panel) commandNames() []string {
	out := make([]string, 0, len(a.commands))
	for _, c := range a.commands {
		out = append(out, c.Name)
	}
	return out
}

// ---- per resource ---------------------------------------------------------

// resourceShape is what the catalog needs to know about a resource, copied
// out of the typed resource. Everything that builds and runs commands works on
// it, so that code exists once in the binary rather than once per model type.
type resourceShape struct {
	slug, title, about string
	pkLabel            string
	view, create, edit bool
	del, trash         bool
	sortable           []string
	quickSearch        []string
	filters            []filterShape
	fields             []fieldShape
	actions            []actionShape
}

type filterShape struct {
	param, label string
	between, in  bool
	dateLike     bool
	enum         []string
}

type fieldShape struct {
	path, label, help, rules string
	kind                     FieldKind
	required                 bool
	onlyCreate, onlyUpdate   bool
	enum                     []string
	relName                  string
	embedPaths               []string
}

type actionShape struct {
	name, label  string
	danger, tool bool
}

// shape copies the resource's catalog-relevant declarations. It is the only
// part of the catalog compiled per model type, and it only copies.
func (t *typedResource[T]) shape() *resourceShape {
	g := t.grid
	s := &resourceShape{
		slug: t.res.m.slug, title: t.res.m.title, about: t.res.description,
		pkLabel: t.ft.pk.Label,
		view:    g.enabled("view"), create: g.enabled("create"), edit: g.enabled("edit"),
		del: g.enabled("delete"), trash: t.trash != nil,
		sortable: sortablePaths(g),
	}
	if g.enabled("quicksearch") {
		s.quickSearch = g.quickSearch
	}
	for _, fi := range g.filters {
		if fi.info == nil {
			continue
		}
		f := filterShape{param: filterParam(fi.path), label: fi.label,
			between: fi.op == OpBetween, in: fi.op == OpIn, dateLike: fi.dateLike()}
		if fi.optionsFn == nil && len(fi.options) > 0 {
			f.enum = sortedKeys(fi.options)
		}
		s.filters = append(s.filters, f)
	}
	for _, fd := range t.form.fields {
		if fd.divider || fd.ignored || fd.kind == FieldDisplay || isUploadKind(fd.kind) ||
			(fd.info == nil && !fd.virtual) {
			continue
		}
		f := fieldShape{
			path: fd.path, label: fd.label, help: fd.help, rules: fd.rules, kind: fd.kind,
			required:   fd.required || strings.Contains(fd.createRules, "required"),
			onlyCreate: fd.onlyCreate, onlyUpdate: fd.onlyUpdate, relName: fd.relName,
		}
		if fd.optionsFn == nil && len(fd.options) > 0 {
			f.enum = sortedKeys(fd.options)
		}
		if fd.embed != nil {
			f.embedPaths = fd.embed.fieldPaths()
		}
		s.fields = append(s.fields, f)
	}
	for _, act := range g.allActions() {
		s.actions = append(s.actions, actionShape{name: act.name, label: act.label,
			danger: act.danger, tool: slices.Contains(g.toolActions, act)})
	}
	return s
}

func (t *typedResource[T]) commands() []*command {
	if t.res.noMCP {
		return nil
	}
	return t.shape().commands()
}

func (s *resourceShape) field(path string) (fieldShape, bool) {
	for _, f := range s.fields {
		if f.path == path {
			return f, true
		}
	}
	return fieldShape{}, false
}

func (s *resourceShape) commands() []*command {
	base := commandBase(s.slug)
	about := s.about
	if about == "" {
		about = s.title
	}
	path := "/" + s.slug
	pk := s.pkLabel
	var out []*command
	mk := func(op, title, desc string, params []param) *command {
		return &command{
			Name: base + ":" + op, Tool: base + "_" + op,
			Title: s.title + ": " + title, Description: about + " — " + desc, Params: params,
		}
	}

	// The listing is gated by ViewAny like the grid page, not by a feature
	// switch: every resource has one.
	list := mk("list", "list", "list records, newest first unless sorted, a page at a time.", s.listParams())
	list.ReadOnly = true
	list.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
		q := url.Values{}
		for k, v := range args {
			if k == "trashed" {
				if b, _ := v.(bool); b {
					q.Set("trashed", "1")
				}
				continue
			}
			q.Set(k, scalarString(v))
		}
		return http.MethodGet, path, q, nil, nil
	}
	out = append(out, list)

	if s.view {
		get := mk("get", "get", "one record by its "+pk+".", []param{
			{Name: "id", Type: "string", Required: true, Description: "the record's " + pk},
		})
		get.ReadOnly = true
		get.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
			return http.MethodGet, path + "/" + url.PathEscape(scalarString(args["id"])), nil, nil, nil
		}
		out = append(out, get)
	}

	if s.create {
		create := mk("create", "create", "create a record. Fields follow the form's rules.", s.formParams(true))
		create.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
			form, err := s.formValues(args)
			return http.MethodPost, path, nil, form, err
		}
		out = append(out, create)
	}

	if s.edit {
		params := append([]param{{Name: "id", Type: "string", Required: true, Description: "the record's " + pk}},
			s.formParams(false)...)
		update := mk("update", "update", "change the given fields of a record; the others are left as they are.", params)
		update.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
			id := scalarString(args["id"])
			rest := map[string]any{}
			for k, v := range args {
				if k != "id" {
					rest[k] = v
				}
			}
			form, err := s.formValues(rest)
			return http.MethodPut, path + "/" + url.PathEscape(id), nil, form, err
		}
		out = append(out, update)
	}

	idsParam := func(required bool) param {
		return param{Name: "ids", Type: "array", Items: "string", Required: required,
			Description: "the records' " + pk + " values"}
	}

	if s.del {
		del := mk("delete", "delete", "delete records.", []param{idsParam(true)})
		del.Destructive = true
		del.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
			ids := stringList(args["ids"])
			if len(ids) == 0 {
				return "", "", nil, nil, errors.New("ids is empty")
			}
			for i := range ids {
				ids[i] = url.PathEscape(ids[i])
			}
			return http.MethodDelete, path + "/" + strings.Join(ids, ","), nil, nil, nil
		}
		out = append(out, del)
	}

	if s.trash {
		for _, op := range []struct {
			name, title, desc, route string
			destructive              bool
		}{
			{"restore", "restore", "bring deleted records back from the trash.", "_restore", false},
			{"force_delete", "delete permanently", "remove deleted records for good.", "_force_delete", true},
		} {
			c := mk(op.name, op.title, op.desc, []param{idsParam(true)})
			c.Destructive = op.destructive
			route := op.route
			c.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
				ids := stringList(args["ids"])
				if len(ids) == 0 {
					return "", "", nil, nil, errors.New("ids is empty")
				}
				return http.MethodPost, path + "/" + route, nil, url.Values{"ids": {strings.Join(ids, ",")}}, nil
			}
			out = append(out, c)
		}
	}

	for _, act := range s.actions {
		desc := "run the " + act.label + " action on records."
		if act.tool {
			desc = "run the " + act.label + " action."
		}
		c := mk(strings.ReplaceAll(act.name, "-", "_"), act.label, desc, []param{idsParam(!act.tool)})
		c.Destructive = act.danger
		name := act.name
		c.request = func(args map[string]any) (string, string, url.Values, url.Values, error) {
			form := url.Values{}
			if ids := stringList(args["ids"]); len(ids) > 0 {
				form.Set("ids", strings.Join(ids, ","))
			}
			return http.MethodPost, path + "/_action/" + url.PathEscape(name), nil, form, nil
		}
		out = append(out, c)
	}
	return out
}

// listParams are the arguments a listing takes: the grid's own query
// parameters.
func (s *resourceShape) listParams() []param {
	ps := []param{
		{Name: "page", Type: "integer", Description: "page number, from 1"},
		{Name: "per_page", Type: "integer", Description: "rows per page, up to 500"},
	}
	if len(s.sortable) > 0 {
		ps = append(ps, param{Name: "sort", Type: "string",
			Description: "field to sort by, prefixed - for descending: " + strings.Join(s.sortable, ", ")})
	}
	if len(s.quickSearch) > 0 {
		ps = append(ps, param{Name: "q", Type: "string",
			Description: "quick search over " + strings.Join(s.quickSearch, ", ") +
				"; field:value targets one field"})
	}
	for _, f := range s.filters {
		p := param{Name: f.param, Type: "string", Description: "filter " + f.label, Enum: f.enum}
		switch {
		case f.between:
			p.Description = "filter " + f.label + ": lower bound"
			ps = append(ps, p, param{Name: f.param + "_to", Type: "string",
				Description: "filter " + f.label + ": upper bound"})
			continue
		case f.in:
			p.Description = "filter " + f.label + ": comma-separated values"
		}
		if f.dateLike {
			p.Description += " (YYYY-MM-DD)"
		}
		ps = append(ps, p)
	}
	if s.trash {
		ps = append(ps, param{Name: "trashed", Type: "boolean", Description: "list deleted records instead"})
	}
	return ps
}

// formParams are a form's fields as arguments. On create the form's required
// fields are required; on update nothing is.
func (s *resourceShape) formParams(creating bool) []param {
	var ps []param
	for _, f := range s.fields {
		if (creating && f.onlyUpdate) || (!creating && f.onlyCreate) {
			continue
		}
		p := param{Name: f.path, Type: "string", Description: f.label, Enum: f.enum,
			Required: creating && f.required}
		if f.help != "" {
			p.Description += ". " + f.help
		}
		if f.rules != "" {
			p.Description += " (rules: " + f.rules + ")"
		}
		switch f.kind {
		case FieldNumber:
			p.Type = "integer"
		case FieldDecimal, FieldCurrency:
			p.Type = "number"
		case FieldSwitch:
			p.Type = "boolean"
		case FieldMultiSelect, FieldTags:
			p.Type, p.Items = "array", "string"
		case FieldEmbed:
			p.Type = "object"
			p.Description += " — fields: " + strings.Join(f.embedPaths, ", ")
		case FieldEmbedMany:
			p.Type, p.Items = "array", "object"
			p.Description += " — each with fields: " + strings.Join(f.embedPaths, ", ")
		case FieldDate:
			p.Description += " (YYYY-MM-DD)"
		case FieldDatetime:
			p.Description += " (YYYY-MM-DDTHH:MM)"
		case FieldTime:
			p.Description += " (HH:MM)"
		case FieldBelongsTo:
			p.Description += " — the " + f.relName + " record's key"
		}
		ps = append(ps, p)
	}
	return ps
}

// formValues encodes arguments the way the form submits them, so the handler
// reads a command exactly as it reads a browser.
func (s *resourceShape) formValues(args map[string]any) (url.Values, error) {
	form := url.Values{}
	for name, v := range args {
		f, ok := s.field(name)
		if !ok {
			return nil, fmt.Errorf("%s is not a field of this form", name)
		}
		switch f.kind {
		case FieldSwitch:
			form.Set("_present_"+name, "1")
			if truthy(v) {
				form.Set(name, "on")
			}
		case FieldMultiSelect, FieldTags:
			raw, err := json.Marshal(stringList(v))
			if err != nil {
				return nil, err
			}
			form.Set(name, string(raw))
		case FieldEmbed:
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s must be an object", name)
			}
			for k, val := range obj {
				form.Set(name+"["+k+"]", scalarString(val))
			}
		case FieldEmbedMany:
			rows, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of objects", name)
			}
			form.Set("_present_"+name, "1")
			for i, r := range rows {
				obj, ok := r.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s[%d] must be an object", name, i)
				}
				key := "new_" + strconv.Itoa(i+1) + "_cmd"
				for k, val := range obj {
					form.Set(name+"["+key+"]["+k+"]", scalarString(val))
				}
			}
		default:
			form.Set(name, scalarString(v))
		}
	}
	return form, nil
}

// ---- arguments ------------------------------------------------------------

// checkArgs refuses an argument the command does not take, a missing required
// one, and a value of the wrong JSON type, before anything runs.
func (c *command) checkArgs(args map[string]any) error {
	byName := map[string]param{}
	for _, p := range c.Params {
		byName[p.Name] = p
	}
	var problems []string
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		p, ok := byName[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is not an argument of %s", k, c.Name))
			continue
		}
		if !typeMatches(p, args[k]) {
			problems = append(problems, fmt.Sprintf("%s must be %s", k, typePhrase(p)))
			continue
		}
		if len(p.Enum) > 0 && !slices.Contains(p.Enum, scalarString(args[k])) {
			problems = append(problems, fmt.Sprintf("%s must be one of %s", k, strings.Join(p.Enum, ", ")))
		}
	}
	for _, p := range c.Params {
		if _, ok := args[p.Name]; p.Required && !ok {
			problems = append(problems, p.Name+" is required")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func typeMatches(p param, v any) bool {
	switch p.Type {
	case "integer":
		f, ok := v.(float64)
		if !ok {
			_, isInt := v.(int)
			return isInt
		}
		return f == float64(int64(f))
	case "number":
		switch v.(type) {
		case float64, int:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	default:
		switch v.(type) {
		case string, float64, int:
			return true
		}
		return false
	}
}

func typePhrase(p param) string {
	switch p.Type {
	case "integer":
		return "a whole number"
	case "array":
		return "a list"
	case "object":
		return "an object"
	}
	return "a " + p.Type
}

// parseArg turns a CLI flag's text into the JSON value its parameter takes.
func (p param) parseArg(raw string) (any, error) {
	switch p.Type {
	case "integer":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s must be a whole number", p.Name)
		}
		return float64(n), nil
	case "number":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("--%s must be a number", p.Name)
		}
		return f, nil
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("--%s must be true or false", p.Name)
		}
		return b, nil
	case "array":
		if strings.HasPrefix(strings.TrimSpace(raw), "[") {
			var v []any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				return nil, fmt.Errorf("--%s: %w", p.Name, err)
			}
			return v, nil
		}
		var out []any
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case "object":
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("--%s must be a JSON object: %w", p.Name, err)
		}
		return v, nil
	}
	return raw, nil
}

// scalarString renders a JSON scalar the way a form submits it.
func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	default:
		raw, _ := json.Marshal(x)
		return string(raw)
	}
}

// stringList reads a list argument; a single scalar is a list of one.
func stringList(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, scalarString(e))
		}
		return out
	case []string:
		return x
	default:
		return []string{scalarString(x)}
	}
}

func sortedKeys(o Options) []string {
	out := make([]string, 0, len(o))
	for k := range o {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- execution ------------------------------------------------------------

// principal is who a command runs as. Token is what makes the panel's
// middleware treat the call as a bearer caller: for /_mcp the token the
// request presented, for the CLI one made in memory and never stored.
type principal struct {
	User  *User
	Token *Token
}

// commandResult is what a command answered.
type commandResult struct {
	Status  int
	Body    any
	IsError bool
	// Message and Errors are the envelope's, for a refusal or a 422.
	Message string
	Errors  map[string][]string
}

// runCommand executes a command as p: the arguments are checked, turned into
// the request the operation's handler serves, and passed through the panel's
// full handler — permissions, tenant, policies, validation, history and the
// operation log apply exactly as they do to a browser.
func (a *Panel) runCommand(ctx context.Context, p principal, c *command, args map[string]any) (*commandResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	if err := c.checkArgs(args); err != nil {
		return &commandResult{Status: http.StatusBadRequest, IsError: true, Message: err.Error()}, nil
	}
	method, rel, query, form, err := c.request(args)
	if err != nil {
		return &commandResult{Status: http.StatusBadRequest, IsError: true, Message: err.Error()}, nil
	}
	target := a.url(strings.TrimPrefix(rel, "/"))
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	// The caller's context may be a tenant-bypassing one (the CLI's is): the
	// inner request must resolve its workspace from the token like any other
	// request, never inherit a bypass.
	ctx = context.WithValue(ctx, noTenantCtxKey{}, false)
	ctx = context.WithValue(ctx, tenantCtxKey{}, uint(0))
	ctx = context.WithValue(ctx, ctxKeyUser, p.User)
	ctx = context.WithValue(ctx, ctxKeyToken, p.Token)
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := &responseBuffer{header: http.Header{}, code: http.StatusOK}
	a.handler.ServeHTTP(rec, req)
	return decodeCommandResult(rec.code, rec.body.Bytes()), nil
}

// responseBuffer collects a handler's answer in memory. It stands in for
// httptest.ResponseRecorder, which is test tooling and would pull a test
// server's dependencies into every binary.
type responseBuffer struct {
	header http.Header
	body   bytes.Buffer
	code   int
	wrote  bool
}

func (r *responseBuffer) Header() http.Header { return r.header }

func (r *responseBuffer) WriteHeader(code int) {
	if !r.wrote {
		r.code, r.wrote = code, true
	}
}

func (r *responseBuffer) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

// decodeCommandResult reads a handler's JSON answer. An envelope with status
// false, or any 4xx or 5xx, is an error the caller can act on.
func decodeCommandResult(status int, raw []byte) *commandResult {
	res := &commandResult{Status: status, IsError: status >= 400}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		res.Body = map[string]any{"text": strings.TrimSpace(string(raw))}
		if res.IsError {
			res.Message = http.StatusText(status)
		}
		return res
	}
	res.Body = body
	if obj, ok := body.(map[string]any); ok {
		if st, ok := obj["status"].(bool); ok && !st {
			res.IsError = true
		}
		if data, ok := obj["data"].(map[string]any); ok {
			res.Message, _ = data["message"].(string)
		}
		if msg, ok := obj["message"].(string); ok && res.Message == "" {
			res.Message = msg
		}
		if errs, ok := obj["errors"].(map[string]any); ok {
			res.Errors = map[string][]string{}
			for k, v := range errs {
				res.Errors[k] = stringList(v)
			}
		}
	}
	if res.IsError && res.Message == "" {
		res.Message = http.StatusText(status)
		if len(res.Errors) > 0 {
			res.Message = "The arguments did not pass the form's rules."
		}
	}
	return res
}

// errorText is a failed result as one readable paragraph.
func (r *commandResult) errorText() string {
	parts := []string{r.Message}
	keys := make([]string, 0, len(r.Errors))
	for k := range r.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+": "+strings.Join(r.Errors[k], " "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// ---- schemas ----------------------------------------------------------------

// inputSchema is a command's arguments as a JSON Schema object.
func (c *command) inputSchema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range c.Params {
		s := map[string]any{"type": p.Type}
		if p.Description != "" {
			s["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			s["enum"] = p.Enum
		}
		if p.Type == "array" {
			items := p.Items
			if items == "" {
				items = "string"
			}
			s["items"] = map[string]any{"type": items}
		}
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// toolJSON is a command as MCP's tools/list describes it.
func (c *command) toolJSON() map[string]any {
	return map[string]any{
		"name":        c.Tool,
		"title":       c.Title,
		"description": c.Description,
		"inputSchema": c.inputSchema(),
		"annotations": map[string]any{
			"readOnlyHint":    c.ReadOnly,
			"destructiveHint": c.Destructive,
		},
	}
}
