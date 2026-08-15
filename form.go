package steward

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/imfiqhan/steward/internal/htmlsafe"
)

// FieldKind selects a form field's input widget and decode behavior.
type FieldKind int

// Form field kinds available in v1.
const (
	FieldText FieldKind = iota
	FieldTextarea
	FieldEmail
	FieldPassword
	FieldURL
	FieldNumber
	FieldDecimal
	FieldCurrency
	FieldHidden
	FieldDisplay
	FieldSelect
	FieldRadio
	FieldSwitch
	FieldColor
	FieldDate
	FieldDatetime
	FieldTime
	FieldFile
	FieldImage
	FieldMarkdown
	FieldBelongsTo
	FieldMultiSelect
	FieldRichtext
	FieldIcon
	FieldFiles
	FieldImages
)

// kindNames map kinds to template partials and schema strings.
var kindNames = map[FieldKind]string{
	FieldText: "text", FieldTextarea: "textarea", FieldEmail: "email",
	FieldPassword: "password", FieldURL: "url", FieldNumber: "number",
	FieldDecimal: "decimal", FieldCurrency: "currency", FieldHidden: "hidden",
	FieldDisplay: "display", FieldSelect: "select", FieldRadio: "radio",
	FieldSwitch: "switch", FieldColor: "color", FieldDate: "date",
	FieldDatetime: "datetime", FieldTime: "time", FieldFile: "file",
	FieldImage: "image", FieldMarkdown: "markdown", FieldBelongsTo: "belongsto",
	FieldMultiSelect: "multiselect", FieldRichtext: "richtext",
	FieldIcon: "icon", FieldFiles: "files", FieldImages: "images",
}

// Form configures a resource's create/edit view; write-only until Build.
// Hooks receive the typed model, never a map.
type Form[T any] struct {
	res    *Resource[T]
	fields []*Field[T]
	nested []nestedForm[T]
	width  FormWidth

	submittedFn func(c *Context) error
	savingFn    func(c *Context, m *T) error
	savedFn     func(c *Context, m *T, created bool) error
	deletingFn  func(c *Context, ids []string) error
	deletedFn   func(c *Context, ids []string) error
}

func newForm[T any](res *Resource[T]) *Form[T] { return &Form[T]{res: res} }

func (f *Form[T]) add(kind FieldKind, path string, label ...string) *Field[T] {
	fd := &Field[T]{form: f, kind: kind, path: path}
	if len(label) > 0 {
		fd.label = label[0]
	}
	f.fields = append(f.fields, fd)
	return fd
}

// Text adds a single-line text input.
func (f *Form[T]) Text(path string, label ...string) *Field[T] {
	return f.add(FieldText, path, label...)
}

// Textarea adds a multi-line input.
func (f *Form[T]) Textarea(path string, label ...string) *Field[T] {
	return f.add(FieldTextarea, path, label...)
}

// Email adds an email input (browser + server validation).
func (f *Form[T]) Email(path string, label ...string) *Field[T] {
	return f.add(FieldEmail, path, label...).Rules("email")
}

// Password adds a password input; empty submissions are ignored on edit.
func (f *Form[T]) Password(path string, label ...string) *Field[T] {
	return f.add(FieldPassword, path, label...)
}

// URL adds a URL input.
func (f *Form[T]) URL(path string, label ...string) *Field[T] {
	return f.add(FieldURL, path, label...).Rules("url")
}

// Number adds an integer input.
func (f *Form[T]) Number(path string, label ...string) *Field[T] {
	return f.add(FieldNumber, path, label...)
}

// Decimal adds a decimal-number input.
func (f *Form[T]) Decimal(path string, label ...string) *Field[T] {
	return f.add(FieldDecimal, path, label...)
}

// Currency adds a decimal input with a currency prefix.
func (f *Form[T]) Currency(path string, label ...string) *Field[T] {
	return f.add(FieldCurrency, path, label...)
}

// Hidden adds a hidden input.
func (f *Form[T]) Hidden(path string, label ...string) *Field[T] {
	return f.add(FieldHidden, path, label...)
}

