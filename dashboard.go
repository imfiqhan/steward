package steward

// The dashboard builder: widgets declared in Go, each with a typed data
// callback, replacing a hand-written home page.
//
// Tiles declared one after another flow into the dashboard's own grid, three
// columns wide. Row places them explicitly instead, taking the same Col that
// composes a custom page — a tile is a Node, so the two vocabularies are one.

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
)

// chartRuntimeAssets are what a chart tile needs in the page. They are fetched
// by `make vendor-chart` rather than committed: Chart.js is Basecoat's peer
// dependency and is not redistributed here.
var chartRuntimeAssets = []string{"dist/chart.umd.min.js"}

// hasChartWidget reports whether any tile needs that runtime.
func (d *Dashboard) hasChartWidget() bool {
	for _, w := range d.allWidgets() {
		if w.kind == widgetChart {
			return true
		}
	}
	return false
}

type widgetKind int

const (
	widgetMetric widgetKind = iota
	widgetTemplate
	widgetChart
)

// maxWidgetSpan matches the dashboard grid's column count.
const maxWidgetSpan = 3

// Widget is one dashboard tile. Methods chain.
type Widget struct {
	kind  widgetKind
	title string
	hint  string
	span  int
	tmpl  string
	icon  string
	tone  BadgeColor
	lazy  bool
	load  func(*Context) (any, error)
}

// Span sets how many of the grid's three columns the widget occupies.
// Values outside 1..3 are clamped.
func (w *Widget) Span(n int) *Widget {
	w.span = min(max(n, 1), maxWidgetSpan)
	return w
}

// Hint adds secondary text under a metric's value.
func (w *Widget) Hint(s string) *Widget { w.hint = s; return w }

// Icon draws a Lucide glyph beside a metric's figure, in the tile's colour.
// The name is checked at boot.
func (w *Widget) Icon(name string) *Widget { w.icon = canonicalIconName(name); return w }

// Color tints a metric tile and its icon, from the panel's colour vocabulary —
// the same one badges use. An unknown colour is reported by Verify.
func (w *Widget) Color(c BadgeColor) *Widget { w.tone = c; return w }

// Lazy defers the widget's data callback to a follow-up request, so a slow
// aggregate does not hold up the page. The tile renders a skeleton and swaps
// itself once the fragment arrives.
func (w *Widget) Lazy() *Widget { w.lazy = true; return w }

// Chart adds a chart tile drawn by Basecoat's Chart component. load returns the
// series to plot; see ChartData. Defaults to spanning two columns, since a
// chart squeezed into one is rarely readable.
//
// Requires the chart assets: run `make vendor-chart` once, then `make assets`.
// Without them the tile explains itself rather than rendering blank.
func (d *Dashboard) Chart(title string, load func(*Context) (*ChartData, error)) *Widget {
	w := &Widget{kind: widgetChart, title: title, span: 2}
	w.load = func(c *Context) (any, error) { return load(c) }
	return d.add(w)
}

// Dashboard collects the widgets shown on the panel's home page.
type Dashboard struct {
	// nodes holds tiles and rows in the order they were declared. A run of
	// tiles renders as the dashboard's own grid; a row renders as itself, so
	// where a row sits among the tiles is where it appears.
	nodes []Node
}

// add appends a tile in declaration order.
func (d *Dashboard) add(w *Widget) *Widget {
	d.nodes = append(d.nodes, w)
	return w
}

