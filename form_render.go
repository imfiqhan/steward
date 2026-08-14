package steward

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/imfiqhan/steward/internal/rules"
	"github.com/imfiqhan/steward/internal/session"
	"gorm.io/gorm"
)

// ---- view model -------------------------------------------------------------

type formFieldVM struct {
	Kind        string
	Name        string // input name = field path
	Label       string
	Value       string
	Required    bool
	Disabled    bool
	ReadOnly    bool
	Placeholder string
	Help        string
	Accept      string
	Fieldset    string
	Divider     bool
	Options     []optionVM
	// Icons carries an Icon field's choices; SpriteURL is where the browser
	// fetches their glyphs, once, for the whole grid.
	Icons     []iconChoiceVM
	SpriteURL string
	// CurrentIcon is the selected glyph, inlined so the closed field shows it
	// without waiting for the sprite.
	CurrentIcon template.HTML
	UploadURL   string
	PreviewURL  string
	// FileName is what an upload is called for the reader; MaxSizeLabel and
	// AcceptLabel state the field's limits, which the server knows and the
	// reader otherwise discovers by having an upload refused.
	FileName     string
	MaxSizeLabel string
	AcceptLabel  string
	// SelectedJSON is a MultiSelect's selection as a JSON array, which is what
	// the combobox stores in its hidden input and submits.
	SelectedJSON string
	// OptionsURL is where a combobox fetches its suggestions, and Search says
	// whether it should: a list that fits one page is shipped whole and filtered
	// in the browser, which costs no request at all.
	OptionsURL string
	Search     bool
	// Multiple marks the combobox as a multi-select.
	Multiple bool
	Errors   []string
}

// iconChoiceVM is one option in an Icon field's picker. It carries the name
// only: the glyphs come from the sprite through <use>, because inlining sixteen
// hundred of them would add half a megabyte to the page.
type iconChoiceVM struct {
	Name     string
	Selected bool
}

type formVM struct {
	Title     string
	Action    string
	Method    string // POST | PUT
	Creating  bool
	Fields    []formFieldVM
	Nested    []nestedVM
	CancelURL string
}

