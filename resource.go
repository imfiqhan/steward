package steward

import (
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/jinzhu/inflection"

	"github.com/imfiqhan/steward/internal/rules"
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

	commandPaths   []string
	commandDisplay []string
	searchPaths    []string

	gridFn   func(*Grid[T])
	formFn   func(*Form[T])
	detailFn func(*Detail[T])
	repo     Repository[T]
	policy   Policy[T]
	pages    []customPage
}

type customPage struct {
	method string
	rel    string
	h      handlerFunc
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
	a.byType[t] = entry
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

// Icon sets the sidebar icon by name, resolved through the asset layers
// ("news", "users", …). Verify reports a name that does not resolve, because at
// runtime an unknown icon renders blank rather than failing — which is easy to
// miss until a sidebar looks wrong.
func (r *Resource[T]) Icon(name string) *Resource[T] { r.m.icon = name; return r }

// Group places the resource under a collapsible sidebar group.
func (r *Resource[T]) Group(name string) *Resource[T] { r.m.group = name; return r }

// Command makes the resource searchable from the command palette, over the
// paths given.
//
//	posts.Command("Title", "Slug")
//
// It is opt-in rather than following QuickSearch, because the palette queries on
// every keystroke and a LIKE over a table nobody meant to include is paid for by
// every search that misses. Measured on one panel: eleven resources searched
// automatically cost 1.5–4.2s per keystroke, the worst of it on queries that
// matched nothing.
func (r *Resource[T]) Command(paths ...string) *Resource[T] {
	r.commandPaths = append(r.commandPaths, paths...)
	return r
}

// CommandDisplay names what a palette row reads, rather than letting it be
// guessed from the grid's first two text columns. The first path is the line
// the reader reads; the rest join into the dimmer line beside it.
//
//	posts.Command("Title").CommandDisplay("Title", "Category.Name", "PostDate")
//
// A path may cross one relation, and is loaded for you.
func (r *Resource[T]) CommandDisplay(paths ...string) *Resource[T] {
	r.commandDisplay = append(r.commandDisplay, paths...)
	return r
}

// Grid declares the list view; fn runs at Build time against a fresh
// builder. Without it the grid shows every direct model field.
func (r *Resource[T]) Grid(fn func(*Grid[T])) *Resource[T] { r.gridFn = fn; return r }

// Form declares the create/edit view; without it every writable direct
// field gets an input inferred from its type.
func (r *Resource[T]) Form(fn func(*Form[T])) *Resource[T] { r.formFn = fn; return r }

// Detail declares the show view; without it every direct field renders with
// its type default.
func (r *Resource[T]) Detail(fn func(*Detail[T])) *Resource[T] { r.detailFn = fn; return r }

// Page mounts a custom route under the resource ("_order" →
// {prefix}/{slug}/_order). The handler runs inside the standard middleware
// chain (auth, CSRF, permissions).
func (r *Resource[T]) Page(method, rel string, h func(c *Context) error) *Resource[T] {
	r.pages = append(r.pages, customPage{method: strings.ToUpper(method), rel: strings.Trim(rel, "/"), h: h})
	return r
}

// Repository swaps the data source (default: GORM repository over Config.DB).
func (r *Resource[T]) Repository(repo Repository[T]) *Resource[T] { r.repo = repo; return r }

// Policy attaches fine-grained authorization. ViewAny gates the grid, its
// JSON listing, exports, and the sidebar entry; Create gates the create
// form and submissions; View/Update/Delete receive the loaded row, so
// ownership checks live here. Implement RowScoper on the same value to
// narrow every list query. Policies bind every user, administrators
// included — they express business rules, not role membership (use
// permissions for that).
func (r *Resource[T]) Policy(p Policy[T]) *Resource[T] { r.policy = p; return r }

// typedResource is the erased registry entry; the any→T boundary lives here
// and nowhere else.
type typedResource[T any] struct {
	res    *Resource[T]
	ft     *fieldTable
	grid   *Grid[T]
	form   *Form[T]
	detail *Detail[T]
	repo   Repository[T]
	policy Policy[T]

	// commandCols are the resolved CommandDisplay paths, in the order given.
	commandCols []*fieldInfo
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
	t.policy = t.res.policy

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
		for _, colour := range col.badges {
			if !badgeColors[colour] {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: column %q: unknown badge colour %q (known colours: %s)",
					t.res.m.slug, col.path, colour, strings.Join(badgeColorNames(), ", ")))
			}
		}
		if col.disk != "" {
			if _, ok := a.Disk(col.disk); !ok {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: column %q: unknown disk %q (configured: %s)",
					t.res.m.slug, col.path, col.disk, strings.Join(a.DiskNames(), ", ")))
			}
		}
		if n := len(col.boolLabels); n != 0 && n != 2 {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: column %q: Bool takes no labels or exactly two, got %d",
				t.res.m.slug, col.path, n))
		}
		col.info = verify("grid column", col.path)
		if col.info != nil {
			if col.label == "" {
				col.label = col.info.Label
			}
			if col.info.Relation != "" && !slices.Contains(preloads, col.info.Relation) {
				preloads = append(preloads, col.info.Relation)
			}
			if col.sortable && col.info.DBName == "" {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: column %q is a relation path and cannot be Sortable",
					t.res.m.slug, col.path))
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
		if fi.info == nil {
			continue
		}
		if !fi.info.filterable() {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: grid filter %q: relation %q has a composite key, which cannot be filtered",
				t.res.m.slug, fi.path, fi.info.Relation))
		}
		if fi.label == "" {
			fi.label = fi.info.Label
		}
	}
	for _, p := range g.quickSearch {
		if info := verify("quick search", p); info != nil && !info.filterable() {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: quick search %q: relation %q has a composite key, which cannot be searched",
				t.res.m.slug, p, info.Relation))
		}
	}
	if l := g.filterLayout; l != "" && !filterLayouts[l] {
		a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
			"resource %q: unknown filter layout %q (known: %s, %s)",
			t.res.m.slug, l, FiltersAbove, FiltersDrawer))
	}
	for _, p := range t.res.commandPaths {
		if info := verify("command search", p); info != nil && !info.filterable() {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: command search %q: relation %q has a composite key, which cannot be searched",
				t.res.m.slug, p, info.Relation))
		}
	}
	for _, p := range t.res.commandDisplay {
		info := verify("command display", p)
		if info == nil {
			continue
		}
		t.commandCols = append(t.commandCols, info)
		if info.Relation != "" && !slices.Contains(preloads, info.Relation) {
			preloads = append(preloads, info.Relation)
			if gr, ok := t.repo.(*GormRepository[T]); ok {
				gr.With(info.Relation)
			}
		}
	}
	if g.defaultSort != nil {
		// Sorting cannot go through the relation subquery — ORDER BY needs the
		// column in the result set, which means a join. Reject it at boot
		// instead of silently ignoring the sort at click time.
		if info := verify("default sort", g.defaultSort.Path); info != nil && info.DBName == "" {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: default sort %q is a relation path; sort by a column on the model itself",
				t.res.m.slug, g.defaultSort.Path))
		}
	}
	if g.treePath != "" {
		if info := verify("tree parent", g.treePath); info != nil && info.Relation != "" {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: tree parent %q must be a direct column", t.res.m.slug, g.treePath))
		}
	}

	// Header groups must cover contiguous, non-hidden columns.
	colIdx := map[string]int{}
	for i, col := range g.columns {
		colIdx[col.path] = i
	}
	for gi := range g.headerGroups {
		hg := &g.headerGroups[gi]
		indexes := make([]int, 0, len(hg.paths))
		bad := false
		for _, p := range hg.paths {
			idx, ok := colIdx[p]
			if !ok {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: header group %q references unknown column %q", t.res.m.slug, hg.label, p))
				bad = true
				break
			}
			if g.columns[idx].hidden {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: header group %q includes hidden column %q", t.res.m.slug, hg.label, p))
				bad = true
				break
			}
			indexes = append(indexes, idx)
		}
		if bad || len(indexes) == 0 {
			hg.span = 0
			continue
		}
		slices.Sort(indexes)
		contiguous := true
		for i := 1; i < len(indexes); i++ {
			if indexes[i] != indexes[i-1]+1 {
				contiguous = false
				break
			}
		}
		if !contiguous {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: header group %q columns must be contiguous", t.res.m.slug, hg.label))
			continue
		}
		hg.start, hg.span = indexes[0], len(indexes)
	}

	// Form.
	fm := newForm(t.res)
	if t.res.formFn != nil {
		t.res.formFn(fm)
	} else {
		t.defaultFormFields(fm)
	}
	t.form = fm
	for _, fd := range fm.fields {
		if fd.divider {
			continue
		}
		// A rule the engine does not know is skipped without a word, so
		// "requried" removes a required check and nothing says so.
		for _, spec := range []string{fd.rules, fd.createRules, fd.updateRules} {
			for _, name := range rules.Unknown(spec) {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: field %q: unknown validation rule %q (known rules: %s)",
					t.res.m.slug, fd.path, name, strings.Join(rules.Names(), ", ")))
			}
		}
		// Likewise an unknown disk: uploads would quietly go to the default one.
		if fd.disk != "" {
			if _, ok := a.Disk(fd.disk); !ok {
				a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
					"resource %q: field %q: unknown disk %q (configured: %s)",
					t.res.m.slug, fd.path, fd.disk, strings.Join(a.DiskNames(), ", ")))
			}
		}
		if fd.virtual {
			if fd.label == "" {
				fd.label = splitCamel(fd.path)
			}
			continue
		}
		fd.info = verify("form field", fd.path)
		if fd.info == nil {
			continue
		}
		if fd.label == "" {
			fd.label = fd.info.Label
		}
		if fd.kind == FieldBelongsTo {
			t.resolveBelongsTo(a, fd)
		}
	}
	for _, n := range fm.nested {
		if err := n.compile(a, t); err != nil {
			return err
		}
	}

	// Inline-editable columns must map to a form field of the same path so
	// their writes share the form's validation and hooks.
	for _, col := range g.columns {
		if col.inline == inlineNone || col.computed {
			continue
		}
		var match *Field[T]
		for _, fd := range fm.fields {
			if fd.path == col.path && !fd.virtual && !fd.ignored {
				match = fd
				break
			}
		}
		switch {
		case match == nil:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: inline column %q has no matching form field", t.res.m.slug, col.path))
			col.inline = inlineNone
		case col.inline == inlineSwitch && match.kind != FieldSwitch:
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"resource %q: inline switch column %q needs a form Switch field", t.res.m.slug, col.path))
			col.inline = inlineNone
		}
	}

	t.verifyActions(a)
	t.compileDetail(a)
	return nil
}

