package steward

// The dashboard builder: widgets declared in Go, each with a typed data
// callback, replacing a hand-written home page.
//
// Composition stops here deliberately. There is no Row/Column/Layout object
// graph — a widget declares what it shows and how wide it is, and the template
// arranges them. Anything more elaborate is a template override, where Tailwind
// and the overlay already do the job.

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
)

type widgetKind int

const (
	widgetMetric widgetKind = iota
	widgetTemplate
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

// Lazy defers the widget's data callback to a follow-up request, so a slow
// aggregate does not hold up the page. The tile renders a skeleton and swaps
// itself once the fragment arrives.
func (w *Widget) Lazy() *Widget { w.lazy = true; return w }

// Dashboard collects the widgets shown on the panel's home page.
type Dashboard struct {
	widgets []*Widget
}

// Metric adds a KPI tile. load runs per request and its result is stringified
// by the template, so returning an int, string, or fmt.Stringer all work.
func (d *Dashboard) Metric(label string, load func(*Context) (any, error)) *Widget {
	w := &Widget{kind: widgetMetric, title: label, span: 1, load: load}
	d.widgets = append(d.widgets, w)
	return w
}

// Template adds a tile rendered from a template of your own, receiving
// whatever load returns as its data. Pass a nil load for a static tile.
func (d *Dashboard) Template(title, tmpl string, load func(*Context) (any, error)) *Widget {
	w := &Widget{kind: widgetTemplate, title: title, tmpl: tmpl, span: maxWidgetSpan, load: load}
	d.widgets = append(d.widgets, w)
	return w
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

// ---- rendering --------------------------------------------------------------

type widgetVM struct {
	Index int
	Kind  string
	Title string
	Hint  string
	Span  int
	Value string
	Body  template.HTML
	// LazyURL is set when the widget defers its load; the tile fetches it.
	LazyURL string
	// Err carries a load failure. One broken widget must not blank the page,
	// so the tile reports it in place and the rest still render.
	Err string
}

// resolve runs a widget's callback and renders its body.
func (a *Admin) resolve(c *Context, w *Widget, i int) widgetVM {
	vm := widgetVM{Index: i, Title: w.title, Hint: w.hint, Span: w.span}
	switch w.kind {
	case widgetMetric:
		vm.Kind = "metric"
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
type dashboardVM struct {
	Widgets []widgetVM
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
	vm := dashboardVM{Widgets: make([]widgetVM, 0, len(a.dash.widgets))}
	for i, w := range a.dash.widgets {
		if w.lazy {
			vm.Widgets = append(vm.Widgets, widgetVM{
				Index: i, Kind: "lazy", Title: w.title, Span: w.span,
				LazyURL: a.url("_widget", strconv.Itoa(i)),
			})
			continue
		}
		vm.Widgets = append(vm.Widgets, a.resolve(c, w, i))
	}
	return a.render(c, "pages/widgets.html", "Dashboard", vm)
}

// widgetFragment serves one lazy widget's tile. Reached only through the
// standard middleware chain, so auth and permissions already applied.
func (a *Admin) widgetFragment(c *Context) error {
	if a.dash == nil {
		return c.JSON(http.StatusNotFound, Error("No dashboard widgets are declared."))
	}
	i, err := strconv.Atoi(c.R.PathValue("index"))
	if err != nil || i < 0 || i >= len(a.dash.widgets) {
		return c.JSON(http.StatusNotFound, Error("Unknown widget."))
	}
	w := a.dash.widgets[i]
	c.W.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.W.Header().Set("Cache-Control", "no-store")
	return a.renderer.execute(c.W, "widgets/tile_fragment.html", a.pageMetaFor(c, w.title), a.resolve(c, w, i))
}
