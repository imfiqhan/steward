package steward

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
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
}

// Form configures a resource's create/edit view; write-only until Build.
// Hooks receive the typed model, never a map.
type Form[T any] struct {
	res    *Resource[T]
	fields []*Field[T]

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
func (f *Form[T]) Text(path string, label ...string) *Field[T] { return f.add(FieldText, path, label...) }

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
func (f *Form[T]) Date(path string, label ...string) *Field[T] { return f.add(FieldDate, path, label...) }

// Datetime adds a date+time picker.
func (f *Form[T]) Datetime(path string, label ...string) *Field[T] {
	return f.add(FieldDatetime, path, label...)
}

// Time adds a time-of-day input stored as "15:04".
func (f *Form[T]) Time(path string, label ...string) *Field[T] { return f.add(FieldTime, path, label...) }

// File adds an upload field storing the file path.
func (f *Form[T]) File(path string, label ...string) *Field[T] { return f.add(FieldFile, path, label...) }

// Image adds an image upload with preview.
func (f *Form[T]) Image(path string, label ...string) *Field[T] {
	fd := f.add(FieldImage, path, label...)
	fd.accept = "image/*"
	return fd
}

// Markdown adds a markdown editor (textarea with preview styling).
func (f *Form[T]) Markdown(path string, label ...string) *Field[T] {
	return f.add(FieldMarkdown, path, label...)
}

// BelongsTo binds a foreign-key field to a searchable select over the
// relation: BelongsTo("AuthorID", "Author", "Name").
func (f *Form[T]) BelongsTo(fkPath, relation, titleField string, label ...string) *Field[T] {
	fd := f.add(FieldBelongsTo, fkPath, label...)
	fd.relName = relation
	fd.relTitle = titleField
	return fd
}

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
func (f *Form[T]) Saved(fn func(c *Context, m *T, created bool) error) *Form[T] { f.savedFn = fn; return f }

// Deleting runs before rows are deleted.
func (f *Form[T]) Deleting(fn func(c *Context, ids []string) error) *Form[T] { f.deletingFn = fn; return f }

// Deleted runs after rows are deleted.
func (f *Form[T]) Deleted(fn func(c *Context, ids []string) error) *Form[T] { f.deletedFn = fn; return f }

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
	dir     string
	maxSize int64
	accept  string

	// belongsTo
	relName  string
	relTitle string

	savingValue func(c *Context, raw string) (any, error)

	info *fieldInfo
}

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

// Accept sets the upload MIME filter ("image/*").
func (fd *Field[T]) Accept(mimes string) *Field[T] { fd.accept = mimes; return fd }

// SavingValue transforms the raw submitted string before decoding — the
// per-field escape hatch (hashing passwords, normalizing input).
func (fd *Field[T]) SavingValue(fn func(c *Context, raw string) (any, error)) *Field[T] {
	fd.savingValue = fn
	return fd
}

// visible reports whether the field renders in the given mode.
func (fd *Field[T]) visible(creating bool) bool {
	if fd.onlyCreate && !creating {
		return false
	}
	if fd.onlyUpdate && creating {
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
		return raw, nil
	case FieldBelongsTo:
		if raw == "" {
			return zeroFor(info), nil
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return raw, nil // non-numeric foreign keys pass through
		}
		return n, nil
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