// valueString renders a model field for an input's value attribute.
func (fd *Field[T]) valueString(row *T) string {
	if fd.info == nil || row == nil {
		return fmt.Sprint(orEmpty(fd.defaultVal))
	}
	v, ok := fd.info.value(reflect.ValueOf(row))
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case time.Time:
		if x.IsZero() {
			return ""
		}
		switch fd.kind {
		case FieldDate:
			return x.Format("2006-01-02")
		case FieldTime:
			return x.Format("15:04")
		default:
			return x.Format("2006-01-02T15:04")
		}
	case bool:
		if x {
			return "on"
		}
		return ""
	default:
		s := fmt.Sprint(v)
		if fd.kind == FieldBelongsTo && s == "0" {
			return ""
		}
		return s
	}
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func (t *typedResource[T]) buildFormVM(c *Context, row *T, creating bool, errs map[string][]string) *formVM {
	m := t.res.m
	vm := &formVM{
		Title:     m.title,
		Creating:  creating,
		CancelURL: c.URL(m.slug),
	}
	if creating {
		vm.Action, vm.Method = c.URL(m.slug), "POST"
	} else {
		vm.Action, vm.Method = c.URL(m.slug, t.rowKey(row)), "PUT"
	}
	for _, fd := range t.form.fields {
		if fd.divider {
			vm.Fields = append(vm.Fields, formFieldVM{Divider: true})
			continue
		}
		if (fd.info == nil && !fd.virtual) || !fd.visible(c, creating) {
			continue
		}
		fv := formFieldVM{
			Kind:        kindNames[fd.kind],
			Name:        fd.path,
			Label:       fd.label,
			Required:    fd.required,
			Disabled:    fd.disabled,
			ReadOnly:    fd.readOnly,
			Placeholder: fd.placeholder,
			Help:        fd.help,
			Accept:      fd.accept,
			Fieldset:    fd.fieldset,
			Errors:      errs[fd.path],
		}
		if creating && row == nil {
			if fd.defaultVal != nil {
				fv.Value = fmt.Sprint(fd.defaultVal)
			}
		} else {
			fv.Value = fd.valueString(row)
		}
		if fd.kind == FieldPassword {
			fv.Value = ""
		}
		switch fd.kind {
		case FieldSelect, FieldRadio:
			opts := fd.choices(c)
			for val, label := range opts {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: val == fv.Value})
			}
			sortOptions(fv.Options)
			if fd.kind == FieldSelect {
				fv.Search = len(fv.Options) > optionSearchLimit
				if fv.Search {
					fv.Options = firstPage(fv.Options)
					fv.OptionsURL = optionsURL(c, m.slug, fd.path)
				}
			}
		case FieldMultiSelect:
			var selected []string
			if fd.valuesFn != nil && row != nil {
				selected = fd.valuesFn(c, row)
			}
			opts := fd.choices(c)
			for val, label := range opts {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: slices.Contains(selected, val)})
			}
			sortOptions(fv.Options)
			fv.SelectedJSON = selectedJSON(fv.Options)
			fv.Multiple = true
			// Past one page, ship the selection plus enough to make the first
			// open useful and fetch the rest as the reader types. That is what
			// keeps a field over thousands of rows from carrying all of them.
			fv.Search = len(fv.Options) > optionSearchLimit
			if fv.Search {
				fv.Options = firstPage(fv.Options)
				fv.OptionsURL = optionsURL(c, m.slug, fd.path)
			}
		case FieldIcon:
			rend := c.Admin.renderer
			for _, name := range rend.iconNames() {
				fv.Icons = append(fv.Icons, iconChoiceVM{Name: name, Selected: name == fv.Value})
			}
			fv.SpriteURL = c.Admin.url("_assets", rend.assetVersion, spritePath)
			if fv.Value != "" {
				fv.CurrentIcon = rend.icon(fv.Value)
			}
		case FieldBelongsTo:
			// Always fetched: the target is a table, and how big it is now says
			// nothing about how big it will be.
			fv.Options = t.belongsToOptions(c, fd, fv.Value)
			fv.Search = true
			fv.OptionsURL = optionsURL(c, m.slug, fd.path)
		case FieldFile, FieldImage:
			fv.UploadURL = c.URL(m.slug, "_upload") + "?field=" + url.QueryEscape(fd.path)
			if fv.Value != "" {
				fv.PreviewURL = c.Admin.cfg.Storage.URL(fv.Value)
				fv.FileName = displayFileName(fv.Value)
			}
			fv.MaxSizeLabel = sizeLabel(fd.maxSize)
			fv.AcceptLabel = acceptLabel(fd.accept)
		}
		vm.Fields = append(vm.Fields, fv)
	}
	for _, n := range t.form.nested {
		nvm, err := n.buildVM(c, row)
		if err != nil {
			c.Admin.log.Error("steward: nested form", "relation", n.fieldName(), "err", err)
			continue
		}
		vm.Nested = append(vm.Nested, nvm)
	}
	return vm
}

// optionsURL is where a field's combobox fetches its suggestions.
func optionsURL(c *Context, slug, field string) string {
	return c.URL(slug, "_options") + "?field=" + url.QueryEscape(field)
}

// belongsToOptions loads one page of related rows, and the selected row with
// them — the control reads its own label off the option, so a selection missing
// from the list would show as an id.
func (t *typedResource[T]) belongsToOptions(c *Context, fd *Field[T], selected string) []optionVM {
	if fd.relTable == "" {
		return nil
	}
	rows, err := c.Admin.db.WithContext(c.Ctx()).
		Table(fd.relTable).
		Select(fd.relPKCol + ", " + fd.relTitleCol).
		Limit(optionSearchLimit).Rows()
	if err != nil {
		c.Admin.log.Error("steward: belongsTo options", "field", fd.path, "err", err)
		return nil
	}
	defer func() { _ = rows.Close() }()
	var opts []optionVM
	found := false
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			continue
		}
		if id == selected {
			found = true
		}
		opts = append(opts, optionVM{Value: id, Label: title, Selected: id == selected})
	}
	sortOptions(opts)

	// The selected row need not be in the first page, and the control has
	// nowhere but its option to read a label from.
	if selected != "" && !found {
		var title string
		err := c.Admin.db.WithContext(c.Ctx()).
			Table(fd.relTable).
			Select(fd.relTitleCol).
			Where(fd.relPKCol+" = ?", selected).
			Limit(1).Scan(&title).Error
		if err == nil && title != "" {
			opts = append([]optionVM{{Value: selected, Label: title, Selected: true}}, opts...)
		}
	}
	return opts
}

