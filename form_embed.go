package steward

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/imfiqhan/steward/internal/rules"
)

// Embed edits a struct stored in one column of T, as a group of fields inside
// the form. The column holds the struct as JSON:
//
//	type Address struct {
//	    Street string
//	    City   string
//	}
//	type Venue struct {
//	    ID      uint
//	    Address Address `gorm:"serializer:json;type:text"`
//	}
//
//	steward.Embed(f, "Address", func(ef *steward.Form[Address]) {
//	    ef.Text("Street").Rules("required").Span(8)
//	    ef.Text("City").Span(4)
//	})
//
// The field may be E or *E. A struct field the embedded form does not declare
// keeps its stored value. Label on the result names the group.
func Embed[T any, E any](f *Form[T], path string, fn func(*Form[E])) *Field[T] {
	child := &Form[E]{}
	fn(child)
	fd := f.add(FieldEmbed, path)
	fd.embed = &embedForm[E]{form: child}
	return fd
}

// EmbedMany edits a slice of structs stored in one column of T, as repeatable
// rows — the HasMany control over a JSON array instead of a table:
//
//	type Link struct {
//	    Label string
//	    URL   string
//	}
//	// Links []Link `gorm:"serializer:json;type:text"`
//
//	steward.EmbedMany(f, "Links", func(ef *steward.Form[Link]) {
//	    ef.Text("Label").Span(4)
//	    ef.URL("URL").Span(8)
//	})
//
// Rows are stored in the order the form shows them.
func EmbedMany[T any, E any](f *Form[T], path string, fn func(*Form[E])) *Field[T] {
	child := &Form[E]{}
	fn(child)
	fd := f.add(FieldEmbedMany, path)
	fd.embed = &embedForm[E]{form: child, many: true}
	return fd
}

// embedder is an Embed or EmbedMany behind the parent form's field, which
// cannot name the embedded type.
type embedder interface {
	compile(a *Panel, slug string, fd fieldSpec, info *fieldInfo) []error
	// view renders the group for the stored value, current.
	view(c *Context, name, label string, current any) (*nestedRowVM, *nestedVM)
	// decode reads the submitted group. present is false when the form
	// carried nothing for it, which leaves the column alone.
	decode(c *Context, name string, current any) (val any, present bool, errs map[string][]string)
}

type embedForm[E any] struct {
	form *Form[E]
	ft   *fieldTable
	many bool
	// ptr marks a single embed stored through *E.
	ptr bool
}

// embedKinds are the field kinds an embedded form renders. Uploads and
// relations need endpoints addressed by a model the embed does not have.
var embedKinds = map[FieldKind]bool{
	FieldText: true, FieldTextarea: true, FieldEmail: true, FieldURL: true,
	FieldNumber: true, FieldDecimal: true, FieldCurrency: true, FieldSelect: true,
	FieldRadio: true, FieldSwitch: true, FieldColor: true, FieldDate: true,
	FieldDatetime: true, FieldTime: true, FieldHidden: true,
}