// Display shows the value read-only and never persists it.
func (f *Form[T]) Display(path string, label ...string) *Field[T] {
	fd := f.add(FieldDisplay, path, label...)
	fd.ignored = true
	return fd
}

// Select adds a dropdown; supply Options or OptionsFunc.
func (f *Form[T]) Select(path string, label ...string) *Field[T] {
	return f.add(FieldSelect, path, label...)
}

// Radio adds a radio group.
func (f *Form[T]) Radio(path string, label ...string) *Field[T] {
	return f.add(FieldRadio, path, label...)
}

// Switch adds an on/off toggle bound to a bool field.
func (f *Form[T]) Switch(path string, label ...string) *Field[T] {
	return f.add(FieldSwitch, path, label...)
}

// Color adds a color picker storing "#rrggbb".
func (f *Form[T]) Color(path string, label ...string) *Field[T] {
	return f.add(FieldColor, path, label...)
}

// Date adds a date picker (stored midnight local).
func (f *Form[T]) Date(path string, label ...string) *Field[T] {
	return f.add(FieldDate, path, label...)
}

// Datetime adds a date+time picker.
func (f *Form[T]) Datetime(path string, label ...string) *Field[T] {
	return f.add(FieldDatetime, path, label...)
}

// Time adds a time-of-day input stored as "15:04".
func (f *Form[T]) Time(path string, label ...string) *Field[T] {
	return f.add(FieldTime, path, label...)
}

// File adds an upload field storing the file path.
func (f *Form[T]) File(path string, label ...string) *Field[T] {
	return f.add(FieldFile, path, label...)
}

// Image adds an image upload with preview.
func (f *Form[T]) Image(path string, label ...string) *Field[T] {
	fd := f.add(FieldImage, path, label...)
	fd.accept = "image/*"
	return fd
}

// Files adds a multi-file upload. The column holds a JSON array of storage
// paths, in the order they were added.
//
// It is for files the panel shows and hands over and never asks questions
// about: a JSON column cannot be filtered, sorted or counted in SQL. Where the
// panel should list them, caption them or order them, model each as a row and
// use HasMany instead.
func (f *Form[T]) Files(path string, label ...string) *Field[T] {
	return f.add(FieldFiles, path, label...)
}

// Images is Files for pictures: the same JSON array, thumbnails instead of
// names, and the image-only rule File carries.
func (f *Form[T]) Images(path string, label ...string) *Field[T] {
	fd := f.add(FieldImages, path, label...)
	fd.accept = "image/*"
	return fd
}

// Markdown adds a markdown editor (textarea with preview styling).
func (f *Form[T]) Markdown(path string, label ...string) *Field[T] {
	return f.add(FieldMarkdown, path, label...)
}

// Richtext adds a small WYSIWYG editor storing HTML: bold, italic, underline,
// headings, lists, links, blockquote, and clear-formatting.
//
// Submitted markup is sanitized server-side against an allowlist of tags and
// attributes (see internal/htmlsafe), because a contenteditable field is an
// arbitrary-HTML input and the value is rendered back with Detail.HTML. Anything
// outside the allowlist is dropped, so a compromised or scripted client cannot
// store markup that later executes in another admin's browser.
//
// It is deliberately modest. A field that needs image handling, tables, or
// pasted-Word cleanup wants a dedicated editor, which belongs in the app rather
// than vendored into the framework.
func (f *Form[T]) Richtext(path string, label ...string) *Field[T] {
	return f.add(FieldRichtext, path, label...)
}

// Icon adds a visual picker over the icons the panel can render, storing the
// chosen name as a string.
//
// It exists because the alternative — a text input holding an icon name — fails
// silently: a typo renders a blank space, and nobody notices until a sidebar
// looks wrong. The picker can only produce a name that resolves.
//
// Choices come from the asset layers, so icons dropped into Config.AssetsFS
// appear alongside the embedded set.
func (f *Form[T]) Icon(path string, label ...string) *Field[T] {
	return f.add(FieldIcon, path, label...)
}

