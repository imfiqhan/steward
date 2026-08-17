package steward

import (
	"fmt"
	"html/template"
	"sort"
)

// Grid configures a resource's list view. Obtained inside
// Resource[T].Grid(func(g *Grid[T]) { ... }); write-only until Build.
type Grid[T any] struct {
	res *Resource[T]

	columns     []*Column[T]
	filters     []*FilterItem[T]
	quickSearch []string

	rowActions   []*Action
	batchActions []*Action
	toolActions  []*Action
	// actionStyle overrides Config.GridActions for this grid; "" inherits it.
	actionStyle GridActionStyle
	// filterLayout overrides Config.FilterLayout for this grid; "" inherits it.
	filterLayout GridFilterLayout

	headerGroups []headerGroup

	perPage        int
	perPageOptions []int
	defaultSort    *Sort
	reorderURL     string
	treePath       string

	off map[string]bool // feature switches (create, delete, filter, ...)
}

func newGrid[T any](res *Resource[T]) *Grid[T] {
	return &Grid[T]{res: res, perPage: 10, perPageOptions: []int{10, 20, 50, 100}, off: map[string]bool{}}
}

// Column adds a model field column ("Title", "Author.Name"); the optional
// second argument overrides the derived label.
func (g *Grid[T]) Column(path string, label ...string) *Column[T] {
	c := &Column[T]{path: path}
	if len(label) > 0 {
		c.label = label[0]
	}
	g.columns = append(g.columns, c)
	return c
}

// ColumnFunc adds a computed column rendered entirely by fn.
func (g *Grid[T]) ColumnFunc(name, label string, fn func(row *T) template.HTML) *Column[T] {
	c := &Column[T]{path: name, label: label, computed: true}
	c.present = func(_ any, row *T) template.HTML { return fn(row) }
	g.columns = append(g.columns, c)
	return c
}

// QuickSearch enables the search box over the given field paths, with the
// dcat mini-DSL (field:value, %contains%, >n, (a,b), [lo,hi], NULL).
func (g *Grid[T]) QuickSearch(paths ...string) *Grid[T] {
	g.quickSearch = append(g.quickSearch, paths...)
	return g
}

// Filter declares the filter panel.
func (g *Grid[T]) Filter(fn func(*Filters[T])) *Grid[T] {
	fn(&Filters[T]{grid: g})
	return g
}

// PerPage sets the default page size and the selector options.
func (g *Grid[T]) PerPage(def int, options ...int) *Grid[T] {
	if def > 0 {
		g.perPage = def
	}
	if len(options) > 0 {
		g.perPageOptions = options
	}
	return g
}

// DefaultSort orders the grid before the user picks a column.
func (g *Grid[T]) DefaultSort(path string, desc bool) *Grid[T] {
	g.defaultSort = &Sort{Path: path, Desc: desc}
	return g
}

// Reorderable makes rows draggable; on drop the new order posts the row
// keys (form field "ids", comma-separated, top to bottom) to url. Pair it
// with a Resource.Page handler that persists the order.
func (g *Grid[T]) Reorderable(url string) *Grid[T] {
	g.reorderURL = url
	return g
}

// Tree renders rows as a collapsible hierarchy over the given parent-key
// field ("ParentID"). The whole tree loads at once (up to 1000 rows) in
// depth-first order; quick search and filters fall back to the flat list.
func (g *Grid[T]) Tree(parentPath string) *Grid[T] {
	g.treePath = parentPath
	return g
}

// headerGroup spans a label over contiguous columns (complex headers).
type headerGroup struct {
	label string
	paths []string
	start int // resolved at compile
	span  int
}

// GroupColumns spans a header label over the named columns, which must be
// contiguous in declaration order (verified at Build). The column picker is
// disabled on grids with grouped headers.
func (g *Grid[T]) GroupColumns(label string, paths ...string) *Grid[T] {
	g.headerGroups = append(g.headerGroups, headerGroup{label: label, paths: paths})
	return g
}

// Feature switches, dcat-style disable pairs.

// ActionStyle overrides Config.GridActions for this grid.
//
// Use it where one resource does not fit the panel-wide choice — a grid with a
// single action rarely needs a menu, and a grid with six rarely wants them
// spread across the row.
func (g *Grid[T]) ActionStyle(style GridActionStyle) *Grid[T] {
	g.actionStyle = style
	return g
}

