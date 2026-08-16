package steward

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"reflect"
	"slices"
	"strings"

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

// FieldFunc adds a row whose value is computed from the whole record rather
// than read from one path, for anything a struct field cannot name: a
// collection, a summary, several values at once.
func (d *Detail[T]) FieldFunc(name, label string, fn func(row *T) template.HTML) *DetailField[T] {
	df := &DetailField[T]{path: name, label: label, computed: true}
	df.present = func(_ any, row *T) template.HTML { return fn(row) }
	d.fields = append(d.fields, df)
	return df
}

// DetailField is one show-view row; transformer methods chain.
type DetailField[T any] struct {
	path  string
	label string

	present func(v any, m *T) template.HTML
	info    *fieldInfo

	// storageRef resolves the value through Storage before presenting it, for
	// helpers that put it straight into a src or an href.
	storageRef bool

	// block gives the value the row's full width, under its label rather than
	// beside it. Long markup needs it; a date does not.
	block bool

	// copyable puts a copy button beside the value.
	copyable bool

	// disk names which storage disk a stored path belongs to.
	disk string

	// computed marks a row whose value comes from FieldFunc rather than from a
	// path, so neither resolution nor verification applies to it.
	computed bool

	// badges is what Badge was given, kept so Verify can check the colours.
	badges map[any]BadgeColor

	// labels is what Using was given, consulted for the text a badge shows.
	labels map[any]string

	// boolLabels is what Bool was given, kept so Verify can check the count.
	boolLabels []string
}

// Disk names which storage disk this field's stored paths live on, for the
// helpers that resolve one into a URL. Unset, the default disk is used.
func (df *DetailField[T]) Disk(name string) *DetailField[T] { df.disk = name; return df }

// Block puts the value under its label across the row's full width, rather than
// beside it. HTML and Markdown do this already.
func (df *DetailField[T]) Block() *DetailField[T] { df.block = true; return df }

// Copyable adds a button that copies the value to the clipboard. It copies what
// is stored, not what is displayed, so a formatted number or a shortened path
// still yields the value someone would paste elsewhere.
func (df *DetailField[T]) Copyable() *DetailField[T] { df.copyable = true; return df }

// As renders the value with a custom function.
func (df *DetailField[T]) As(fn func(v any, m *T) template.HTML) *DetailField[T] {
	df.present = fn
	return df
}

// Badge renders a colored badge (see Column.Badge). With Using, the colour is
// keyed on the stored value and the text comes from Using's map.
func (df *DetailField[T]) Badge(colors map[any]BadgeColor) *DetailField[T] {
	df.badges = colors
	df.present = df.mappedHTML
	return df
}

// mappedHTML renders under whichever of Badge and Using are set. Both install
// it, so the two compose whichever order they are called in.
func (df *DetailField[T]) mappedHTML(v any, _ *T) template.HTML {
	text, labeled := df.labelFor(v)
	switch {
	case df.badges != nil && labeled:
		return badgeLabeledHTML(df.badges, v, text)
	case df.badges != nil:
		return badgeHTML(df.badges, v)
	case labeled:
		return template.HTML(template.HTMLEscapeString(text))
	}
	return defaultCell(v)
}

// labelFor looks the value up in Using's map, by value and by its fmt form.
func (df *DetailField[T]) labelFor(v any) (string, bool) {
	if df.labels == nil {
		return "", false
	}
	if s, ok := df.labels[v]; ok {
		return s, true
	}
	if s, ok := df.labels[fmt.Sprint(v)]; ok {
		return s, true
	}
	return "", false
}

// Bool renders a truthy/falsy value as a status. It says Yes and No unless
// given two words of its own: Bool("Ya", "Tidak").
func (df *DetailField[T]) Bool(labels ...string) *DetailField[T] {
	df.boolLabels = labels
	yes, no := boolWords(labels)
	df.present = func(v any, _ *T) template.HTML { return statusHTML(truthy(v), yes, no) }
	return df
}