// Row arranges tiles explicitly rather than letting them flow into the
// dashboard's three-column grid, and is the same Row a custom page uses:
//
//	app.Dashboard(func(d *steward.Dashboard) {
//	    d.Row(
//	        steward.Col(8, d.Chart("Trend", trend)),
//	        steward.Col(4,
//	            d.Metric("This year", countThisYear),
//	            d.Metric("This month", countThisMonth),
//	        ),
//	    )
//	})
//
// A tile placed in a row is not also flowed into the grid.
func (d *Dashboard) Row(cols ...Node) *Dashboard {
	row := Row(cols...)
	// A tile constructor appends as it is called, so one placed in a row was
	// already in the flow — it is taken out and the row stands where the first
	// of them was declared.
	placed := map[*Widget]bool{}
	for _, w := range tiles([]Node{row}) {
		placed[w] = true
	}
	kept := d.nodes[:0]
	inserted := false
	for _, n := range d.nodes {
		sp := n.spec()
		if sp.kind == "tile" && sp.tile != nil && placed[sp.tile] {
			if !inserted {
				kept = append(kept, row)
				inserted = true
			}
			continue
		}
		kept = append(kept, n)
	}
	if !inserted {
		kept = append(kept, row)
	}
	d.nodes = kept
	return d
}

// Metric adds a KPI tile. load runs per request and its result is stringified
// by the template, so returning an int, string, or fmt.Stringer all work.
func (d *Dashboard) Metric(label string, load func(*Context) (any, error)) *Widget {
	w := &Widget{kind: widgetMetric, title: label, span: 1, load: load}
	return d.add(w)
}

// Template adds a tile rendered from a template of your own, receiving
// whatever load returns as its data. Pass a nil load for a static tile.
func (d *Dashboard) Template(title, tmpl string, load func(*Context) (any, error)) *Widget {
	w := &Widget{kind: widgetTemplate, title: title, tmpl: tmpl, span: maxWidgetSpan, load: load}
	return d.add(w)
}

// Dashboard replaces the default home page with widgets declared in Go:
//
//	app.Dashboard(func(d *steward.Dashboard) {
//	    d.Metric("Users", countUsers).Span(1).Hint("all time")
//	    d.Template("Recent", "widgets/recent.html", recentRows).Span(2).Lazy()
//	})
//
// Call it before Build. Without it the built-in overview page is served.
func (a *Admin) Dashboard(fn func(*Dashboard)) *Admin {
	d := &Dashboard{}
	fn(d)
	a.dash = d
	return a
}

// allWidgets lists every tile the dashboard holds, flowed or placed, in a
// stable order: a lazy tile is fetched by its position in this list.
func (d *Dashboard) allWidgets() []*Widget { return tiles(d.nodes) }

// ---- rendering --------------------------------------------------------------

type widgetVM struct {
	Index int
	Kind  string
	Title string
	Hint  string
	Span  int
	Icon  string
	Tone  string
	Value string
	Body  template.HTML
	// Payload is a chart's JSON, embedded for the client initializer.
	Payload template.JS
	// LazyURL is set when the widget defers its load; the tile fetches it.
	LazyURL string
	// Err carries a load failure. One broken widget must not blank the page,
	// so the tile reports it in place and the rest still render.
	Err string
	// Empty carries a "nothing to show" note, distinct from Err: a chart over an
	// empty table is a normal state and must not read as a broken query.
	Empty string
}

// resolve runs a widget's callback and renders its body.
func (a *Admin) resolve(c *Context, w *Widget, i int) widgetVM {
	vm := widgetVM{Index: i, Title: w.title, Hint: w.hint, Span: w.span,
		Icon: w.icon, Tone: string(w.tone)}
	switch w.kind {
	case widgetMetric:
		vm.Kind = "metric"
	case widgetChart:
		vm.Kind = "chart"
	default:
		vm.Kind = "template"
	}

	var data any
	if w.load != nil {
		v, err := w.load(c)
		if err != nil {
			a.log.Error("steward: dashboard widget", "title", w.title, "err", err)
			vm.Err = "Could not load."
			return vm
		}
		data = v
	}

	if w.kind == widgetMetric {
		vm.Value = fmt.Sprint(data)
		return vm
	}

	if w.kind == widgetChart {
		cd, ok := data.(*ChartData)
		if !ok || cd == nil {
			vm.Err = "Could not load."
			return vm
		}
		raw, err := cd.json()
		if errors.Is(err, errChartNoData) {
			// Nothing to plot is a state, not a fault — say so plainly instead
			// of implying the query broke.
			vm.Empty = "No data yet."
			return vm
		}
		if err != nil {
			// A malformed chart is the caller's bug, so name it in the log.
			a.log.Error("steward: dashboard chart", "title", w.title, "err", err)
			vm.Err = "Could not load."
			return vm
		}
		vm.Payload = template.JS(raw)
		return vm
	}

	var buf bytes.Buffer
	if err := a.renderer.execute(&buf, w.tmpl, a.pageMetaFor(c, w.title), data); err != nil {
		a.log.Error("steward: dashboard widget template", "template", w.tmpl, "err", err)
		vm.Err = "Could not render."
		return vm
	}
	vm.Body = template.HTML(buf.String())
	return vm
}

