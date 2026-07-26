package steward

import (
	"fmt"
	"html/template"
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

	perPage        int
	perPageOptions []int
	defaultSort    *Sort

	off map[string]bool // feature switches (create, delete, filter, ...)
}

func newGrid[T any](res *Resource[T]) *Grid[T] {
	return &Grid[T]{res: res, perPage: 20, perPageOptions: []int{10, 20, 50, 100}, off: map[string]bool{}}
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

// Feature switches, dcat-style disable pairs.

// DisableCreate hides the create button.
func (g *Grid[T]) DisableCreate() *Grid[T] { g.off["create"] = true; return g }

// DisableDelete hides row delete actions and batch delete.
func (g *Grid[T]) DisableDelete() *Grid[T] { g.off["delete"] = true; return g }

// DisableEdit hides row edit actions.
func (g *Grid[T]) DisableEdit() *Grid[T] { g.off["edit"] = true; return g }

// DisableView hides the row detail action.
func (g *Grid[T]) DisableView() *Grid[T] { g.off["view"] = true; return g }

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

// Badge renders the value as a colored Tabler badge; keys are raw values
// (or their fmt representation), values are Tabler colors ("green",
// "secondary", "red", ...). Unmapped values fall back to "secondary".
func (c *Column[T]) Badge(colors map[any]string) *Column[T] {
	c.present = func(v any, _ *T) template.HTML {
		color, ok := colors[v]
		if !ok {
			color, ok = colors[fmt.Sprint(v)]
		}
		if !ok {
			color = "secondary"
		}
		return template.HTML(fmt.Sprintf(`<span class="badge bg-%s-lt">%s</span>`,
			template.HTMLEscapeString(color), template.HTMLEscapeString(fmt.Sprint(v))))
	}
	return c
}

// Bool renders ✓/✕ statuses for truthy/falsy values.
func (c *Column[T]) Bool() *Column[T] {
	c.present = func(v any, _ *T) template.HTML {
		if truthy(v) {
			return `<span class="status status-green">Yes</span>`
		}
		return `<span class="status status-secondary">No</span>`
	}
	return c
}

// Link renders the value as an anchor; href receives the typed row.
func (c *Column[T]) Link(href func(row *T) string) *Column[T] {
	c.present = func(v any, row *T) template.HTML {
		return template.HTML(fmt.Sprintf(`<a href="%s" target="_blank" rel="noopener">%s</a>`,
			template.HTMLEscapeString(href(row)), template.HTMLEscapeString(fmt.Sprint(v))))
	}
	return c
}

// Image renders the value (a URL or storage path) as a thumbnail.
func (c *Column[T]) Image(width, height int) *Column[T] {
	c.present = func(v any, _ *T) template.HTML {
		s := fmt.Sprint(v)
		if s == "" || v == nil {
			return ""
		}
		style := ""
		if width > 0 {
			style += fmt.Sprintf("width:%dpx;", width)
		}
		if height > 0 {
			style += fmt.Sprintf("height:%dpx;", height)
		}
		return template.HTML(fmt.Sprintf(`<img src="%s" class="rounded" style="%s object-fit:cover" alt=""/>`,
			template.HTMLEscapeString(s), style))
	}
	return c
}

// Copyable adds a copy-to-clipboard affordance.
func (c *Column[T]) Copyable() *Column[T] {
	prev := c.present
	c.present = func(v any, row *T) template.HTML {
		var inner template.HTML
		if prev != nil {
			inner = prev(v, row)
		} else {
			inner = template.HTML(template.HTMLEscapeString(fmt.Sprint(v)))
		}
		return template.HTML(fmt.Sprintf(
			`<span class="steward-copy" data-steward-copy="%s">%s</span>`,
			template.HTMLEscapeString(fmt.Sprint(v)), inner))
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
	inputDateBetween
)

// FilterItem is one filter control.
type FilterItem[T any] struct {
	path, label string
	op          Op
	input       filterInput
	options     Options
	placeholder string
	info        *fieldInfo
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

// Datetime switches a Between filter to date-range inputs.
func (fi *FilterItem[T]) Datetime() *FilterItem[T] {
	if fi.input == inputBetween {
		fi.input = inputDateBetween
	} else {
		fi.input = inputDatetime
	}
	return fi
}

// Placeholder sets the input placeholder.
func (fi *FilterItem[T]) Placeholder(s string) *FilterItem[T] {
	fi.placeholder = s
	return fi
}