// Image renders the value (URL or storage path) as an image; storage paths
// resolve through the configured Storage at render time.
func (df *DetailField[T]) Image(width, height int) *DetailField[T] {
	df.storageRef = true
	df.present = func(v any, _ *T) template.HTML {
		_, s := refParts(v)
		if s == "" {
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

// Link renders the value as a link to itself, for a column holding a URL or an
// uploaded file's path. A storage-relative path resolves through the configured
// Storage, so a File field's value downloads rather than 404ing; an absolute URL
// is left as it stands.
func (df *DetailField[T]) Link() *DetailField[T] {
	df.storageRef = true
	df.present = func(v any, _ *T) template.HTML {
		// The link shows the stored value and points at the resolved one, so a
		// file path stays readable instead of being replaced by its URL.
		raw, href := refParts(v)
		if raw == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(`<a href="` + template.HTMLEscapeString(href) +
			`" target="_blank" rel="noopener">` + template.HTMLEscapeString(raw) + `</a>`)
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
	df.block = true
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
	df.block = true
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

// Markdown renders the value as markdown (GitHub flavour), sanitized through
// the same allowlist a Richtext value passes.
func (df *DetailField[T]) Markdown() *DetailField[T] {
	df.block = true
	df.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(v)
		if s == "" {
			return `<span class="text-muted-foreground">—</span>`
		}
		return template.HTML(`<div class="prose-sm max-w-none">` + string(renderMarkdown(s)) + `</div>`) //nolint:gosec // renderMarkdown sanitizes
	}
	return df
}

// Preformatted renders the value as text, keeping its line breaks and runs of
// spaces. This is what Markdown did before it rendered anything.
func (df *DetailField[T]) Preformatted() *DetailField[T] {
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
	df.labels = m
	df.present = df.mappedHTML
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
	Block bool
	// Copy is the raw value the copy button writes to the clipboard, empty when
	// the field has no button.
	Copy string
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
		if df.info == nil && !df.computed {
			continue
		}
		var val any
		if !df.computed {
			if v, ok := df.info.value(reflect.ValueOf(row)); ok {
				val = v
			}
		}
		if df.storageRef && val != nil {
			s := fmt.Sprint(val)
			val = resolvedRef{raw: s, url: c.Admin.DiskURL(df.disk, s)}
		}
		html := defaultCell(val)
		if df.present != nil {
			html = df.present(val, row)
		}
		rowVM := detailRowVM{Label: df.label, HTML: html, Block: df.block}
		if df.copyable && val != nil {
			if r, ok := val.(resolvedRef); ok {
				rowVM.Copy = r.raw
			} else {
				rowVM.Copy = fmt.Sprint(val)
			}
		}
		vm.Rows = append(vm.Rows, rowVM)
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
	var preloads []string
	for _, df := range d.fields {
		for _, colour := range df.badges {
			if !badgeColors[colour] {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: detail field %q: unknown badge colour %q (known colours: %s)",
					t.res.m.slug, df.path, colour, strings.Join(badgeColorNames(), ", ")))
			}
		}
		if n := len(df.boolLabels); n != 0 && n != 2 {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: detail field %q: Bool takes no labels or exactly two, got %d",
				t.res.m.slug, df.path, n))
		}
		if df.disk != "" {
			if _, ok := a.Disk(df.disk); !ok {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: detail field %q: unknown disk %q (configured: %s)",
					t.res.m.slug, df.path, df.disk, strings.Join(a.DiskNames(), ", ")))
			}
		}
		if df.computed {
			if df.label == "" {
				df.label = df.path
			}
			continue
		}
		info, err := t.ft.lookup(df.path)
		if err != nil {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf("resource %q: detail field: %w", t.res.m.slug, err))
			continue
		}
		df.info = info
		if df.label == "" {
			df.label = info.Label
		}
		if info.Relation != "" && !slices.Contains(preloads, info.Relation) {
			preloads = append(preloads, info.Relation)
		}
	}
	// A detail field naming a relation needs that relation loaded. The grid
	// registers preloads for its own columns, which covered a detail page only
	// where the two happened to name the same relation; anywhere else the row
	// rendered as if the value were unset.
	if gr, ok := t.repo.(*GormRepository[T]); ok && len(preloads) > 0 {
		gr.With(preloads...)
	}
	for i := range d.relations {
		if _, ok := a.byType[d.relations[i].typ]; !ok {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: RelationGrid target %s is not a registered resource", t.res.m.slug, d.relations[i].typ))
		}
	}
}