// BelongsTo binds a foreign-key field to a searchable select over the
// relation: BelongsTo("AuthorID", "Author", "Name").
func (f *Form[T]) BelongsTo(fkPath, relation, titleField string, label ...string) *Field[T] {
	fd := f.add(FieldBelongsTo, fkPath, label...)
	fd.relName = relation
	fd.relTitle = titleField
	return fd
}

// MultiSelect adds a multiple-choice select. It is virtual by default: the
// submitted values never write to a model column — read them in a Saved
// hook via c.R.Form[name] (used for pivot syncing, tags, etc.). Supply the
// current selection with ValuesFunc.
func (f *Form[T]) MultiSelect(name string, label ...string) *Field[T] {
	fd := f.add(FieldMultiSelect, name, label...)
	fd.virtual = true
	fd.ignored = true
	return fd
}

// formColumns is the number of columns a form row is divided into. Twelve
// divides by 2, 3, 4 and 6, which covers the rows people actually build.
const formColumns = 12

// FormWidth caps how wide a form grows on a large screen.
type FormWidth string

const (
	// FormNarrow suits a form of a few short fields, where full width would
	// leave the labels stranded a long way from their controls.
	FormNarrow FormWidth = "narrow"
	// FormNormal is the default.
	FormNormal FormWidth = "normal"
	// FormWide suits a form with several fields to a row.
	FormWide FormWidth = "wide"
	// FormFull uses whatever the page gives it.
	FormFull FormWidth = "full"
)

// class is the width the form renders at.
func (w FormWidth) class() string {
	switch w {
	case FormNarrow:
		return "max-w-xl"
	case FormWide:
		return "max-w-5xl"
	case FormFull:
		return "max-w-none"
	default:
		return "max-w-3xl"
	}
}

// Width caps how wide the form grows. The default suits a single column of
// fields; a form using Span to put several on a row usually wants more.
func (f *Form[T]) Width(w FormWidth) *Form[T] { f.width = w; return f }

// Divider inserts a horizontal rule between fields.
func (f *Form[T]) Divider() {
	f.fields = append(f.fields, &Field[T]{form: f, kind: FieldDisplay, divider: true, ignored: true})
}

// Fieldset groups the fields declared inside fn under a legend.
func (f *Form[T]) Fieldset(title string, fn func(*Form[T])) {
	start := len(f.fields)
	fn(f)
	for i := start; i < len(f.fields); i++ {
		if f.fields[i].fieldset == "" {
			f.fields[i].fieldset = title
		}
	}
}

// Submitted runs before input decoding; returning an error aborts with it.
func (f *Form[T]) Submitted(fn func(c *Context) error) *Form[T] { f.submittedFn = fn; return f }

// Saving runs after validation on the populated model, before persistence.
func (f *Form[T]) Saving(fn func(c *Context, m *T) error) *Form[T] { f.savingFn = fn; return f }

// Saved runs after successful persistence.
func (f *Form[T]) Saved(fn func(c *Context, m *T, created bool) error) *Form[T] {
	f.savedFn = fn
	return f
}

// Deleting runs before rows are deleted.
func (f *Form[T]) Deleting(fn func(c *Context, ids []string) error) *Form[T] {
	f.deletingFn = fn
	return f
}

// Deleted runs after rows are deleted.
func (f *Form[T]) Deleted(fn func(c *Context, ids []string) error) *Form[T] {
	f.deletedFn = fn
	return f
}