// resolveBelongsTo fills the relation's table/pk/title columns for option
// loading, verifying the relation and title field exist.
func (t *typedResource[T]) resolveBelongsTo(a *Admin, fd *Field[T]) {
	rel, ok := t.ft.model.Relationships.Relations[fd.relName]
	if !ok || rel.FieldSchema == nil {
		a.verifyErrs = append(a.verifyErrs,
			fmt.Errorf("resource %q: form field %q: unknown relation %q", t.res.m.slug, fd.path, fd.relName))
		return
	}
	titleField := rel.FieldSchema.LookUpField(fd.relTitle)
	if titleField == nil {
		a.verifyErrs = append(a.verifyErrs,
			fmt.Errorf("resource %q: form field %q: relation %s has no field %q", t.res.m.slug, fd.path, fd.relName, fd.relTitle))
		return
	}
	fd.relTable = rel.FieldSchema.Table
	fd.relTitleCol = titleField.DBName
	if len(rel.FieldSchema.PrimaryFields) > 0 {
		fd.relPKCol = rel.FieldSchema.PrimaryFields[0].DBName
	} else {
		fd.relPKCol = "id"
	}
}

// defaultFormFields projects the zero-config form: every writable direct
// field, widget inferred from the field's Go type and name.
func (t *typedResource[T]) defaultFormFields(fm *Form[T]) {
	for _, f := range t.ft.model.Fields {
		info, ok := t.ft.byPath[f.Name]
		if !ok || info.Primary || info.Kind == kindBytes || info.Kind == kindOther {
			continue
		}
		if f.Name == "CreatedAt" || f.Name == "UpdatedAt" || f.Name == "DeletedAt" {
			continue
		}
		lower := strings.ToLower(f.Name)
		switch {
		case info.Kind == kindBool:
			fm.Switch(f.Name)
		case info.Kind == kindTime:
			fm.Datetime(f.Name)
		case info.Kind == kindInt || info.Kind == kindUint:
			fm.Number(f.Name)
		case info.Kind == kindFloat:
			fm.Decimal(f.Name)
		case strings.Contains(lower, "password"):
			fm.Password(f.Name)
		case strings.Contains(lower, "email"):
			fm.Email(f.Name)
		case strings.Contains(lower, "url") || strings.Contains(lower, "link"):
			fm.URL(f.Name)
		case strings.Contains(lower, "color"):
			fm.Color(f.Name)
		case info.Kind == kindString && f.Size == 0:
			// TEXT columns (no size limit) read better as textareas.
			fm.Textarea(f.Name)
		default:
			fm.Text(f.Name)
		}
	}
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
	base := a.url(m.slug)
	mux.HandleFunc("GET "+base, a.h(t.index))
	mux.HandleFunc("POST "+base, a.h(t.store))
	mux.HandleFunc("GET "+base+"/create", a.h(t.createPage))
	// Special endpoints use single literal segments (+ ?field=) — two-segment
	// wildcards like "_options/{field}" would be ambiguous against
	// "{id}/edit" for the Go 1.22 mux.
	mux.HandleFunc("GET "+base+"/_schema", a.h(t.schemaJSON))
	mux.HandleFunc("GET "+base+"/_options", a.h(t.optionsJSON))
	mux.HandleFunc("POST "+base+"/_upload", a.h(t.uploadFile))
	mux.HandleFunc("POST "+base+"/_preview", a.h(t.previewMarkdown))
	mux.HandleFunc("POST "+base+"/_action/{name}", a.h(t.dispatchAction))
	for _, p := range t.res.pages {
		mux.HandleFunc(p.method+" "+base+"/"+p.rel, a.h(p.h))
	}
	mux.HandleFunc("GET "+base+"/{id}", a.h(t.show))
	mux.HandleFunc("GET "+base+"/{id}/edit", a.h(t.editPage))
	mux.HandleFunc("PUT "+base+"/{id}", a.h(t.update))
	mux.HandleFunc("PATCH "+base+"/{id}", a.h(t.update))
	mux.HandleFunc("DELETE "+base+"/{id}", a.h(t.destroy))
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