// DisableCreate hides the create button.
func (g *Grid[T]) DisableCreate() *Grid[T] { g.off["create"] = true; return g }

// DisableDelete hides row delete actions and batch delete.
func (g *Grid[T]) DisableDelete() *Grid[T] { g.off["delete"] = true; return g }

// DisableEdit hides row edit actions.
func (g *Grid[T]) DisableEdit() *Grid[T] { g.off["edit"] = true; return g }

// DisableView hides the row detail action.
func (g *Grid[T]) DisableView() *Grid[T] { g.off["view"] = true; return g }

// GridFilterLayout chooses where a grid's filter panel lives.
type GridFilterLayout string

const (
	// FiltersAbove puts the panel between the toolbar and the rows, open in
	// place. The default.
	FiltersAbove GridFilterLayout = "above"
	// FiltersDrawer puts it in a drawer from the right, over the page. Worth it
	// where a grid has enough filters that opening them in place pushes the
	// rows off the screen.
	FiltersDrawer GridFilterLayout = "drawer"
)

// filterLayouts lists what the template renders. One outside it would fall
// back to "above" silently, which is why Verify reports it.
var filterLayouts = map[GridFilterLayout]bool{FiltersAbove: true, FiltersDrawer: true}

// FilterLayout overrides Config.FilterLayout for this grid.
func (g *Grid[T]) FilterLayout(l GridFilterLayout) *Grid[T] {
	g.filterLayout = l
	return g
}

// DisableFilter hides the filter panel.
func (g *Grid[T]) DisableFilter() *Grid[T] { g.off["filter"] = true; return g }

// DisableExport hides CSV export.
func (g *Grid[T]) DisableExport() *Grid[T] { g.off["export"] = true; return g }

// DisableRowSelector hides checkboxes (and with them batch actions).
func (g *Grid[T]) DisableRowSelector() *Grid[T] { g.off["selector"] = true; return g }

// DisablePagination shows all rows.
func (g *Grid[T]) DisablePagination() *Grid[T] { g.off["pagination"] = true; return g }

// DisableQuickSearch hides the search box even when paths are set.
func (g *Grid[T]) DisableQuickSearch() *Grid[T] { g.off["quicksearch"] = true; return g }

func (g *Grid[T]) enabled(feature string) bool { return !g.off[feature] }

// inlineKind marks a column as live-editable from the grid.
type inlineKind int

const (
	inlineNone inlineKind = iota
	inlineSwitch
	inlineText
)

// Column configures one grid column; every method returns the column for
// chaining. Display callbacks receive the typed row — never a map.
type Column[T any] struct {
	path     string
	label    string
	computed bool

	sortable bool
	hidden   bool
	width    int
	help     string
	inline   inlineKind

	// transform mutates the raw value before presentation (Limit, Using).
	transform []func(v any, row *T) any
	// present renders the final cell; nil falls back to the type default.
	present func(v any, row *T) template.HTML

	// storageRef resolves the value through Storage before presenting it, for
	// helpers that put it straight into a src or an href.
	storageRef bool

	// disk names which storage disk a stored path belongs to.
	disk string

	// badges is what Badge was given, kept so Verify can check the colours.
	badges map[any]BadgeColor

	// boolLabels is what Bool was given, kept so Verify can check the count.
	boolLabels []string

	info *fieldInfo // resolved at compile
}

// Switch renders a live toggle that saves immediately through the form
// pipeline. The resource's form must declare a Switch field for the same
// path — its rules and hooks apply to inline edits too.
func (c *Column[T]) Switch() *Column[T] { c.inline = inlineSwitch; return c }

// Editable renders a click-to-edit text cell saving through the form
// pipeline; the form must declare a field for the same path.
func (c *Column[T]) Editable() *Column[T] { c.inline = inlineText; return c }

// Sortable makes the header clickable.
func (c *Column[T]) Sortable() *Column[T] { c.sortable = true; return c }

// Hide renders the column hidden (still exported).
func (c *Column[T]) Hide() *Column[T] { c.hidden = true; return c }

// Width fixes the column width in pixels.
func (c *Column[T]) Width(px int) *Column[T] { c.width = px; return c }

// Help adds a header tooltip.
func (c *Column[T]) Help(s string) *Column[T] { c.help = s; return c }

// Display renders the cell with a custom function (receives the raw field
// value and the typed row).
func (c *Column[T]) Display(fn func(v any, row *T) template.HTML) *Column[T] {
	c.present = fn
	return c
}