// ---- handlers ---------------------------------------------------------------

func (t *typedResource[T]) createPage(c *Context) error {
	if !t.canCreate(c) {
		return t.denyPolicy(c)
	}
	vm := t.buildFormVM(c, nil, true, nil)
	return c.Admin.render(c, "form/page.html", "New "+t.res.m.title, vm)
}

func (t *typedResource[T]) editPage(c *Context) error {
	row, err := t.repo.Find(c.Ctx(), c.R.PathValue("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Admin.renderError(c, http.StatusNotFound, "Record not found", nil)
			return nil
		}
		return err
	}
	if !t.canUpdate(c, row) {
		return t.denyPolicy(c)
	}
	vm := t.buildFormVM(c, row, false, nil)
	return c.Admin.render(c, "form/page.html", "Edit "+t.res.m.title, vm)
}

// formValue fetches a submitted value; ok=false when absent entirely.
func formValue(r *http.Request, name string) (string, bool) {
	if r.Form == nil {
		return "", false
	}
	vs, ok := r.Form[name]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}

// checkboxPresence: switches submit nothing when off, so an edit form marks
// presence with a companion marker input.
func switchPresent(r *http.Request, name string) bool {
	_, marker := r.Form["_present_"+name]
	return marker
}

func (t *typedResource[T]) store(c *Context) error {
	return t.save(c, "", true)
}

func (t *typedResource[T]) update(c *Context) error {
	return t.save(c, c.R.PathValue("id"), false)
}

// save is the shared create/update pipeline: hooks → validate → decode →
// persist → envelope.
// sortOptions orders a field's choices by what the reader sees. Options are
// declared as a map, whose iteration order Go randomises, so without this a
// select's contents move between requests.
func sortOptions(opts []optionVM) {
	sort.SliceStable(opts, func(i, j int) bool {
		a, b := opts[i], opts[j]
		if strings.EqualFold(a.Label, b.Label) {
			return a.Value < b.Value
		}
		return strings.ToLower(a.Label) < strings.ToLower(b.Label)
	})
}

// firstPage keeps the selection and enough of the rest to fill one page, so a
// field over thousands of options renders a page rather than a catalogue.
func firstPage(opts []optionVM) []optionVM {
	out := make([]optionVM, 0, optionSearchLimit)
	for _, o := range opts {
		if o.Selected {
			out = append(out, o)
		}
	}
	for _, o := range opts {
		if len(out) >= optionSearchLimit {
			break
		}
		if !o.Selected {
			out = append(out, o)
		}
	}
	return out
}

// selectedJSON renders a MultiSelect's selection the way its combobox stores it.
//
// Objects rather than bare values, because the suggestion list is fetched and
// replaced as the reader types: a chip whose option is no longer in the DOM has
// nowhere else to read its label from, and would show its id instead.
func selectedJSON(opts []optionVM) string {
	sel := make([]map[string]string, 0)
	for _, o := range opts {
		if o.Selected {
			sel = append(sel, map[string]string{"value": o.Value, "label": o.Label})
		}
	}
	out, err := json.Marshal(sel)
	if err != nil {
		return "[]"
	}
	return string(out)
}

// decodeSelection reads a combobox's submitted array, in either shape it can
// take: bare values, or the {value,label} objects a manually-filtered field
// stores so its chips keep their text.
func decodeSelection(raw string) ([]string, bool) {
	var entries []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err == nil {
		vals := make([]string, 0, len(entries))
		for _, e := range entries {
			vals = append(vals, e.Value)
		}
		return vals, true
	}
	var vals []string
	if err := json.Unmarshal([]byte(raw), &vals); err == nil {
		return vals, true
	}
	return nil, false
}

// expandMultiSelects turns each MultiSelect's submitted JSON array back into
// repeated form values.
//
// The combobox submits one hidden input holding `["1","2"]`. Every handler and
// hook reads c.R.Form[name] as a plain list, and that is the contract worth
// keeping — so the shape the widget happens to use is undone here, once, rather
// than in every application that reads one.
//
// A value that is not a JSON array is left alone, so a plain <select multiple>,
// or a client posting the field the ordinary way, still works.
func (t *typedResource[T]) expandMultiSelects(c *Context) {
	if c.R.Form == nil {
		return
	}
	for _, fd := range t.form.fields {
		if fd.kind != FieldMultiSelect {
			continue
		}
		raw := c.R.Form[fd.path]
		if len(raw) != 1 || !strings.HasPrefix(strings.TrimSpace(raw[0]), "[") {
			continue
		}
		vals, ok := decodeSelection(raw[0])
		if !ok {
			continue
		}
		c.R.Form[fd.path] = vals
		if c.R.PostForm != nil {
			c.R.PostForm[fd.path] = vals
		}
	}
}

