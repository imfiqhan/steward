package steward

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/schema"

	"github.com/imfiqhan/steward/internal/suggest"
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

// lookup resolves a field path. The error names the nearest field and the set
// to choose from, since a path that does not resolve is almost always a typo
// or a field on the wrong model.
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
	return nil, &unknownFieldError{path: path, model: ft.model.Name, known: known}
}

// unknownFieldError carries the parts of the message rather than the message,
// so a caller that knows where the path was declared can put that on the first
// line — after the message is built, the first line is no longer reachable.
type unknownFieldError struct {
	path  string
	model string
	known []string
	site  string
}

func (e *unknownFieldError) Error() string {
	// The declaration site says where to look more precisely than the model
	// name does, so naming both only spends the first line's budget twice.
	where := " on " + e.model
	if e.site != "" {
		where = at(e.site)
	}
	return fmt.Sprintf("unknown field %q%s%s", e.path, where, suggest.Block(e.path, e.known))
}

// withSite records where a path was declared, when the caller knows. Anything
// that is not an unresolved path passes through untouched.
func withSite(err error, site string) error {
	var u *unknownFieldError
	if site != "" && errors.As(err, &u) {
		u.site = site
	}
	return err
}

// callerSite reports the file and line in the caller's own code, for an error
// that names something they wrote. Frames inside this package are skipped, so
// what comes back is the resource declaration rather than the builder that
// read it.
//
// The path is trimmed to its last two segments: the whole thing is the
// machine's, not the reader's, and the first line of an error has a budget.
// It returns "" when no frame outside the package is found, which is what a
// panel declared by generated code inside it would look like.
func callerSite() string {
	pc := make([]uintptr, 16)
	n := runtime.Callers(2, pc[:])
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pc[:n])
	for {
		f, more := frames.Next()
		if f.File != "" && !strings.HasPrefix(f.Function, selfPackage) {
			return shortPath(f.File) + ":" + strconv.Itoa(f.Line)
		}
		if !more {
			return ""
		}
	}
}

// selfPackage is the prefix runtime gives every function in this package.
// Frames matching it are the framework's own and never what a reader wrote.
const selfPackage = "github.com/imfiqhan/steward."

func shortPath(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		if j := strings.LastIndexByte(p[:i], '/'); j >= 0 {
			return p[j+1:]
		}
	}
	return p
}

// at renders a declaration site for the tail of an error's first line, or ""
// when it is not known.
func at(site string) string {
	if site == "" {
		return ""
	}
	return " (" + site + ")"
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