// Using maps raw values to replacement text ({"1": "Yes"}).
func (c *Column[T]) Using(m map[any]string) *Column[T] {
	c.transform = append(c.transform, func(v any, _ *T) any {
		if s, ok := m[v]; ok {
			return s
		}
		if s, ok := m[fmt.Sprint(v)]; ok {
			return s
		}
		return v
	})
	return c
}

// Limit truncates long text with an ellipsis and a title tooltip.
func (c *Column[T]) Limit(n int) *Column[T] {
	c.transform = append(c.transform, func(v any, _ *T) any {
		s := fmt.Sprint(v)
		if len([]rune(s)) <= n {
			return s
		}
		return string([]rune(s)[:n]) + "…"
	})
	return c
}

// badgeHTML renders a Basecoat badge; named colors map onto Tailwind
// palette utilities (kept in sync with the @source inline safelist in
// frontend/src/app.css), everything else falls back to the secondary
// variant.
// BadgeColor names one of the badge palettes. It is a string type rather than
// an integer enum on purpose: a value the framework does not know still
// compiles, so a panel with its own palette is not shut out — but the ones that
// exist are discoverable and a typo in them is caught by Verify.
type BadgeColor string

const (
	BadgeGreen       BadgeColor = "green"
	BadgeBlue        BadgeColor = "blue"
	BadgeAzure       BadgeColor = "azure"
	BadgePurple      BadgeColor = "purple"
	BadgeOrange      BadgeColor = "orange"
	BadgeYellow      BadgeColor = "yellow"
	BadgeRed         BadgeColor = "red"
	BadgeDestructive BadgeColor = "destructive"
	BadgeOutline     BadgeColor = "outline"
	BadgeSecondary   BadgeColor = "secondary"
)

// badgeColors lists what badgeHTML renders. A colour outside it falls back to
// secondary, silently at render time — which is why Verify reports it instead.
var badgeColors = map[BadgeColor]bool{
	BadgeGreen: true, BadgeBlue: true, BadgeAzure: true, BadgePurple: true,
	BadgeOrange: true, BadgeYellow: true, BadgeRed: true,
	BadgeDestructive: true, BadgeOutline: true, BadgeSecondary: true,
}

