package steward

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
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
	// Icons carries the choices for an Icon field, each with its rendered SVG.
	Icons      []iconChoiceVM
	UploadURL  string
	PreviewURL string
	Errors     []string
}

// iconChoiceVM is one option in an Icon field's picker.
type iconChoiceVM struct {
	Name     string
	SVG      template.HTML
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
			opts := fd.options
			if fd.optionsFn != nil {
				opts = fd.optionsFn(c)
			}
			for val, label := range opts {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: val == fv.Value})
			}
		case FieldMultiSelect:
			opts := fd.options
			if fd.optionsFn != nil {
				opts = fd.optionsFn(c)
			}
			var selected []string
			if fd.valuesFn != nil && row != nil {
				selected = fd.valuesFn(c, row)
			}
			for val, label := range opts {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: slices.Contains(selected, val)})
			}
		case FieldIcon:
			for _, name := range iconNames(c.Admin.renderer.assetLayers) {
				fv.Icons = append(fv.Icons, iconChoiceVM{
					Name:     name,
					SVG:      c.Admin.renderer.icon(name),
					Selected: name == fv.Value,
				})
			}
		case FieldBelongsTo:
			fv.Options = t.belongsToOptions(c, fd, fv.Value)
		case FieldFile, FieldImage:
			fv.UploadURL = c.URL(m.slug, "_upload") + "?field=" + url.QueryEscape(fd.path)
			if fv.Value != "" {
				fv.PreviewURL = c.Admin.cfg.Storage.URL(fv.Value)
			}
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

// belongsToOptions loads up to 200 related rows as select options.
func (t *typedResource[T]) belongsToOptions(c *Context, fd *Field[T], selected string) []optionVM {
	if fd.relTable == "" {
		return nil
	}
	rows, err := c.Admin.db.WithContext(c.Ctx()).
		Table(fd.relTable).
		Select(fd.relPKCol + ", " + fd.relTitleCol).
		Limit(200).Rows()
	if err != nil {
		c.Admin.log.Error("steward: belongsTo options", "field", fd.path, "err", err)
		return nil
	}
	defer func() { _ = rows.Close() }()
	var opts []optionVM
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			continue
		}
		opts = append(opts, optionVM{Value: id, Label: title, Selected: id == selected})
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
		opts := fd.options
		if fd.optionsFn != nil {
			opts = fd.optionsFn(c)
		}
		return c.JSON(http.StatusOK, map[string]any{"options": opts})
	}
	return c.JSON(http.StatusNotFound, Error("unknown field"))
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
		maxSize = 8 << 20
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
	tok, err := session.NewToken()
	if err != nil {
		return err
	}
	dir := target.dir
	if dir == "" {
		dir = t.res.m.slug
	}
	stored := dir + "/" + time.Now().Format("20060102") + "-" + tok[:12] + ext
	url, err := c.Admin.cfg.Storage.Put(c.Ctx(), stored, file, header.Size, header.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"status": true, "path": stored, "url": url})
}

var allowedImageExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": false, ".avif": true,
}