// dashboardVM is the page payload.
// dashBlock is one stretch of the page: either a run of flowed tiles or one
// explicitly placed row.
type dashBlock struct {
	Tiles []widgetVM
	Row   []layoutNodeVM
}

type dashboardVM struct {
	Blocks []dashBlock
	// HasChart pulls the chart runtime into the page, once, only when needed.
	HasChart bool
	// ResourceCount keeps the built-in overview page working unchanged.
	ResourceCount int
}

// dashboard serves the home page: declared widgets when Admin.Dashboard was
// called, otherwise the built-in overview.
func (a *Admin) dashboard(c *Context) error {
	if a.dash == nil {
		return a.render(c, "pages/dashboard.html", "Dashboard", dashboardVM{
			ResourceCount: len(a.registry),
		})
	}
	vm := dashboardVM{HasChart: a.dash.hasChartWidget()}
	// The index is what a lazy tile fetches itself by, so it has to identify the
	// widget wherever it sits — in the flow or in a row.
	index := map[*Widget]int{}
	for i, w := range a.dash.allWidgets() {
		index[w] = i
	}
	tile := func(w *Widget) *widgetVM {
		i := index[w]
		if w.lazy {
			return &widgetVM{
				Index: i, Kind: "lazy", Title: w.title, Span: w.span,
				LazyURL: a.url("_widget", strconv.Itoa(i)),
			}
		}
		vm := a.resolve(c, w, i)
		return &vm
	}
	// Runs of tiles keep the dashboard's own grid; a row breaks the run, so
	// what was declared between two rows stays between them.
	var group []widgetVM
	flush := func() {
		if len(group) > 0 {
			vm.Blocks = append(vm.Blocks, dashBlock{Tiles: group})
			group = nil
		}
	}
	for _, n := range a.dash.nodes {
		sp := n.spec()
		if sp.kind == "tile" && sp.tile != nil {
			group = append(group, *tile(sp.tile))
			continue
		}
		flush()
		vm.Blocks = append(vm.Blocks, dashBlock{Row: viewNodes([]Node{n}, tile)})
	}
	flush()
	return a.render(c, "pages/widgets.html", "Dashboard", vm)
}

// widgetFragment serves one lazy widget's tile. Reached only through the
// standard middleware chain, so auth and permissions already applied.
func (a *Admin) widgetFragment(c *Context) error {
	if a.dash == nil {
		return c.JSON(http.StatusNotFound, Error("No dashboard widgets are declared."))
	}
	// Numbered over every tile, flowed or placed in a row, which is the order
	// the page hands out.
	all := a.dash.allWidgets()
	i, err := strconv.Atoi(c.R.PathValue("index"))
	if err != nil || i < 0 || i >= len(all) {
		return c.JSON(http.StatusNotFound, Error("Unknown widget."))
	}
	w := all[i]
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.Header().Set("Cache-Control", "no-store")
	return a.renderer.execute(c.W, "widgets/tile_fragment.html", a.pageMetaFor(c, w.title), a.resolve(c, w, i))
}