// badgeColorNames lists the known colours, sorted, for an error that says what
// was allowed.
func badgeColorNames() []string {
	out := make([]string, 0, len(badgeColors))
	for c := range badgeColors {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}

// labeledValue separates the value a badge is coloured by from the text it
// shows, for the case where Using has already replaced one with the other.
type labeledValue struct {
	key  any
	text string
}

func badgeHTML(colors map[any]BadgeColor, v any) template.HTML {
	if lv, ok := v.(labeledValue); ok {
		return badgeLabeledHTML(colors, lv.key, lv.text)
	}
	return badgeLabeledHTML(colors, v, fmt.Sprint(v))
}

// badgeLabeledHTML colours by the stored value and shows text. The colour map
// may be keyed either way: on the stored value, or on the text Using replaced
// it with.
func badgeLabeledHTML(colors map[any]BadgeColor, key any, text string) template.HTML {
	color, ok := colors[key]
	if !ok {
		color, ok = colors[fmt.Sprint(key)]
	}
	if !ok {
		color = colors[text]
	}
	label := template.HTMLEscapeString(text)
	palette := map[BadgeColor]string{
		"green":  "bg-green-50 text-green-700 dark:bg-green-950 dark:text-green-300",
		"blue":   "bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300",
		"azure":  "bg-sky-50 text-sky-700 dark:bg-sky-950 dark:text-sky-300",
		"purple": "bg-purple-50 text-purple-700 dark:bg-purple-950 dark:text-purple-300",
		"orange": "bg-orange-50 text-orange-700 dark:bg-orange-950 dark:text-orange-300",
		"yellow": "bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300",
	}
	if cls, ok := palette[color]; ok {
		return template.HTML(`<span class="badge ` + cls + `">` + label + `</span>`)
	}
	switch color {
	case "red", "destructive":
		return template.HTML(`<span class="badge" data-variant="destructive">` + label + `</span>`)
	case "outline":
		return template.HTML(`<span class="badge" data-variant="outline">` + label + `</span>`)
	default:
		return template.HTML(`<span class="badge" data-variant="secondary">` + label + `</span>`)
	}
}

// statusHTML renders a labeled status dot.
func statusHTML(ok bool, yes, no string) template.HTML {
	if ok {
		return template.HTML(`<span class="inline-flex items-center gap-1.5"><span class="size-2 rounded-full bg-green-500"></span>` +
			template.HTMLEscapeString(yes) + `</span>`)
	}
	return template.HTML(`<span class="inline-flex items-center gap-1.5 text-muted-foreground"><span class="size-2 rounded-full bg-muted-foreground/40"></span>` +
		template.HTMLEscapeString(no) + `</span>`)
}

// Badge renders the value as a colored badge; keys are raw values (or
// their fmt representation), values are color names ("green", "blue",
// "azure", "purple", "orange", "yellow", "red", "secondary", "outline").
func (c *Column[T]) Badge(colors map[any]BadgeColor) *Column[T] {
	c.badges = colors
	c.present = func(v any, _ *T) template.HTML { return badgeHTML(colors, v) }
	return c
}

// Bool renders truthy/falsy values as a status. It says Yes and No unless
// given two words of its own: Bool("Ya", "Tidak").
func (c *Column[T]) Bool(labels ...string) *Column[T] {
	c.boolLabels = labels
	yes, no := boolWords(labels)
	c.present = func(v any, _ *T) template.HTML { return statusHTML(truthy(v), yes, no) }
	return c
}

// boolWords resolves Bool's optional labels, falling back to English. A wrong
// count is reported by Verify rather than guessed at here.
func boolWords(labels []string) (string, string) {
	if len(labels) == 2 {
		return labels[0], labels[1]
	}
	return "Yes", "No"
}

// Link renders the value as an anchor; href receives the typed row.
func (c *Column[T]) Link(href func(row *T) string) *Column[T] {
	c.present = func(v any, row *T) template.HTML {
		target := href(row)
		glyph := linkGlyph
		// A stored path opens a file; anything absolute goes somewhere else.
		if !absoluteRef(target) {
			glyph = fileGlyph
		}
		return template.HTML(markedLink(target, fmt.Sprint(v), glyph, true)) //nolint:gosec // markedLink escapes both
	}
	return c
}

// Image renders the value (a URL or storage path) as a thumbnail. A
// storage-relative path resolves through the configured Storage; see
// Admin.StorageURL.
func (c *Column[T]) Image(width, height int) *Column[T] {
	c.storageRef = true
	c.present = func(v any, _ *T) template.HTML {
		_, s := refParts(v)
		if s == "" {
			return ""
		}
		style := ""
		if width > 0 {
			style += fmt.Sprintf("width:%dpx;", width)
		}
		if height > 0 {
			style += fmt.Sprintf("height:%dpx;", height)
		}
		return previewable(s, fmt.Sprintf(`<img src="%s" class="rounded" style="%s object-fit:cover" alt=""/>`,
			template.HTMLEscapeString(s), style))
	}
	return c
}

// previewable wraps rendered markup in the trigger that opens it full size, so
// a thumbnail sized for a row is not the only look a reader gets at it.
func previewable(href, inner string) template.HTML {
	return Preview(href, template.HTML(inner)) //nolint:gosec // callers pass markup they built
}

// Preview wraps markup in the trigger that opens href in the panel's viewer —
// images and PDFs inline, anything else as a link. Image columns and detail
// fields do this already; this is for a cell built by hand.
//
//	g.ColumnFunc("photo", "Photo", func(r *Row) template.HTML {
//	    url := app.DiskURL("", r.Photo)
//	    return steward.Preview(url, template.HTML(`<img src="`+url+`" width="60"/>`))
//	})
func Preview(href string, inner template.HTML) template.HTML {
	if href == "" {
		return inner
	}
	return template.HTML(`<button type="button" class="steward-preview-trigger" ` + //nolint:gosec // inner is the caller's markup
		`data-steward-preview="` + template.HTMLEscapeString(href) + `" ` +
		`aria-label="Preview">` + string(inner) + `</button>`)
}

// copyGlyph is Lucide's "copy", inlined because a cell is rendered in Go rather
// than in a template and so has no {{icon}} to call. It must stay in step with
// the sprite the rest of the panel draws from.
const copyGlyph = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" ` +
	`viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ` +
	`stroke-linecap="round" stroke-linejoin="round" class="size-3">` +
	`<rect width="14" height="14" x="8" y="8" rx="2" ry="2"/>` +
	`<path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`

// linkGlyph and fileGlyph mark a value as somewhere to go or something to open,
// so a link is recognisable before it is hovered and a file is not mistaken for
// text. Inlined for the same reason copyGlyph is — a cell is rendered in Go and
// has no {{icon}} to call — and they must stay in step with the sprite.
const glyphOpen = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" ` +
	`viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ` +
	`stroke-linecap="round" stroke-linejoin="round" class="size-3 shrink-0 opacity-70" aria-hidden="true">`

const linkGlyph = glyphOpen +
	`<path d="M15 3h6v6"/><path d="M10 14 21 3"/>` +
	`<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/></svg>`

const fileGlyph = glyphOpen +
	`<path d="m16 6-8.414 8.586a2 2 0 0 0 2.829 2.829l8.414-8.586a4 4 0 1 0-5.657-5.657` +
	`l-8.379 8.551a6 6 0 1 0 8.485 8.485l8.379-8.551"/></svg>`

// markedLink wraps an anchor's contents with the glyph that says what following
// it does.
func markedLink(href, text, glyph string, newTab bool) string {
	target := ""
	if newTab {
		target = ` target="_blank" rel="noopener"`
	}
	return `<a href="` + template.HTMLEscapeString(href) + `"` + target +
		` class="inline-flex items-center gap-1.5">` + glyph +
		`<span class="truncate">` + template.HTMLEscapeString(text) + `</span></a>`
}

// Disk names which storage disk this column's stored paths live on, for the
// helpers that resolve one into a URL. Unset, the default disk is used.
func (c *Column[T]) Disk(name string) *Column[T] { c.disk = name; return c }

// Copyable adds a copy-to-clipboard affordance.
func (c *Column[T]) Copyable() *Column[T] {
	prev := c.present
	c.present = func(v any, row *T) template.HTML {
		// The stored value, not the resolved URL: what is worth copying off a
		// storage column is the path it holds.
		raw, _ := refParts(v)
		var inner template.HTML
		if prev != nil {
			inner = prev(v, row)
		} else {
			inner = template.HTML(template.HTMLEscapeString(raw))
		}
		if raw == "" {
			return inner
		}
		// A button rather than the cell itself: the cell carried no sign it
		// could be copied, and wrapping a link or a badge in a click handler
		// takes the click away from whatever is already inside it.
		return template.HTML(fmt.Sprintf(
			`<span class="steward-copy-cell">%s<button type="button" class="steward-copy" `+
				`data-steward-copy="%s" aria-label="Copy %s">%s</button></span>`,
			inner, template.HTMLEscapeString(raw),
			template.HTMLEscapeString(c.label), copyGlyph))
	}
	return c
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case nil:
		return false
	case string:
		return x != "" && x != "0" && x != "false"
	default:
		return fmt.Sprint(v) != "0"
	}
}