func (e *embedForm[E]) compile(a *Panel, slug string, fd fieldSpec, info *fieldInfo) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: embed %q: "+format, append([]any{slug, fd.path}, args...)...))
	}
	elem := reflect.TypeFor[E]()
	want := "a " + elem.String()
	switch {
	case e.many:
		want = "a []" + elem.String()
		if info.GoType != reflect.SliceOf(elem) {
			fail("the field is %s, not %s", info.GoType, want)
			return errs
		}
	case info.GoType == elem:
	case info.GoType == reflect.PointerTo(elem):
		e.ptr = true
	default:
		fail("the field is %s, not %s or *%s", info.GoType, want, elem)
		return errs
	}
	if elem.Kind() != reflect.Struct {
		fail("%s is not a struct", elem)
		return errs
	}
	if info.serializer == "" {
		fail("the column needs `gorm:\"serializer:json\"` to be stored as JSON")
	}
	if len(e.form.tabs) > 0 || len(e.form.nested) > 0 {
		fail("an embedded form cannot hold tabs or HasMany")
	}

	ft, err := newEmbedTable(elem, a.db.NamingStrategy)
	if err != nil {
		fail("%v", err)
		return errs
	}
	e.ft = ft
	for _, cf := range e.form.fields {
		if cf.divider {
			continue
		}
		if !embedKinds[cf.kind] {
			fail("field %q is a %s field, which an embedded form cannot hold", cf.path, kindNames[cf.kind])
			cf.ignored = true
			continue
		}
		if cf.showFn != nil || cf.savingValue != nil || cf.valuesFn != nil ||
			cf.onlyCreate || cf.onlyUpdate || cf.createRules != "" || cf.updateRules != "" {
			fail("field %q: Show, SavingValue, ValuesFunc, OnlyOn* and Creation/UpdateRules "+
				"have no effect inside an embedded form", cf.path)
		}
		ci, err := ft.lookup(cf.path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: embed %q: %w", slug, fd.path, withSite(err, cf.declaredAt)))
			cf.ignored = true
			continue
		}
		cf.info = ci
		if cf.label == "" {
			cf.label = ci.Label
		}
	}
	return errs
}

// fieldName is the input name of one embedded field: "Address[City]", or
// "Links[2][URL]" for a row of EmbedMany.
func embedFieldName(name, key, path string) string {
	if key == "" {
		return name + "[" + path + "]"
	}
	return name + "[" + key + "][" + path + "]"
}

func (e *embedForm[E]) rowVM(c *Context, name, key string, row *E) nestedRowVM {
	rv := nestedRowVM{Key: key}
	for _, cf := range e.form.fields {
		if cf.divider {
			rv.appendField(formFieldVM{Divider: true}, cf.fieldset)
			continue
		}
		if cf.ignored || cf.info == nil {
			continue
		}
		fv := formFieldVM{
			Kind:        kindNames[cf.kind],
			Name:        embedFieldName(name, key, cf.path),
			Label:       cf.label,
			Required:    cf.required,
			Disabled:    cf.disabled,
			ReadOnly:    cf.readOnly,
			Placeholder: cf.placeholder,
			Help:        cf.help,
			Span:        cf.span,
			MinAttr:     cf.boundString(cf.minVal),
			MaxAttr:     cf.boundString(cf.maxVal),
		}
		if row != nil {
			fv.Value = cf.valueString(row)
		} else if cf.defaultVal != nil {
			fv.Value = fmt.Sprint(cf.defaultVal)
		}
		switch cf.kind {
		case FieldSelect, FieldRadio:
			for val, label := range cf.choices(c) {
				fv.Options = append(fv.Options, optionVM{Value: val, Label: label, Selected: val == fv.Value})
			}
			sortOptions(fv.Options)
		case FieldCurrency:
			fv.Symbol = cf.symbol
			if fv.Symbol == "" {
				fv.Symbol = c.Panel.cfg.CurrencySymbol
			}
		}
		rv.appendField(fv, cf.fieldset)
	}
	return rv
}

func (e *embedForm[E]) view(c *Context, name, label string, current any) (*nestedRowVM, *nestedVM) {
	if !e.many {
		var row *E
		switch v := current.(type) {
		case E:
			row = &v
		case *E:
			row = v
		}
		rv := e.rowVM(c, name, "", row)
		rv.Label = label
		return &rv, nil
	}
	vm := &nestedVM{Name: name, Label: label, Template: e.rowVM(c, name, "__KEY__", nil), Present: true}
	rows, _ := current.([]E)
	for i := range rows {
		vm.Rows = append(vm.Rows, e.rowVM(c, name, strconv.Itoa(i), &rows[i]))
	}
	return nil, vm
}

