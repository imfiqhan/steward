package steward

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/imfiqhan/steward/internal/rules"
)

// HasMany embeds a repeatable child form inside T's form — dcat-admin's
// hasMany. relation names T's []C slice field (used to verify the
// association); fkPath names C's foreign-key field, set to the parent's key
// on save. Child rows follow dcat's protocol: existing rows are keyed by
// their id, new rows by "new_*", and rows flagged "_remove" are deleted.
//
//	steward.HasMany(f, "Items", "OrderID", func(cf *steward.Form[Item]) {
//	    cf.Text("Name").Rules("required")
//	    cf.Number("Qty")
//	})
//
// v1 limits child fields to non-upload, non-relation kinds (no File/Image/
// BelongsTo inside rows); Verify reports violations at boot.
func HasMany[T any, C any](f *Form[T], relation string, fkPath string, fn func(*Form[C])) {
	child := &Form[C]{}
	fn(child)
	f.nested = append(f.nested, &hasManyForm[T, C]{
		relation:  relation,
		fkPath:    fkPath,
		childForm: child,
	})
}

// nestedForm is the type-erased handle Form[T] stores; C lives only inside
// hasManyForm.
type nestedForm[T any] interface {
	fieldName() string
	compile(a *Admin, parent *typedResource[T]) error
	buildVM(c *Context, parent *T) (nestedVM, error)
	validate(c *Context) (payload any, errs map[string][]string)
	persist(c *Context, parent *T, payload any) error
}

type nestedVM struct {
	Name     string
	Label    string
	Rows     []nestedRowVM
	Template nestedRowVM // key = __KEY__, cloned client-side
}

type nestedRowVM struct {
	Key    string
	Fields []formFieldVM
}

// Interface assertion: staticcheck cannot see generic interface
// satisfaction through HasMany's construction.
var _ nestedForm[struct{}] = (*hasManyForm[struct{}, struct{}])(nil)

type hasManyForm[T, C any] struct {
	relation  string
	fkPath    string
	label     string
	childForm *Form[C]
	childFT   *fieldTable
	parentPK  *fieldInfo
	fkInfo    *fieldInfo
	repo      *GormRepository[C]
	db        bool // compiled successfully
}

func (h *hasManyForm[T, C]) fieldName() string { return h.relation }

func (h *hasManyForm[T, C]) compile(a *Admin, parent *typedResource[T]) error {
	h.label = splitCamel(h.relation)
	h.parentPK = parent.ft.pk

	// The relation must exist on T as a has-many.
	rel, ok := parent.ft.model.Relationships.Relations[h.relation]
	if !ok || rel.FieldSchema == nil {
		a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
			"resource %q: HasMany relation %q not found on %s", parent.res.m.slug, h.relation, parent.ft.model.Name))
		return nil
	}

	var zero C
	ft, err := newFieldTable(reflect.TypeOf(zero), a.db.NamingStrategy)
	if err != nil {
		return err
	}
	h.childFT = ft

	fk, err := ft.lookup(h.fkPath)
	if err != nil {
		a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
			"resource %q: HasMany %q foreign key: %w", parent.res.m.slug, h.relation, err))
		return nil
	}
	h.fkInfo = fk

	for _, fd := range h.childForm.fields {
		if fd.divider || fd.virtual {
			continue
		}
		switch fd.kind {
		case FieldFile, FieldImage, FieldBelongsTo:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: HasMany %q field %q: %s fields are not supported inside nested rows yet",
				parent.res.m.slug, h.relation, fd.path, kindNames[fd.kind]))
			fd.ignored = true
			continue
		}
		info, err := ft.lookup(fd.path)
		if err != nil {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: HasMany %q field: %w", parent.res.m.slug, h.relation, err))
			continue
		}
		fd.info = info
		if fd.label == "" {
			fd.label = info.Label
		}
	}

	repo, err := NewGormRepository[C](a.db)
	if err != nil {
		return err
	}
	h.repo = repo
	h.db = true
	return nil
}

// childFieldVM renders one child field with a bracketed input name.
func (h *hasManyForm[T, C]) childFieldVM(c *Context, fd *Field[C], row *C, key string) formFieldVM {
	fv := formFieldVM{
		Kind:        kindNames[fd.kind],
		Name:        fmt.Sprintf("%s[%s][%s]", h.relation, key, fd.path),
		Label:       fd.label,
		Required:    fd.required,
		Placeholder: fd.placeholder,
		Help:        fd.help,
	}
	if row != nil {
		fv.Value = fd.valueString(row)
	} else if fd.defaultVal != nil {
		fv.Value = fmt.Sprint(fd.defaultVal)
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
	}
	return fv
}

func (h *hasManyForm[T, C]) rowVM(c *Context, row *C, key string) nestedRowVM {
	rv := nestedRowVM{Key: key}
	for _, fd := range h.childForm.fields {
		if fd.divider || fd.ignored || fd.info == nil {
			continue
		}
		rv.Fields = append(rv.Fields, h.childFieldVM(c, fd, row, key))
	}
	return rv
}