func (t *typedResource[T]) save(c *Context, id string, creating bool) error {
	f := t.form
	if f.submittedFn != nil {
		if err := f.submittedFn(c); err != nil {
			return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
		}
	}
	if err := c.R.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return err
	}
	// Before anything reads the form, so a hook sees the ordinary shape.
	t.expandMultiSelects(c)

	// Load the target model.
	var m *T
	if creating {
		if !t.canCreate(c) {
			return c.Envelope(Error("You do not have permission to create this.").Code(http.StatusForbidden))
		}
		m = new(T)
	} else {
		var err error
		m, err = t.repo.Find(c.Ctx(), id)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.Envelope(Error("Record not found.").Code(http.StatusNotFound))
			}
			return err
		}
		if !t.canUpdate(c, m) {
			return c.Envelope(Error("You do not have permission to update this.").Code(http.StatusForbidden))
		}
	}

	// Validate + decode submitted fields.
	errs := map[string][]string{}
	type pending struct {
		fd  *Field[T]
		val any
	}
	var writes []pending
	var dirty []string

	for _, fd := range f.fields {
		if fd.info == nil || fd.ignored || !fd.visible(c, creating) {
			continue
		}
		raw, present := formValue(c.R, fd.path)
		if fd.kind == FieldSwitch {
			if !switchPresent(c.R, fd.path) && !present {
				continue
			}
			present = true
		}
		if !present {
			if creating && fd.required {
				errs[fd.path] = append(errs[fd.path], fd.label+" is required.")
			}
			continue
		}
		if fd.kind == FieldPassword && raw == "" && !creating {
			continue // blank password on edit = keep current
		}

		spec := fd.rules
		if creating && fd.createRules != "" {
			spec += "|" + fd.createRules
		}
		if !creating && fd.updateRules != "" {
			spec += "|" + fd.updateRules
		}
		if spec != "" {
			target := rules.Field{DB: c.Admin.db, Ctx: c.Ctx(), Label: fd.label, RecordID: id}
			if msgs := rules.Validate(target, spec, raw); len(msgs) > 0 {
				errs[fd.path] = append(errs[fd.path], msgs...)
				continue
			}
		}

		var val any
		var err error
		if fd.savingValue != nil {
			val, err = fd.savingValue(c, raw)
		} else {
			val, err = fd.decode(raw)
		}
		if err != nil {
			errs[fd.path] = append(errs[fd.path], fd.label+": "+err.Error())
			continue
		}
		writes = append(writes, pending{fd, val})
		dirty = append(dirty, fd.path)
	}

	// Nested rows validate alongside the parent so one 422 carries all
	// errors; their writes wait until the parent has a primary key.
	nestedPayloads := make([]any, len(f.nested))
	for i, n := range f.nested {
		payload, nerrs := n.validate(c)
		for k, v := range nerrs {
			errs[k] = append(errs[k], v...)
		}
		nestedPayloads[i] = payload
	}

	if len(errs) > 0 {
		return c.Envelope(ValidationErrors(errs))
	}

	for _, w := range writes {
		if err := setField(m, w.fd.info, w.val); err != nil {
			return fmt.Errorf("setting %s: %w", w.fd.path, err)
		}
	}

	if f.savingFn != nil {
		if err := f.savingFn(c, m); err != nil {
			return c.Envelope(Error(err.Error()).Code(http.StatusBadRequest))
		}
	}

	var err error
	if creating {
		err = t.repo.Create(c.Ctx(), m)
	} else {
		err = t.repo.Update(c.Ctx(), m, dirty)
	}
	if err != nil {
		return err
	}
	for i, n := range f.nested {
		if err := n.persist(c, m, nestedPayloads[i]); err != nil {
			return fmt.Errorf("saving %s rows: %w", n.fieldName(), err)
		}
	}
	if f.savedFn != nil {
		if err := f.savedFn(c, m, creating); err != nil {
			c.Admin.log.Error("steward: saved hook", "err", err)
		}
	}

	// Inline single-field edits stay on the page: toast only, no redirect.
	if c.R.FormValue("_inline") == "1" {
		return c.Envelope(Success("Saved."))
	}
	verb := "updated"
	if creating {
		verb = "created"
	}
	c.Flash("success", t.res.m.title+" "+verb+".")
	return c.Envelope(Success(t.res.m.title + " " + verb + ".").Redirect(c.URL(t.res.m.slug)))
}