// decodeRow fills row from the inputs of one embedded group.
func (e *embedForm[E]) decodeRow(c *Context, name, key string, row *E, errs map[string][]string) {
	for _, cf := range e.form.fields {
		if cf.divider || cf.ignored || cf.info == nil || cf.disabled || cf.kind == FieldDisplay {
			continue
		}
		full := embedFieldName(name, key, cf.path)
		raw, present := formValue(c.R, full)
		if cf.kind == FieldSwitch {
			if !present && !switchPresent(c.R, full) {
				continue
			}
		} else if !present {
			if cf.required {
				errs[full] = append(errs[full], cf.label+" is required.")
			}
			continue
		}
		if cf.rules != "" {
			target := rules.Field{DB: c.Panel.db, Ctx: c.Ctx(), Label: cf.label}
			if msgs := rules.Validate(target, cf.rules, raw); len(msgs) > 0 {
				errs[full] = append(errs[full], msgs...)
				continue
			}
		}
		val, err := cf.decode(raw)
		if err != nil {
			errs[full] = append(errs[full], cf.label+": "+err.Error())
			continue
		}
		if msg := cf.outOfRange(val); msg != "" {
			errs[full] = append(errs[full], msg)
			continue
		}
		if err := setField(row, cf.info, val); err != nil {
			errs[full] = append(errs[full], cf.label+": "+err.Error())
		}
	}
}

var embedRowRe = regexp.MustCompile(`^\[([A-Za-z0-9_]+)\]\[([A-Za-z0-9_]+)\]$`)

func (e *embedForm[E]) decode(c *Context, name string, current any) (any, bool, map[string][]string) {
	errs := map[string][]string{}
	if !e.many {
		present := false
		for k := range c.R.Form {
			if strings.HasPrefix(k, name+"[") || strings.HasPrefix(k, "_present_"+name+"[") {
				present = true
				break
			}
		}
		if !present {
			return nil, false, nil
		}
		// Starting from the stored value keeps what the form does not declare.
		var row E
		switch v := current.(type) {
		case E:
			row = v
		case *E:
			if v != nil {
				row = *v
			}
		}
		e.decodeRow(c, name, "", &row, errs)
		if len(errs) > 0 {
			return nil, true, errs
		}
		return row, true, nil
	}

	keys := map[string]bool{}
	removed := map[string]bool{}
	for k, vals := range c.R.Form {
		m := embedRowRe.FindStringSubmatch(strings.TrimPrefix(k, name))
		if m == nil || !strings.HasPrefix(k, name+"[") {
			continue
		}
		keys[m[1]] = true
		if m[2] == "_remove" && len(vals) > 0 && vals[0] == "1" {
			removed[m[1]] = true
		}
	}
	if len(keys) == 0 && !switchPresent(c.R, name) {
		return nil, false, nil
	}
	stored, _ := current.([]E)
	out := make([]E, 0, len(keys))
	for _, key := range orderedRowKeys(keys) {
		if removed[key] {
			continue
		}
		// An existing row starts from its stored value, for the same reason a
		// single embed does.
		var row E
		if i, err := strconv.Atoi(key); err == nil && i >= 0 && i < len(stored) {
			row = stored[i]
		}
		e.decodeRow(c, name, key, &row, errs)
		out = append(out, row)
	}
	if len(errs) > 0 {
		return nil, true, errs
	}
	return out, true, nil
}

// orderedRowKeys orders a repeater's submitted keys the way the form showed
// them: stored rows by index, then added rows in the order they were added,
// which the browser numbers as new_<n>_<random>.
func orderedRowKeys(keys map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	rank := func(k string) (int, int) {
		if n, err := strconv.Atoi(k); err == nil {
			return 0, n
		}
		if rest, ok := strings.CutPrefix(k, "new_"); ok {
			num, _, _ := strings.Cut(rest, "_")
			if n, err := strconv.Atoi(num); err == nil {
				return 1, n
			}
		}
		return 2, 0
	}
	sort.SliceStable(out, func(i, j int) bool {
		gi, ni := rank(out[i])
		gj, nj := rank(out[j])
		if gi != gj {
			return gi < gj
		}
		if ni != nj {
			return ni < nj
		}
		return out[i] < out[j]
	})
	return out
}