func (h *hasManyForm[T, C]) buildVM(c *Context, parent *T) (nestedVM, error) {
	vm := nestedVM{Name: h.relation, Label: h.label, Template: h.rowVM(c, nil, "__KEY__")}
	if !h.db || parent == nil {
		return vm, nil
	}
	pk, ok := h.parentPK.value(reflect.ValueOf(parent))
	if !ok {
		return vm, nil
	}
	children, _, err := h.repo.List(c.Ctx(), &ListQuery{
		Conds: []Cond{{Path: h.fkPath, Op: OpEq, Val: fmt.Sprint(pk)}},
		Sorts: []Sort{{Path: h.childFT.pk.Path}},
		Page:  1, PerPage: 500,
	})
	if err != nil {
		return vm, err
	}
	for i := range children {
		key := fmt.Sprint(mustValue(h.childFT.pk, &children[i]))
		vm.Rows = append(vm.Rows, h.rowVM(c, &children[i], key))
	}
	return vm, nil
}

func mustValue[C any](info *fieldInfo, row *C) any {
	v, _ := info.value(reflect.ValueOf(row))
	return v
}

var nestedKeyRe = regexp.MustCompile(`^([A-Za-z0-9_]+)\[([A-Za-z0-9_]+)\]\[([A-Za-z0-9_.]+)\]$`)

// pendingChild is one submitted row, decoded and validated.
type pendingChild[C any] struct {
	key    string
	remove bool
	values map[string]any // path → decoded value
	raw    map[string]string
}

// validate parses and validates the submitted child rows without writing.
func (h *hasManyForm[T, C]) validate(c *Context) (any, map[string][]string) {
	if !h.db {
		return nil, nil
	}
	rows := map[string]*pendingChild[C]{}
	var order []string
	get := func(key string) *pendingChild[C] {
		if p, ok := rows[key]; ok {
			return p
		}
		p := &pendingChild[C]{key: key, values: map[string]any{}, raw: map[string]string{}}
		rows[key] = p
		order = append(order, key)
		return p
	}

	for name, vals := range c.R.Form {
		m := nestedKeyRe.FindStringSubmatch(name)
		if m == nil || m[1] != h.relation || len(vals) == 0 {
			continue
		}
		key, path := m[2], m[3]
		if path == "_remove" {
			if vals[0] == "1" {
				get(key).remove = true
			}
			continue
		}
		get(key).raw[path] = vals[0]
	}

	errs := map[string][]string{}
	for _, key := range order {
		p := rows[key]
		if p.remove {
			continue
		}
		for _, fd := range h.childForm.fields {
			if fd.divider || fd.ignored || fd.info == nil {
				continue
			}
			raw, present := p.raw[fd.path]
			if !present {
				continue
			}
			field := fmt.Sprintf("%s[%s][%s]", h.relation, key, fd.path)
			if fd.rules != "" {
				target := rules.Field{DB: c.Admin.db, Ctx: c.Ctx(), Label: fd.label}
				if msgs := rules.Validate(target, fd.rules, raw); len(msgs) > 0 {
					errs[field] = append(errs[field], msgs...)
					continue
				}
			}
			val, err := fd.decode(raw)
			if err != nil {
				errs[field] = append(errs[field], fd.label+": "+err.Error())
				continue
			}
			p.values[fd.path] = val
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	// Preserve submission order.
	ordered := make([]*pendingChild[C], 0, len(order))
	for _, key := range order {
		ordered = append(ordered, rows[key])
	}
	return ordered, nil
}

// persist applies the validated rows after the parent is saved.
func (h *hasManyForm[T, C]) persist(c *Context, parent *T, payload any) error {
	pendings, _ := payload.([]*pendingChild[C])
	if len(pendings) == 0 || !h.db {
		return nil
	}
	pkVal, ok := h.parentPK.value(reflect.ValueOf(parent))
	if !ok {
		return fmt.Errorf("HasMany %s: parent key unavailable", h.relation)
	}
	parentKey := fmt.Sprint(pkVal)

	for _, p := range pendings {
		isNew := strings.HasPrefix(p.key, "new_")
		switch {
		case isNew && p.remove:
			continue
		case isNew:
			var child C
			for path, val := range p.values {
				if info, ok := h.childFT.byPath[path]; ok {
					if err := setField(&child, info, val); err != nil {
						return err
					}
				}
			}
			if err := setField(&child, h.fkInfo, pkVal); err != nil {
				return err
			}
			if err := h.repo.Create(c.Ctx(), &child); err != nil {
				return err
			}
		default:
			child, err := h.repo.Find(c.Ctx(), p.key)
			if err != nil {
				continue // stale row; skip rather than fail the whole save
			}
			// Ownership check: never touch children of other parents.
			if fkv, ok := h.fkInfo.value(reflect.ValueOf(child)); !ok || fmt.Sprint(fkv) != parentKey {
				continue
			}
			if p.remove {
				if err := h.repo.Delete(c.Ctx(), []string{p.key}); err != nil {
					return err
				}
				continue
			}
			var dirty []string
			for path, val := range p.values {
				if info, ok := h.childFT.byPath[path]; ok {
					if err := setField(child, info, val); err != nil {
						return err
					}
					dirty = append(dirty, path)
				}
			}
			if len(dirty) == 0 {
				continue
			}
			if err := h.repo.Update(c.Ctx(), child, dirty); err != nil {
				return err
			}
		}
	}
	return nil
}
