package steward

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"

	"gorm.io/gorm"

	"github.com/imfiqhan/steward/internal/htmlsafe"
)

// Detail configures a resource's show view. Without configuration every
// direct field renders with its type default.
type Detail[T any] struct {
	res       *Resource[T]
	fields    []*DetailField[T]
	relations []relationGrid[T]
}

func newDetail[T any](res *Resource[T]) *Detail[T] { return &Detail[T]{res: res} }

// Field adds one field row to the detail panel.
func (d *Detail[T]) Field(path string, label ...string) *DetailField[T] {
	df := &DetailField[T]{path: path}
	if len(label) > 0 {
		df.label = label[0]
	}
	d.fields = append(d.fields, df)
	return df
}

// DetailField is one show-view row; transformer methods chain.
type DetailField[T any] struct {
	path  string
	label string

	present func(v any, m *T) template.HTML
	info    *fieldInfo
}

// As renders the value with a custom function.
func (df *DetailField[T]) As(fn func(v any, m *T) template.HTML) *DetailField[T] {
	df.present = fn
	return df
}

// Badge renders a colored Tabler badge (see Column.Badge).
func (df *DetailField[T]) Badge(colors map[any]string) *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML { return badgeHTML(colors, v) }
	return df
}

// Bool renders Yes/No statuses.
func (df *DetailField[T]) Bool() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML { return statusHTML(truthy(v), "Yes", "No") }
	return df
}

// Image renders the value (URL or storage path) as an image; storage paths
// resolve through the configured Storage at render time.
func (df *DetailField[T]) Image(width, height int) *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(v)
		if s == "" || v == nil {
			return `<span class="text-muted-foreground">—</span>`
		}
		style := ""
		if width > 0 {
			style += fmt.Sprintf("max-width:%dpx;", width)
		}
		if height > 0 {
			style += fmt.Sprintf("max-height:%dpx;", height)
		}
		return template.HTML(fmt.Sprintf(`<img src="%s" class="rounded-md border" style="%s" alt=""/>`,
			template.HTMLEscapeString(s), style))
	}
	return df
}

// Link renders the value as an external link.
func (df *DetailField[T]) Link() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(v)
		if s == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		esc := template.HTMLEscapeString(s)
		return template.HTML(`<a href="` + esc + `" target="_blank" rel="noopener">` + esc + `</a>`)
	}
	return df
}

// HTML renders the value as markup rather than escaped text — the read side of
// a Form.Richtext field.
//
// The value is sanitized again here, not merely trusted for having been
// sanitized on save. Rows predating the Richtext field, rows written by a
// migration or a direct SQL fix, and rows from another writer never passed
// through that path, so cleaning on render is what makes the guarantee hold for
// the data actually in the table.
func (df *DetailField[T]) HTML() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(orEmpty(v))
		if s == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(`<div class="prose-sm max-w-none">` + htmlsafe.Sanitize(s) + `</div>`) //nolint:gosec // htmlsafe.Sanitize is the allowlist boundary
	}
	return df
}

// JSON pretty-prints the value as a code block.
func (df *DetailField[T]) JSON() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			out = fmt.Append(nil, v)
		}
		return template.HTML(`<pre class="rounded-md bg-muted p-3 text-sm overflow-x-auto"><code>` + template.HTMLEscapeString(string(out)) + `</code></pre>`)
	}
	return df
}

// Filesize renders a byte count as a human size.
func (df *DetailField[T]) Filesize() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		var n float64
		if _, err := fmt.Sscan(fmt.Sprint(v), &n); err != nil {
			return defaultCell(v)
		}
		units := []string{"B", "KB", "MB", "GB", "TB"}
		i := 0
		for n >= 1024 && i < len(units)-1 {
			n /= 1024
			i++
		}
		return template.HTML(template.HTMLEscapeString(fmt.Sprintf("%.1f %s", n, units[i])))
	}
	return df
}

// Markdown renders the raw text preserving whitespace (no HTML rendering —
// bring your own renderer through As for rich output).
func (df *DetailField[T]) Markdown() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(v)
		if s == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(`<div style="white-space: pre-wrap">` + template.HTMLEscapeString(s) + `</div>`)
	}
	return df
}

// Using maps stored values to display text.
func (df *DetailField[T]) Using(m map[any]string) *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML {
		if s, ok := m[v]; ok {
			return template.HTML(template.HTMLEscapeString(s))
		}
		if s, ok := m[fmt.Sprint(v)]; ok {
			return template.HTML(template.HTMLEscapeString(s))
		}
		return defaultCell(v)
	}
	return df
}

// relationGrid embeds a related resource's rows under the detail panel.
type relationGrid[T any] struct {
	title string
	typ   reflect.Type
	bind  func(q *ListQuery, m *T)
}