// Filters declares the filter panel inside Grid.Filter.
type Filters[T any] struct {
	grid *Grid[T]
}

type filterInput int

const (
	inputText filterInput = iota
	inputSelect
	inputDate
	inputDatetime
	inputBetween
	inputDateRange
)

// FilterItem is one filter control.
type FilterItem[T any] struct {
	path, label string
	op          Op
	input       filterInput
	options     Options
	optionsFn   func(*Context) Options
	placeholder string
	span        int
	// withTime carries times through a range's two ends, which also makes its
	// bounds exact rather than rounded out to whole days.
	withTime bool
	// datetimeOnBetween records the retired spelling so Verify can name its
	// replacement, rather than the filter quietly behaving as a numeric one.
	datetimeOnBetween bool
	info              *fieldInfo
}

// Span sets how many of the filter panel's twelve columns the control takes,
// the same twelve a form divides into. Values outside 1..12 are clamped.
//
//	f.Equal("Status").Span(3)
//	f.Between("PostDate").Datetime().Span(6)
//
// Unset, each kind takes a width that suits it: a range needs room for two
// controls, a switch needs almost none.
func (fi *FilterItem[T]) Span(n int) *FilterItem[T] {
	fi.span = min(max(n, 1), layoutColumns)
	return fi
}

// spanOr returns the declared span, or what the input kind is worth.
func (fi *FilterItem[T]) spanOr() int {
	if fi.span > 0 {
		return fi.span
	}
	switch fi.input {
	case inputBetween, inputDateRange:
		// Two controls, or a range picker with a written-out label.
		return 6
	case inputDate, inputDatetime:
		return 4
	default:
		return 3
	}
}