// setField writes a decoded value into the model via reflection, converting
// to the target type and allocating pointers as needed.
func setField[T any](m *T, info *fieldInfo, val any) error {
	fv := reflect.ValueOf(m).Elem()
	for i, idx := range info.index {
		fv = fv.Field(idx)
		if i < len(info.index)-1 {
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					fv.Set(reflect.New(fv.Type().Elem()))
				}
				fv = fv.Elem()
			}
		}
	}

	target := fv.Type()
	if val == nil {
		fv.Set(reflect.Zero(target))
		return nil
	}
	vv := reflect.ValueOf(val)

	// Pointer targets: set through a new allocation.
	if target.Kind() == reflect.Pointer {
		inner := reflect.New(target.Elem())
		if err := assign(inner.Elem(), vv); err != nil {
			return err
		}
		fv.Set(inner)
		return nil
	}
	return assign(fv, vv)
}

func assign(dst, src reflect.Value) error {
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return nil
	}
	if src.Type().ConvertibleTo(dst.Type()) {
		dst.Set(src.Convert(dst.Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %s to %s", src.Type(), dst.Type())
}

// ---- schema + options + upload ------------------------------------------------

// schemaJSON exposes form/grid field metadata for headless clients.
func (t *typedResource[T]) schemaJSON(c *Context) error {
	if !t.canViewAny(c) {
		return t.denyPolicy(c)
	}
	type schemaField struct {
		Name     string  `json:"name"`
		Kind     string  `json:"kind"`
		Label    string  `json:"label"`
		Required bool    `json:"required"`
		Options  Options `json:"options,omitempty"`
		Rules    string  `json:"rules,omitempty"`
	}
	out := struct {
		Slug   string        `json:"slug"`
		Title  string        `json:"title"`
		Key    string        `json:"key"`
		Fields []schemaField `json:"fields"`
	}{Slug: t.res.m.slug, Title: t.res.m.title, Key: t.ft.pk.Path}
	for _, fd := range t.form.fields {
		// Report the form this caller would actually be served, so a Show
		// predicate does not advertise a field their submission cannot write.
		if fd.info == nil || fd.divider || !fd.visible(c, true) {
			continue
		}
		out.Fields = append(out.Fields, schemaField{
			Name: fd.path, Kind: kindNames[fd.kind], Label: fd.label,
			Required: fd.required, Options: fd.options, Rules: fd.rules,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// optionsJSON serves BelongsTo/select search: GET {slug}/_options?field=X&q=.
func (t *typedResource[T]) optionsJSON(c *Context) error {
	if !t.canViewAny(c) {
		return t.denyPolicy(c)
	}
	name := c.R.URL.Query().Get("field")
	for _, fd := range t.form.fields {
		if fd.path != name {
			continue
		}
		if fd.kind == FieldBelongsTo && fd.relTable != "" {
			q := strings.TrimSpace(c.R.URL.Query().Get("q"))
			db := c.Admin.db.WithContext(c.Ctx()).Table(fd.relTable).
				Select(fd.relPKCol + ", " + fd.relTitleCol).Limit(50)
			if q != "" {
				db = db.Where(fd.relTitleCol+" LIKE ?", "%"+q+"%")
			}
			rows, err := db.Rows()
			if err != nil {
				return err
			}
			defer func() { _ = rows.Close() }()
			var opts []map[string]string
			for rows.Next() {
				var id, title string
				if err := rows.Scan(&id, &title); err != nil {
					continue
				}
				opts = append(opts, map[string]string{"value": id, "label": title})
			}
			return c.JSON(http.StatusOK, map[string]any{"options": opts})
		}
		if fd.kind == FieldMultiSelect || fd.kind == FieldSelect {
			list, more := searchOptions(fd.choices(c), c.R.URL.Query().Get("q"))
			out := make([]map[string]string, 0, len(list))
			for _, o := range list {
				out = append(out, map[string]string{"value": o.Value, "label": o.Label})
			}
			return c.JSON(http.StatusOK, map[string]any{"options": out, "more": more})
		}
		return c.JSON(http.StatusOK, map[string]any{"options": fd.choices(c)})
	}
	return c.JSON(http.StatusNotFound, Error("unknown field"))
}

// optionSearchLimit bounds one page of a combobox's suggestions. The list is
// there to be typed at, not scrolled through.
const optionSearchLimit = 50

// choices resolves a field's options, whichever way they were declared.
func (fd *Field[T]) choices(c *Context) Options {
	if fd.optionsFn != nil {
		return fd.optionsFn(c)
	}
	return fd.options
}

// searchOptions filters, orders and truncates a field's options, reporting
// whether anything was left out so the reader can be told to keep typing.
//
// The match runs in Go rather than SQL: options arrive as a map from whatever
// OptionsFunc chose to do, and the framework cannot push a predicate into a
// query it never saw.
func searchOptions(opts Options, query string) (list []optionVM, more bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	list = make([]optionVM, 0, len(opts))
	for val, label := range opts {
		if q != "" && !strings.Contains(strings.ToLower(label), q) {
			continue
		}
		list = append(list, optionVM{Value: val, Label: label})
	}
	sortOptions(list)
	if len(list) > optionSearchLimit {
		return list[:optionSearchLimit], true
	}
	return list, false
}

// uploadFile handles POST {slug}/_upload?field=X (multipart "file").
// Gated by ViewAny only: the form may be a create or an edit, and the
// written model is policy-checked again at save time.
func (t *typedResource[T]) uploadFile(c *Context) error {
	if !t.canViewAny(c) {
		return t.denyPolicy(c)
	}
	name := c.R.URL.Query().Get("field")
	var target *Field[T]
	for _, fd := range t.form.fields {
		if fd.path == name && (fd.kind == FieldFile || fd.kind == FieldImage) {
			target = fd
			break
		}
	}
	if target == nil {
		return c.JSON(http.StatusNotFound, Error("unknown upload field"))
	}
	maxSize := target.maxSize
	if maxSize <= 0 {
		maxSize = defaultMaxUpload
	}
	c.R.Body = http.MaxBytesReader(c.W, c.R.Body, maxSize+1<<20)
	if err := c.R.ParseMultipartForm(maxSize); err != nil {
		return c.Envelope(Error("Upload too large or malformed.").Code(http.StatusRequestEntityTooLarge))
	}
	file, header, err := c.R.FormFile("file")
	if err != nil {
		return c.Envelope(Error("No file received.").Code(http.StatusBadRequest))
	}
	defer func() { _ = file.Close() }()
	if header.Size > maxSize {
		return c.Envelope(Error(fmt.Sprintf("File exceeds the %d MB limit.", maxSize>>20)).Code(http.StatusRequestEntityTooLarge))
	}

	ext := strings.ToLower(path.Ext(header.Filename))
	if target.kind == FieldImage && !allowedImageExt[ext] {
		return c.Envelope(Error("Only image files are allowed here.").Code(http.StatusUnsupportedMediaType))
	}
	if target.kind == FieldFile && activeExt[ext] {
		return c.Envelope(Error("That file type cannot be uploaded.").Code(http.StatusUnsupportedMediaType))
	}
	if !acceptAllows(target.accept, ext) {
		return c.Envelope(Error("Only " + acceptLabel(target.accept) + " can be uploaded here.").
			Code(http.StatusUnsupportedMediaType))
	}
	tok, err := session.NewToken()
	if err != nil {
		return err
	}
	dir := target.dir
	if dir == "" {
		dir = t.res.m.slug
	}
	// The original name is kept after the unique part: a download prompt, and
	// the field itself, would otherwise offer nothing but a token.
	stored := dir + "/" + time.Now().Format("20060102") + "-" + tok[:12]
	if base := safeFileBase(header.Filename); base != "" {
		stored += "-" + base
	}
	stored += ext
	url, err := c.Admin.cfg.Storage.Put(c.Ctx(), stored, file, header.Size, header.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"status": true, "path": stored, "url": url})
}

// defaultMaxUpload bounds a field that names no limit of its own.
const defaultMaxUpload = 8 << 20

var allowedImageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": false, ".avif": true,
}

// activeExt lists what a browser will execute or render as a document rather
// than hand to the reader as bytes. Uploads are served with an attachment
// disposition, which is the boundary; this refuses them outright as well, so a
// File field cannot become a page-hosting service by accident.
//
// SVG is here for the same reason it is false above: it is a document format
// that carries script.
var activeExt = map[string]bool{
	".html": true, ".htm": true, ".xhtml": true, ".shtml": true, ".xml": true,
	".svg": true, ".js": true, ".mjs": true, ".css": true, ".swf": true,
	".jar": true, ".htaccess": true, ".php": true, ".phtml": true,
}

// storedNamePrefix matches the date-and-token prefix a stored upload carries, so
// a name can be shown without it.
var storedNamePrefix = regexp.MustCompile(`^\d{8}-[A-Za-z0-9_-]{12}-?`)

// displayFileName is what an upload is called on screen. Rows written before
// the original name was kept, and rows carried over from another system, have no
// prefix to strip and show whatever they hold.
func displayFileName(stored string) string {
	base := path.Base(stored)
	if trimmed := storedNamePrefix.ReplaceAllString(base, ""); trimmed != "" {
		return trimmed
	}
	return base
}

// safeFileBase reduces a submitted filename to something safe to put in a path
// and still recognisable to whoever uploaded it. Everything outside the allowed
// set becomes a dash, because a stored name ends up in a URL, on a filesystem,
// and in a download prompt.
func safeFileBase(name string) string {
	base := strings.TrimSuffix(path.Base(filepath.ToSlash(name)), path.Ext(name))
	var b strings.Builder
	dash := false
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '.' || r == '_' || r == '-' || r == ' ':
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 60 {
			break
		}
	}
	return strings.Trim(b.String(), "-.")
}

// sizeLabel renders a byte limit the way a person would say it.
func sizeLabel(n int64) string {
	if n <= 0 {
		n = defaultMaxUpload
	}
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// acceptLabel turns an accept attribute into something readable, so a field can
// say what it takes before refusing something.
func acceptLabel(accept string) string {
	accept = strings.TrimSpace(accept)
	switch accept {
	case "", "*/*":
		return ""
	case "image/*":
		return "images"
	}
	parts := strings.Split(accept, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		out = append(out, strings.ToUpper(strings.TrimPrefix(p, ".")))
	}
	return strings.Join(out, ", ")
}

// uploadTypes maps the extensions people name in an Accept that Go's own table
// does not carry. Go falls back to the host's MIME database, which a scratch
// container has none of — so without this an accept rule would hold on a
// developer's machine and reject the same file in production.
var uploadTypes = map[string]string{
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".csv":  "text/csv",
	".txt":  "text/plain",
	".rtf":  "application/rtf",
	".zip":  "application/zip",
	".mp4":  "video/mp4",
	".mp3":  "audio/mpeg",
}

// extType is the media type an extension stands for, resolved the same way
// everywhere this runs.
func extType(ext string) string {
	if t, ok := uploadTypes[ext]; ok {
		return t
	}
	t := mime.TypeByExtension(ext)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

// acceptAllows reports whether a field's Accept admits this file.
//
// Accept takes what the HTML attribute takes — extensions (".pdf"), types
// ("application/pdf") and wildcards ("image/*") — so one declaration serves the
// browser's file picker and this check.
//
// The type is derived from the extension, never from the Content-Type the
// client sent: that header is chosen by whoever is uploading, so trusting it
// would make the check decorative. An extension the table cannot type therefore
// only matches a rule naming that extension, which is why naming extensions is
// the dependable form.
func acceptAllows(accept, ext string) bool {
	accept = strings.TrimSpace(accept)
	if accept == "" || accept == "*/*" {
		return true
	}
	actual := extType(strings.ToLower(ext))
	for _, rule := range strings.Split(accept, ",") {
		rule = strings.ToLower(strings.TrimSpace(rule))
		switch {
		case rule == "":
			continue
		case strings.HasPrefix(rule, "."):
			if rule == strings.ToLower(ext) {
				return true
			}
		case strings.HasSuffix(rule, "/*"):
			if actual != "" && strings.HasPrefix(actual, strings.TrimSuffix(rule, "*")) {
				return true
			}
		default:
			if actual != "" && actual == rule {
				return true
			}
		}
	}
	return false
}
