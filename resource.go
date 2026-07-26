package steward

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/jinzhu/inflection"
)

// resourceMeta is the type-erased identity every resource carries.
type resourceMeta struct {
	slug     string
	title    string
	icon     string
	group    string
	typeName string
}

// Resource is the public, generic handle returned by Register. All
// configuration happens through it before Build; the internal registry only
// ever sees the type-erased resourceEntry.
type Resource[T any] struct {
	a *Admin
	m *resourceMeta

	gridFn func(*Grid[T])
	repo   Repository[T]
}

// Register adds the model T to the panel. With no further configuration the
// resource gets a slug and title derived from the type name; Grid, Form, and
// Detail builders arrive with later milestones and hang off this handle.
func Register[T any](a *Admin) *Resource[T] {
	if a.built {
		panic("steward: Register called after Build — register all resources first")
	}
	var zero T
	t := reflect.TypeOf(zero)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("steward: Register requires a struct type, got %T", zero))
	}
	name := t.Name()
	m := &resourceMeta{
		slug:     inflection.Plural(toSnake(name)),
		title:    inflection.Plural(splitCamel(name)),
		icon:     "file",
		typeName: name,
	}
	res := &Resource[T]{a: a, m: m}
	entry := &typedResource[T]{res: res}
	a.registry = append(a.registry, entry)
	a.bySlug[m.slug] = entry
	return res
}

// Slug overrides the URL segment (default: snake-cased plural of the type).
func (r *Resource[T]) Slug(s string) *Resource[T] {
	delete(r.a.bySlug, r.m.slug)
	r.m.slug = strings.Trim(s, "/")
	r.a.bySlug[r.m.slug] = r.a.registry[r.indexInRegistry()]
	return r
}

func (r *Resource[T]) indexInRegistry() int {
	for i, e := range r.a.registry {
		if tr, ok := e.(*typedResource[T]); ok && tr.res == r {
			return i
		}
	}
	panic("steward: resource not in registry")
}

// Title overrides the human name shown in menu and headings.
func (r *Resource[T]) Title(s string) *Resource[T] { r.m.title = s; return r }

// Icon sets the sidebar icon (a Tabler icon name, e.g. "news").
func (r *Resource[T]) Icon(name string) *Resource[T] { r.m.icon = name; return r }

// Group places the resource under a collapsible sidebar group.
func (r *Resource[T]) Group(name string) *Resource[T] { r.m.group = name; return r }

// Grid declares the list view; fn runs at Build time against a fresh
// builder. Without it the grid shows every direct model field.
func (r *Resource[T]) Grid(fn func(*Grid[T])) *Resource[T] { r.gridFn = fn; return r }

// Repository swaps the data source (default: GORM repository over Config.DB).
func (r *Resource[T]) Repository(repo Repository[T]) *Resource[T] { r.repo = repo; return r }

// typedResource is the erased registry entry; the any→T boundary lives here
// and nowhere else.
type typedResource[T any] struct {
	res  *Resource[T]
	ft   *fieldTable
	grid *Grid[T]
	repo Repository[T]
}

func (t *typedResource[T]) meta() *resourceMeta { return t.res.m }

// compile parses the model into the field table, applies the user's builder
// functions, resolves every string field reference (collecting bad ones into
// Verify errors), and finalizes the repository.
func (t *typedResource[T]) compile(a *Admin) error {
	var zero T
	typ := reflect.TypeOf(zero)
	ft, err := newFieldTable(typ, a.db.NamingStrategy)
	if err != nil {
		return err
	}
	t.ft = ft

	t.repo = t.res.repo
	if t.repo == nil {
		gr, err := NewGormRepository[T](a.db)
		if err != nil {
			return err
		}
		t.repo = gr
	}

	g := newGrid(t.res)
	if t.res.gridFn != nil {
		t.res.gridFn(g)
	} else {
		t.defaultColumns(g)
	}
	t.grid = g

	verify := func(context, path string) *fieldInfo {
		info, err := ft.lookup(path)
		if err != nil {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf("resource %q: %s: %w", t.res.m.slug, context, err))
			return nil
		}
		return info
	}

	var preloads []string
	for _, col := range g.columns {
		if col.computed {
			if col.label == "" {
				col.label = col.path
			}
			continue
		}
		col.info = verify("grid column", col.path)
		if col.info != nil {
			if col.label == "" {
				col.label = col.info.Label
			}
			if col.info.Relation != "" && !slices.Contains(preloads, col.info.Relation) {
				preloads = append(preloads, col.info.Relation)
			}
			// Long text gets a default truncation when nothing custom is set.
			if col.info.Kind == kindString && col.present == nil && len(col.transform) == 0 {
				col.transform = append(col.transform, func(v any, _ *T) any {
					s := fmt.Sprint(v)
					if len([]rune(s)) > 80 {
						return string([]rune(s)[:80]) + "…"
					}
					return v
				})
			}
		} else if col.label == "" {
			col.label = col.path
		}
	}
	if gr, ok := t.repo.(*GormRepository[T]); ok && len(preloads) > 0 {
		gr.With(preloads...)
	}
	for _, fi := range g.filters {
		fi.info = verify("grid filter", fi.path)
		if fi.info != nil && fi.label == "" {
			fi.label = fi.info.Label
		}
	}
	for _, p := range g.quickSearch {
		verify("quick search", p)
	}
	if g.defaultSort != nil {
		verify("default sort", g.defaultSort.Path)
	}
	return nil
}

// defaultColumns projects the zero-config grid: every direct field in struct
// order, primary key sortable, byte/blob fields skipped.
func (t *typedResource[T]) defaultColumns(g *Grid[T]) {
	for _, f := range t.ft.model.Fields {
		info, ok := t.ft.byPath[f.Name]
		if !ok || info.Kind == kindBytes || info.Kind == kindOther {
			continue
		}
		col := g.Column(f.Name)
		if info.Primary || info.Kind == kindTime {
			col.Sortable()
		}
	}
}

func (t *typedResource[T]) registerRoutes(a *Admin, mux *http.ServeMux) {
	m := t.res.m
	mux.HandleFunc("GET "+a.url(m.slug), a.h(t.index))
	mux.HandleFunc("DELETE "+a.url(m.slug)+"/{id}", a.h(t.destroy))
}

// toSnake converts CamelCase to snake_case ("BlogPost" → "blog_post").
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// splitCamel renders CamelCase as words, keeping acronyms intact:
// "BlogPost" → "Blog Post", "ID" → "ID", "AuthorID" → "Author ID",
// "HTTPServer" → "HTTP Server".
func splitCamel(s string) string {
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prevLower := unicode.IsLower(runes[i-1])
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if prevLower || (unicode.IsUpper(runes[i-1]) && nextLower) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}