// dateLike reports whether the filter's values are dates, which decides how an
// upper bound is read.
func (fi *FilterItem[T]) dateLike() bool {
	switch fi.input {
	case inputDate, inputDatetime, inputDateRange:
		return true
	}
	return false
}

// choices resolves a filter's options, whichever way they were declared.
func (fi *FilterItem[T]) choices(c *Context) Options {
	if fi.optionsFn != nil {
		return fi.optionsFn(c)
	}
	return fi.options
}

// GridActionStyle selects how a row's actions are presented.
type GridActionStyle string

const (
	// GridActionsButtons lays the actions out side by side. Fastest to reach,
	// and the default, but it costs a column's width per action.
	GridActionsButtons GridActionStyle = "buttons"

	// GridActionsMenu collapses them behind a single trigger. Worth it once a
	// grid has several actions or many columns, at the price of one extra click.
	GridActionsMenu GridActionStyle = "menu"
)

// resolve returns the effective style, falling back to the panel-wide default
// and then to buttons.
func (s GridActionStyle) resolve(fallback GridActionStyle) GridActionStyle {
	if s == "" {
		s = fallback
	}
	if s == GridActionsMenu {
		return GridActionsMenu
	}
	return GridActionsButtons
}

// resolve settles a grid's layout against the panel-wide default.
func (l GridFilterLayout) resolve(fallback GridFilterLayout) GridFilterLayout {
	if l == "" {
		l = fallback
	}
	if l == FiltersDrawer {
		return FiltersDrawer
	}
	return FiltersAbove
}

// Options maps stored values to display labels for selects/radios.
type Options map[string]string

func (f *Filters[T]) add(path string, op Op, input filterInput, label ...string) *FilterItem[T] {
	it := &FilterItem[T]{path: path, op: op, input: input}
	if len(label) > 0 {
		it.label = label[0]
	}
	f.grid.filters = append(f.grid.filters, it)
	return it
}

// Equal filters path = value.
func (f *Filters[T]) Equal(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpEq, inputText, label...)
}

// Like filters path LIKE %value%.
func (f *Filters[T]) Like(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpLike, inputText, label...)
}

// Gt filters path > value.
func (f *Filters[T]) Gt(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpGt, inputText, label...)
}

// Lt filters path < value.
func (f *Filters[T]) Lt(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpLt, inputText, label...)
}

// In filters path IN (values) — pair with Select+multiple later milestones.
func (f *Filters[T]) In(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpIn, inputText, label...)
}

// Between filters lo ≤ path ≤ hi with two inputs.
func (f *Filters[T]) Between(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpBetween, inputBetween, label...)
}

// Date filters a date column by day.
func (f *Filters[T]) Date(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpEq, inputDate, label...)
}

// Select renders the filter as a dropdown of options.
func (fi *FilterItem[T]) Select(o Options) *FilterItem[T] {
	fi.input = inputSelect
	fi.options = o
	return fi
}

// SelectFunc renders the filter as a dropdown whose options are resolved per
// request. Select takes its map once, when the resource is registered, so a
// list read from the database there is both loaded at boot and never refreshed.
func (fi *FilterItem[T]) SelectFunc(fn func(*Context) Options) *FilterItem[T] {
	fi.input = inputSelect
	fi.optionsFn = fn
	return fi
}

// DateRange filters a date column by a range chosen in one calendar, rather
// than two separate inputs. It submits the same two parameters a Between filter
// does — {param} and {param}_to — so a half-open range still works and a URL
// written by hand behaves the same.
func (f *Filters[T]) DateRange(path string, label ...string) *FilterItem[T] {
	return f.add(path, OpBetween, inputDateRange, label...)
}

// Datetime switches a Between filter to date-range inputs.
// Datetime asks for a time alongside the date.
//
// On a DateRange filter both ends carry one, and the bounds are then exact
// rather than rounded out to whole days. On any other filter it becomes a
// single date-and-time picker.
//
// It is refused on Between, which is the numeric two-ended filter: a range of
// dates is DateRange, with or without times.
func (fi *FilterItem[T]) Datetime() *FilterItem[T] {
	switch fi.input {
	case inputDateRange:
		fi.withTime = true
	case inputBetween:
		fi.datetimeOnBetween = true
	default:
		fi.input = inputDatetime
	}
	return fi
}

// Placeholder sets the input placeholder.
func (fi *FilterItem[T]) Placeholder(s string) *FilterItem[T] {
	fi.placeholder = s
	return fi
}