// RelationGrid embeds a table of related C rows on T's detail page. C must
// be a registered resource; bind scopes the query to the shown record:
//
//	steward.RelationGrid(d, "Posts by this author",
//	    func(q *steward.ListQuery, a *Author) {
//	        q.Conds = append(q.Conds, steward.Cond{Path: "AuthorID", Op: steward.OpEq, Val: a.ID})
//	    })
func RelationGrid[T, C any](d *Detail[T], title string, bind func(q *ListQuery, m *T)) {
	d.relations = append(d.relations, relationGrid[T]{
		title: title,
		typ:   reflect.TypeFor[C](),
		bind:  bind,
	})
}

// ---- rendering ----------------------------------------------------------------

type detailRowVM struct {
	Label string
	HTML  template.HTML
}

type detailRelVM struct {
	Title   string
	Columns []string
	Rows    [][]template.HTML
	More    string // URL to the related resource's grid, "" when absent
}

type detailVM struct {
	Title     string
	Key       string
	Rows      []detailRowVM
	Relations []detailRelVM
	EditURL   string
	ListURL   string
	DeleteURL string
	CanEdit   bool
	CanDelete bool
}

func (t *typedResource[T]) show(c *Context) error {
	id := c.R.PathValue("id")
	row, err := t.repo.Find(c.Ctx(), id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Admin.renderError(c, http.StatusNotFound, "Record not found", nil)
			return nil
		}
		return err
	}
	if !t.canView(c, row) {
		return t.denyPolicy(c)
	}
	if c.WantsJSON() {
		c.W.Header().Set("Vary", "HX-Request, Accept")
		return c.JSON(http.StatusOK, row)
	}

	m := t.res.m
	vm := &detailVM{
		Title:     m.title,
		Key:       id,
		ListURL:   c.URL(m.slug),
		EditURL:   c.URL(m.slug, id, "edit"),
		DeleteURL: c.URL(m.slug, id),
		CanEdit:   t.grid.enabled("edit") && t.canUpdate(c, row),
		CanDelete: t.grid.enabled("delete") && t.canDelete(c, row),
	}
	for _, df := range t.detail.fields {
		if df.info == nil {
			continue
		}
		v, ok := df.info.value(reflect.ValueOf(row))
		var val any
		if ok {
			val = v
		}
		html := defaultCell(val)
		if df.present != nil {
			html = df.present(val, row)
		}
		vm.Rows = append(vm.Rows, detailRowVM{Label: df.label, HTML: html})
	}
	for _, rel := range t.detail.relations {
		entry, ok := c.Admin.byType[rel.typ]
		if !ok {
			continue
		}
		q := &ListQuery{Page: 1, PerPage: 10}
		if rel.bind != nil {
			rel.bind(q, row)
		}
		relVM, err := entry.renderRelation(c, rel.title, q)
		if err != nil {
			return err
		}
		if relVM == nil { // the related resource's policy denies ViewAny
			continue
		}
		vm.Relations = append(vm.Relations, *relVM)
	}
	return c.Admin.render(c, "detail/page.html", m.title+" #"+id, vm)
}

// renderRelation renders this resource's grid columns for an embedded
// relation table on another resource's detail page. A nil result (no error)
// means this resource's policy hides the section entirely.
func (t *typedResource[T]) renderRelation(c *Context, title string, q *ListQuery) (*detailRelVM, error) {
	if !t.canViewAny(c) {
		return nil, nil
	}
	t.applyRowScope(c, q)
	items, _, err := t.repo.List(c.Ctx(), q)
	if err != nil {
		return nil, err
	}
	vm := &detailRelVM{Title: title, More: c.URL(t.res.m.slug)}
	for _, col := range t.grid.columns {
		if col.hidden {
			continue
		}
		vm.Columns = append(vm.Columns, col.label)
	}
	for i := range items {
		row := &items[i]
		var cells []template.HTML
		for _, col := range t.grid.columns {
			if col.hidden {
				continue
			}
			cells = append(cells, t.renderCell(col, row))
		}
		vm.Rows = append(vm.Rows, cells)
	}
	return vm, nil
}

// defaultDetailFields mirrors the zero-config projection.
func (t *typedResource[T]) defaultDetailFields(d *Detail[T]) {
	for _, f := range t.ft.model.Fields {
		info, ok := t.ft.byPath[f.Name]
		if !ok || info.Kind == kindBytes || info.Kind == kindOther {
			continue
		}
		d.Field(f.Name)
	}
}

func (t *typedResource[T]) compileDetail(a *Admin) {
	d := newDetail(t.res)
	if t.res.detailFn != nil {
		t.res.detailFn(d)
	} else {
		t.defaultDetailFields(d)
	}
	t.detail = d
	for _, df := range d.fields {
		info, err := t.ft.lookup(df.path)
		if err != nil {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf("resource %q: detail field: %w", t.res.m.slug, err))
			continue
		}
		df.info = info
		if df.label == "" {
			df.label = info.Label
		}
	}
	for i := range d.relations {
		if _, ok := a.byType[d.relations[i].typ]; !ok {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: RelationGrid target %s is not a registered resource", t.res.m.slug, d.relations[i].typ))
		}
	}
}
