package steward

// Page composition: rows, columns, and the widgets that sit in them.
//
// A handler builds a tree and hands it back; nothing here queries anything. The
// handler already has a Context and is the right place to fetch, so a widget
// takes the value it displays rather than a callback that produces one. That is
// what separates this from the dashboard's tiles, which exist to defer slow
// aggregates and so must carry loaders.

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
)

// layoutColumns is the grid every row divides into, matching the form's.
const layoutColumns = 12

// Node is one element of a page layout: a row, a column, or something to show.
// The interface is closed — the constructors in this file are the only
// implementations — so a layout is always a shape the renderer understands.
type Node interface{ spec() *nodeSpec }

type nodeSpec struct {
	kind     string // "row" | "col" | "card" | "html" | "tile"
	span     int    // col only
	title    string
	children []Node
	body     template.HTML
	value    string
	hint     string
	icon     string
	tone     BadgeColor
	// tile is a dashboard widget placed in a layout. It carries a loader
	// rather than a value, so it is resolved per request — which is what lets
	// a slow aggregate stay Lazy inside a row.
	tile *Widget
}

func (n *nodeSpec) spec() *nodeSpec { return n }

// Row places its columns side by side. Columns whose spans exceed twelve wrap
// onto the next line, and below the small breakpoint each takes the full width.
//
//	steward.Row(
//	    steward.Col(8, steward.Card("Trend", chart)),
//	    steward.Col(4, steward.Card("Totals", totals)),
//	)
func Row(cols ...Node) Node {
	return &nodeSpec{kind: "row", children: cols}
}

// Col occupies span of the row's twelve columns and stacks its children.
// A span outside 1..12 is clamped.
func Col(span int, children ...Node) Node {
	return &nodeSpec{kind: "col", span: min(max(span, 1), layoutColumns), children: children}
}

// Card wraps its children in the panel's card, with an optional heading. An
// empty title renders the card without a header.
func Card(title string, children ...Node) Node {
	return &nodeSpec{kind: "card", title: title, children: children}
}

// Text renders escaped body text.
func Text(s string) Node {
	return &nodeSpec{kind: "html", body: template.HTML(`<p class="text-sm text-muted-foreground">` + //nolint:gosec // escaped on the next line
		template.HTMLEscapeString(s) + `</p>`)}
}

// Heading renders a section heading above whatever follows it.
func Heading(s string) Node {
	return &nodeSpec{kind: "html", body: template.HTML(`<h3 class="text-base font-semibold">` + //nolint:gosec // escaped on the next line
		template.HTMLEscapeString(s) + `</h3>`)}
}

// Markup places markup you have already built. It is not sanitized: pass
// template.HTML you produced, not a value that came from a request.
func Markup(h template.HTML) Node {
	return &nodeSpec{kind: "html", body: h}
}

// Divider draws a rule between sections.
func Divider() Node {
	return &nodeSpec{kind: "html", body: `<hr class="border-border"/>`}
}

// MetricNode is one figure and its label. Icon and Color chain off Metric.
type MetricNode struct{ s *nodeSpec }

func (m *MetricNode) spec() *nodeSpec { return m.s }

// Icon draws a Lucide glyph beside the figure, in the tile's colour. The name
// is checked at boot, so a glyph that does not exist is a build error rather
// than an empty square.
func (m *MetricNode) Icon(name string) *MetricNode {
	m.s.icon = canonicalIconName(name)
	return m
}

// Color tints the card and the icon. It takes the panel's colour vocabulary,
// the same one badges use, and an unknown colour is reported by Verify.
func (m *MetricNode) Color(c BadgeColor) *MetricNode {
	m.s.tone = c
	return m
}

// Metric shows one figure with its label, the same tile the dashboard uses.
// hint is optional secondary text beneath the value.
//
//	steward.Metric("Published", 1752, "live on the site").
//	    Icon("newspaper").Color(steward.BadgeGreen)
func Metric(label string, value any, hint ...string) *MetricNode {
	n := &nodeSpec{kind: "metric", title: label, value: fmt.Sprint(value)}
	if len(hint) > 0 {
		n.hint = hint[0]
	}
	return &MetricNode{s: n}
}

// Table renders rows under headers. Values are escaped, so a cell holding
// markup is shown as text; build the markup with Markup for a cell that should
// render.
func Table(headers []string, rows [][]any) Node {
	var b strings.Builder
	b.WriteString(`<div class="overflow-x-auto"><table class="table"><thead><tr>`)
	for _, h := range headers {
		b.WriteString(`<th>` + template.HTMLEscapeString(h) + `</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, row := range rows {
		b.WriteString(`<tr>`)
		for _, cell := range row {
			b.WriteString(`<td>`)
			if h, ok := cell.(template.HTML); ok {
				b.WriteString(string(h))
			} else {
				b.WriteString(template.HTMLEscapeString(fmt.Sprint(cell)))
			}
			b.WriteString(`</td>`)
		}
		b.WriteString(`</tr>`)
	}
	if len(rows) == 0 {
		b.WriteString(`<tr><td colspan="` + fmt.Sprint(max(len(headers), 1)) +
			`" class="text-sm text-muted-foreground">Nothing to show.</td></tr>`)
	}
	b.WriteString(`</tbody></table></div>`)
	return &nodeSpec{kind: "html", body: template.HTML(b.String())} //nolint:gosec // cells are escaped unless the caller passed template.HTML
}

