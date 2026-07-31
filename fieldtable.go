package steward

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/schema"
)

// fieldKind is the coarse value classification renderers and the scaffolder
// key defaults off.
type fieldKind int

const (
	kindString fieldKind = iota
	kindText
	kindInt
	kindUint
	kindFloat
	kindBool
	kindTime
	kindBytes
	kindOther
)

// fieldInfo describes one addressable field path of a resource model.
type fieldInfo struct {
	Path     string // "Title" or "Author.Name"
	DBName   string // column name on the model's own table ("" for relation paths)
	Label    string
	Kind     fieldKind
	GoType   reflect.Type
	Nullable bool
	Primary  bool
	// Relation is the first path segment's relationship name for nested
	// paths ("Author" for "Author.Name"); preloaded automatically.
	Relation string
	// rel carries the SQL topology for a relation path, so filters and quick
	// search can constrain on it. Nil when the relation's shape cannot be
	// expressed as a single-column subquery (a composite key), which callers
	// report at boot rather than mis-querying.
	rel   *relTarget
	index []int // reflect index path to read the value
}

// filterable reports whether a path can appear in a WHERE clause: a direct
// column, or a relation path whose topology resolved.
func (info *fieldInfo) filterable() bool {
	return info.DBName != "" || info.rel != nil
}

// fieldTable is the single source of truth every builder projects from:
// parsed once per resource at compile, it validates string field references
// and supplies labels, kinds, and column names.
type fieldTable struct {
	model  *schema.Schema
	byPath map[string]*fieldInfo
	pk     *fieldInfo
}

var schemaCache = &sync.Map{}

func newFieldTable(t reflect.Type, naming schema.Namer) (*fieldTable, error) {
	if naming == nil {
		naming = schema.NamingStrategy{}
	}
	model, err := schema.Parse(reflect.New(t).Interface(), schemaCache, naming)
	if err != nil {
		return nil, fmt.Errorf("parsing model %s: %w", t.Name(), err)
	}
	ft := &fieldTable{model: model, byPath: map[string]*fieldInfo{}}

	for _, f := range model.Fields {
		if f.DBName == "" {
			continue // unexported or ignored
		}
		info := &fieldInfo{
			Path:     f.Name,
			DBName:   f.DBName,
			Label:    splitCamel(f.Name),
			Kind:     classify(f.FieldType),
			GoType:   f.FieldType,
			Nullable: !f.NotNull,
			Primary:  f.PrimaryKey,
			index:    f.StructField.Index,
		}
		ft.byPath[f.Name] = info
		if f.PrimaryKey && ft.pk == nil {
			ft.pk = info
		}
	}

	// One relation hop: "Author.Name" style paths for display columns.
	for _, rel := range model.Relationships.Relations {
		if rel.Field == nil || rel.FieldSchema == nil {
			continue
		}
		for _, rf := range rel.FieldSchema.Fields {
			if rf.DBName == "" {
				continue
			}
			p := rel.Name + "." + rf.Name
			target, _ := newRelTarget(rel, rf)
			ft.byPath[p] = &fieldInfo{
				Path:     p,
				Label:    splitCamel(rel.Name) + " " + splitCamel(rf.Name),
				Kind:     classify(rf.FieldType),
				GoType:   rf.FieldType,
				Nullable: !rf.NotNull,
				Relation: rel.Name,
				rel:      target,
				index:    append(append([]int{}, rel.Field.StructField.Index...), rf.StructField.Index...),
			}
		}
	}
	if ft.pk == nil {
		return nil, fmt.Errorf("model %s has no primary key", t.Name())
	}
	return ft, nil
}

func classify(t reflect.Type) fieldKind {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[time.Time]() {
		return kindTime
	}
	switch t.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return kindInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return kindUint
	case reflect.Float32, reflect.Float64:
		return kindFloat
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return kindBytes
		}
	}
	return kindOther
}

// lookup resolves a field path, error carrying suggestions for typos.
func (ft *fieldTable) lookup(path string) (*fieldInfo, error) {
	if info, ok := ft.byPath[path]; ok {
		return info, nil
	}
	var known []string
	for p := range ft.byPath {
		if !strings.Contains(p, ".") {
			known = append(known, p)
		}
	}
	return nil, fmt.Errorf("unknown field %q on %s (known fields: %s)", path, ft.model.Name, strings.Join(known, ", "))
}

// value reads the field at path from a model instance, dereferencing
// pointers; (nil, false) when a nil pointer interrupts the path.
func (info *fieldInfo) value(m reflect.Value) (any, bool) {
	v := m
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	for i, idx := range info.index {
		v = v.Field(idx)
		if i < len(info.index)-1 {
			for v.Kind() == reflect.Pointer {
				if v.IsNil() {
					return nil, false
				}
				v = v.Elem()
			}
		}
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	return v.Interface(), true
}