// Field configures one form input; methods chain.
type Field[T any] struct {
	form *Form[T]
	path string

	kind     FieldKind
	label    string
	fieldset string
	divider  bool

	rules       string
	createRules string
	updateRules string

	defaultVal  any
	placeholder string
	help        string
	required    bool
	disabled    bool
	readOnly    bool
	ignored     bool
	onlyCreate  bool
	onlyUpdate  bool

	options   Options
	optionsFn func(c *Context) Options

	// uploads
	dir      string
	maxSize  int64
	symbol   string
	span     int
	maxFiles int
	minVal   any
	maxVal   any
	accept   string

	// belongsTo
	relName     string
	relTitle    string
	relTable    string // resolved at compile
	relPKCol    string
	relTitleCol string

	savingValue func(c *Context, raw string) (any, error)

	// virtual fields render and submit but never touch a model column.
	virtual  bool
	valuesFn func(c *Context, m any) []string

	showFn func(c *Context) bool

	info *fieldInfo
}

// Show gates the field on a per-request predicate — the seam for a form whose
// shape depends on who is filling it in:
//
//	f.Select("Status").Show(func(c *steward.Context) bool {
//	    return c.User.HasRole("editor")
//	})
//
// A hidden field is skipped when the form renders *and* when a submission is
// decoded, so a caller who forges the input cannot write it. It is also omitted
// from the resource's _schema response, so a headless client is not told about
// a field it may not set.
//
// Because the field never decodes, nothing writes the column — Default is a
// render-time value for the input and does not apply. Supply the value in a
// Saving hook (or leave it to the column's database default):
//
//	f.Select("Status").Options(...).Show(isEditor)
//	f.Saving(func(c *steward.Context, p *Post) error {
//	    if !isEditor(c) {
//	        p.Status = "draft"
//	    }
//	    return nil
//	})
func (fd *Field[T]) Show(fn func(c *Context) bool) *Field[T] { fd.showFn = fn; return fd }

// Rules sets Laravel-style validation ("required|max:255|unique:posts,title,{id}").
func (fd *Field[T]) Rules(rules string) *Field[T] {
	if fd.rules != "" {
		fd.rules += "|"
	}
	fd.rules += rules
	if strings.Contains(rules, "required") {
		fd.required = true
	}
	return fd
}

// CreationRules adds rules applied only when creating.
func (fd *Field[T]) CreationRules(rules string) *Field[T] { fd.createRules = rules; return fd }

// UpdateRules adds rules applied only when updating.
func (fd *Field[T]) UpdateRules(rules string) *Field[T] { fd.updateRules = rules; return fd }

// Required marks the field required (adds the rule and the asterisk).
func (fd *Field[T]) Required() *Field[T] { return fd.Rules("required") }

// Default supplies the initial value on the create form.
func (fd *Field[T]) Default(v any) *Field[T] { fd.defaultVal = v; return fd }

// Placeholder sets the input placeholder.
func (fd *Field[T]) Placeholder(s string) *Field[T] { fd.placeholder = s; return fd }

// Help renders hint text under the input.
func (fd *Field[T]) Help(s string) *Field[T] { fd.help = s; return fd }

// Disable renders the input disabled (and ignores submissions).
func (fd *Field[T]) Disable() *Field[T] { fd.disabled = true; fd.ignored = true; return fd }

// ReadOnly renders the input read-only.
func (fd *Field[T]) ReadOnly() *Field[T] { fd.readOnly = true; return fd }

// Options supplies choices for Select/Radio.
func (fd *Field[T]) Options(o Options) *Field[T] { fd.options = o; return fd }

// OptionsFunc supplies choices lazily per request.
func (fd *Field[T]) OptionsFunc(fn func(c *Context) Options) *Field[T] { fd.optionsFn = fn; return fd }

// OnlyOnCreate shows the field only on the create form.
func (fd *Field[T]) OnlyOnCreate() *Field[T] { fd.onlyCreate = true; return fd }

// OnlyOnUpdate shows the field only on the edit form.
func (fd *Field[T]) OnlyOnUpdate() *Field[T] { fd.onlyUpdate = true; return fd }

// Dir sets the upload subdirectory for File/Image fields.
func (fd *Field[T]) Dir(dir string) *Field[T] { fd.dir = strings.Trim(dir, "/"); return fd }

// MaxSize caps upload size in bytes.
func (fd *Field[T]) MaxSize(n int64) *Field[T] { fd.maxSize = n; return fd }