// Chart draws a series with the same component the dashboard's chart tiles use,
// and needs the same runtime in the page — Context.Layout includes it whenever
// the tree holds one.
func Chart(data *ChartData) Node {
	raw, err := data.json()
	if errors.Is(err, errChartNoData) {
		// Nothing to plot is a state, not a fault.
		return Text("No data yet.")
	}
	if err != nil {
		return &nodeSpec{kind: "html", body: template.HTML(`<p class="text-sm text-destructive">` + //nolint:gosec // escaped on the next line
			template.HTMLEscapeString(err.Error()) + `</p>`)}
	}
	return &nodeSpec{kind: "html", body: template.HTML(`<div data-steward-chart>` + //nolint:gosec // payload is JSON produced above
		`<canvas></canvas>` +
		`<p class="text-sm text-muted-foreground" data-steward-chart-note>Chart runtime not loaded.</p>` +
		`<script type="application/json" data-steward-chart-data>` + string(raw) + `</script>` +
		`</div>`)}
}

// spec makes a dashboard widget usable wherever a Node is.
func (w *Widget) spec() *nodeSpec { return &nodeSpec{kind: "tile", tile: w} }

// tiles collects the widgets a tree holds, in order, so the dashboard can
// resolve each one and hand the results back for rendering.
func tiles(nodes []Node) []*Widget {
	var out []*Widget
	for _, n := range nodes {
		s := n.spec()
		if s.kind == "tile" && s.tile != nil {
			out = append(out, s.tile)
		}
		out = append(out, tiles(s.children)...)
	}
	return out
}

// hasChart reports whether the tree holds a chart, so the page can ship the
// runtime only where one is used.
func hasChart(nodes []Node) bool {
	for _, n := range nodes {
		s := n.spec()
		if s.kind == "html" && strings.Contains(string(s.body), "data-steward-chart") {
			return true
		}
		if s.kind == "tile" && s.tile != nil && s.tile.kind == widgetChart {
			return true
		}
		if hasChart(s.children) {
			return true
		}
	}
	return false
}

// layoutNodeVM is the tree a template can walk: Node's own accessor is
// unexported, and templates only reach exported names.
type layoutNodeVM struct {
	Kind     string
	Span     int
	Title    string
	Body     template.HTML
	Value    string
	Hint     string
	Icon     string
	Tone     string
	Children []layoutNodeVM
	// Tile is set on a widget node, already resolved.
	Tile *widgetVM
}

// viewNodes converts the tree for the template. resolve turns a widget into its
// tile; it is nil where no dashboard is involved, and a widget node then
// renders as nothing rather than as a broken tile.
func viewNodes(nodes []Node, resolve func(*Widget) *widgetVM) []layoutNodeVM {
	out := make([]layoutNodeVM, 0, len(nodes))
	for _, n := range nodes {
		s := n.spec()
		vm := layoutNodeVM{
			Kind: s.kind, Span: s.span, Title: s.title, Body: s.body,
			Value: s.value, Hint: s.hint, Icon: s.icon, Tone: string(s.tone),
			Children: viewNodes(s.children, resolve),
		}
		if s.kind == "tile" && resolve != nil && s.tile != nil {
			vm.Tile = resolve(s.tile)
		}
		out = append(out, vm)
	}
	return out
}

// layoutVM is what the page template renders.
type layoutVM struct {
	Nodes    []layoutNodeVM
	HasChart bool
}

// Layout renders a tree of rows and columns as a page, with the panel's chrome
// around it.
//
//	return c.Layout("Reports",
//	    steward.Row(
//	        steward.Col(8, steward.Card("Trend", steward.Chart(data))),
//	        steward.Col(4, steward.Metric("This year", 1752, "published")),
//	    ),
//	)
func (c *Context) Layout(title string, nodes ...Node) error {
	return c.Admin.render(c, "pages/layout.html", title, &layoutVM{
		Nodes:    viewNodes(nodes, nil),
		HasChart: hasChart(nodes),
	})
}

// verifyNodes reports icons and colours a layout names that do not exist. A
// layout is built per request, so this covers the ones declared on a dashboard,
// where the tree is known at boot.
func verifyNodes(a *Admin, where string, nodes []Node) {
	for _, n := range nodes {
		s := n.spec()
		if s.icon != "" && a.renderer != nil && !a.renderer.hasIcon(s.icon) {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"%s: icon %q not found; available: %s",
				where, s.icon, strings.Join(a.renderer.iconNames(), ", ")))
		}
		if s.tone != "" && !badgeColors[s.tone] {
			a.verifyErrs = append(a.verifyErrs, fmt.Errorf(
				"%s: unknown colour %q (known colours: %s)",
				where, s.tone, strings.Join(badgeColorNames(), ", ")))
		}
		verifyNodes(a, where, s.children)
	}
}