// hexColor is the form a Color field stores: "#rrggbb", lowercase.
var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Span sets how much of the form's width the field takes, in twelfths. A field
// spans the whole row unless told otherwise, so two Span(6) fields sit side by
// side and three Span(4) fields make a row of three. Values outside 1..12 are
// ignored.
//
// The span applies from the "sm" breakpoint up. Below it every field is full
// width, because two controls side by side on a phone are two controls too
// narrow to use.
func (fd *Field[T]) Span(n int) *Field[T] {
	if n >= 1 && n <= formColumns {
		fd.span = n
	}
	return fd
}

// Symbol sets what a Currency field is prefixed with, overriding
// Config.CurrencySymbol for this field alone.
func (fd *Field[T]) Symbol(s string) *Field[T] { fd.symbol = s; return fd }

// Min sets the lowest value a field accepts: a time.Time or a layout-shaped
// string for the temporal kinds, a number for the numeric ones.
//
// It reaches the control as the browser's own min attribute and is checked again
// on save, because an attribute is a hint to whoever is using the page and no
// obstacle to anyone who is not.
func (fd *Field[T]) Min(v any) *Field[T] { fd.minVal = v; return fd }

// Max sets the highest value a field accepts; see Min.
func (fd *Field[T]) Max(v any) *Field[T] { fd.maxVal = v; return fd }

// MaxFiles bounds how many a Files or Images field accepts (default 10).
func (fd *Field[T]) MaxFiles(n int) *Field[T] { fd.maxFiles = n; return fd }

// Accept sets the upload MIME filter ("image/*").
func (fd *Field[T]) Accept(mimes string) *Field[T] { fd.accept = mimes; return fd }

// SavingValue transforms the raw submitted string before decoding — the
// per-field escape hatch (hashing passwords, normalizing input).
func (fd *Field[T]) SavingValue(fn func(c *Context, raw string) (any, error)) *Field[T] {
	fd.savingValue = fn
	return fd
}

// ValuesFunc supplies the selected values for a MultiSelect on the edit
// form; m is the typed row (assert to *T).
func (fd *Field[T]) ValuesFunc(fn func(c *Context, m any) []string) *Field[T] {
	fd.valuesFn = fn
	return fd
}

// visible reports whether the field renders in the given mode for this
// request. c may be nil where no request is in scope (the boot-time schema
// pass), in which case only the mode gates apply.
func (fd *Field[T]) visible(c *Context, creating bool) bool {
	if fd.onlyCreate && !creating {
		return false
	}
	if fd.onlyUpdate && creating {
		return false
	}
	if fd.showFn != nil && c != nil && !fd.showFn(c) {
		return false
	}
	return true
}

// decode converts the submitted string into the model field's Go type.
func (fd *Field[T]) decode(raw string) (any, error) {
	info := fd.info
	if info == nil {
		return nil, fmt.Errorf("field %s not resolved", fd.path)
	}
	switch fd.kind {
	case FieldSwitch:
		return raw == "on" || raw == "1" || raw == "true", nil
	case FieldColor:
		// The submitted field is a text input, so the format is checked here
		// rather than left to the swatch beside it.
		if raw == "" {
			return "", nil
		}
		v := strings.ToLower(strings.TrimSpace(raw))
		if !hexColor.MatchString(v) {
			return nil, fmt.Errorf("must be a colour like #3366ff")
		}
		return v, nil
	case FieldNumber:
		if raw == "" {
			return zeroFor(info), nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a whole number")
		}
		return n, nil
	case FieldDecimal, FieldCurrency:
		if raw == "" {
			return zeroFor(info), nil
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return n, nil
	case FieldDate:
		return parseTimeInput(raw, "2006-01-02", info)
	case FieldDatetime:
		return parseTimeInput(raw, "2006-01-02T15:04", info)
	case FieldTime:
		// A time-of-day column is usually a string, but it may be a time.Time.
		// Returning the raw string for both left the second kind rendering
		// correctly and failing on save with "cannot assign string to
		// time.Time" — read worked, write did not.
		if info != nil && info.GoType == reflect.TypeOf(time.Time{}) {
			return parseTimeInput(raw, "15:04", info)
		}
		return raw, nil
	case FieldRichtext:
		// Sanitize on the way in, so a stored value is always safe to render
		// and no read path has to remember to clean it.
		return htmlsafe.Sanitize(raw), nil
	case FieldBelongsTo:
		if raw == "" {
			return zeroFor(info), nil
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return raw, nil // non-numeric foreign keys pass through
		}
		return n, nil
	case FieldSelect, FieldRadio:
		// Option values arrive as strings, but the column they target is often
		// numeric — a status enum stored as a small int is the common case. The
		// target's own kind decides, so Options{"0": "Draft"} writes 0 to an
		// int column and "0" to a string one.
		return coerceToField(raw, info)
	default:
		return raw, nil
	}
}

// coerceToField converts a submitted string to the model field's numeric or
// boolean type, leaving it a string when the target is textual.
func coerceToField(raw string, info *fieldInfo) (any, error) {
	if raw == "" {
		return zeroFor(info), nil
	}
	switch info.Kind {
	case kindInt:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a whole number")
		}
		return n, nil
	case kindUint:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a whole number")
		}
		return n, nil
	case kindFloat:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return n, nil
	case kindBool:
		return raw == "1" || raw == "true" || raw == "on", nil
	default:
		return raw, nil
	}
}

func zeroFor(info *fieldInfo) any {
	if info.Nullable || info.GoType.Kind() == reflect.Pointer {
		return nil
	}
	switch info.Kind {
	case kindInt, kindUint:
		return int64(0)
	case kindFloat:
		return float64(0)
	case kindBool:
		return false
	default:
		return ""
	}
}

func parseTimeInput(raw, layout string, info *fieldInfo) (any, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(layout, raw, time.Local)
	if err != nil {
		// Datetime inputs may omit seconds or include them.
		if t2, err2 := time.ParseInLocation(layout+":05", raw, time.Local); err2 == nil {
			return t2, nil
		}
		return nil, fmt.Errorf("invalid date/time")
	}
	_ = info
	return t, nil
}

// boundString renders a Min or Max for the browser, in the layout the field's
// own control expects.
func (fd *Field[T]) boundString(v any) string {
	if v == nil {
		return ""
	}
	if t, ok := v.(time.Time); ok {
		switch fd.kind {
		case FieldDate:
			return t.Format("2006-01-02")
		case FieldTime:
			return t.Format("15:04:05")
		default:
			return t.Format("2006-01-02T15:04:05")
		}
	}
	return fmt.Sprint(v)
}

// outOfRange reports why a decoded value falls outside Min or Max, or "".
//
// The comparison runs on the decoded value rather than the submitted text, so a
// date is compared as a date and a number as a number.
func (fd *Field[T]) outOfRange(val any) string {
	if fd.minVal == nil && fd.maxVal == nil {
		return ""
	}
	if t, ok := asTime(val); ok {
		if lo, ok := asTime(fd.minVal); ok && t.Before(lo) {
			return fd.label + " cannot be earlier than " + fd.boundString(fd.minVal) + "."
		}
		if hi, ok := asTime(fd.maxVal); ok && t.After(hi) {
			return fd.label + " cannot be later than " + fd.boundString(fd.maxVal) + "."
		}
		return ""
	}
	n, ok := asNumber(val)
	if !ok {
		return ""
	}
	if lo, ok := asNumber(fd.minVal); ok && n < lo {
		return fmt.Sprintf("%s must be at least %v.", fd.label, fd.minVal)
	}
	if hi, ok := asNumber(fd.maxVal); ok && n > hi {
		return fmt.Sprintf("%s may not be greater than %v.", fd.label, fd.maxVal)
	}
	return ""
}

func asTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case *time.Time:
		if x == nil {
			return time.Time{}, false
		}
		return *x, true
	}
	return time.Time{}, false
}

func asNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}
