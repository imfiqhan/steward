package main

import (
	"html/template"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// A custom page had no way to be composed in Go: it rendered a template the
// application had to author. Row and Col build the page from the handler.

type pageLayoutRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

func newPageLayoutServer(t *testing.T, build func(c *steward.Context) error) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&pageLayoutRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("layout-page-test-secret-key"),
		AuthExcept: []string{"/page_layout_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[pageLayoutRow](app).Page("GET", "report", build)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

func TestLayoutComposesRowsAndColumns(t *testing.T) {
	srv := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Report",
			steward.Row(
				steward.Col(8, steward.Card("Trend", steward.Text("a line here"))),
				steward.Col(4,
					steward.Metric("This year", 1752, "published"),
					steward.Metric("This month", 97),
				),
			),
			steward.Row(
				steward.Col(12, steward.Card("Latest",
					steward.Table([]string{"Title", "Author"}, [][]any{
						{"A headline", "Ada"},
						{"Another", "Grace"},
					}))),
			),
		)
	})
	html := fetchOK(t, srv.URL+"/admin/page_layout_rows/report")

	for _, want := range []string{
		"steward-layout-row", "steward-span-8", "steward-span-4", "steward-span-12",
		">Trend<", ">Latest<", "a line here", "1752", "published", "A headline", "Grace",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page should contain %q", want)
		}
	}
	// Two rows, and the spans inside the first one.
	if n := strings.Count(html, "steward-layout-row"); n != 2 {
		t.Errorf("expected two rows, got %d", n)
	}
	if !strings.Contains(html, "Report") {
		t.Error("the page should carry its title")
	}
}

// TestLayoutNests covers the part a flat widget list cannot express: a column
// holding rows of its own.
func TestLayoutNests(t *testing.T) {
	srv := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Nested",
			steward.Row(
				steward.Col(6,
					steward.Row(
						steward.Col(6, steward.Metric("A", 1)),
						steward.Col(6, steward.Metric("B", 2)),
					),
				),
				steward.Col(6, steward.Card("Side", steward.Text("beside them"))),
			),
		)
	})
	html := fetchOK(t, srv.URL+"/admin/page_layout_rows/report")
	if n := strings.Count(html, "steward-layout-row"); n != 2 {
		t.Errorf("a row inside a column should render as a row of its own, got %d", n)
	}
	if !strings.Contains(html, ">A<") || !strings.Contains(html, ">B<") {
		t.Error("the nested metrics should render")
	}
}

// TestLayoutEscapesValues keeps a value out of the markup: a table cell and a
// text node are content, not HTML.
func TestLayoutEscapesValues(t *testing.T) {
	srv := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Escaping",
			steward.Text(`<script>alert(1)</script>`),
			steward.Table([]string{`<b>H</b>`}, [][]any{
				{`<img src=x onerror=alert(1)>`},
				{template.HTML(`<em>deliberate</em>`)},
			}),
			steward.Metric(`<b>label</b>`, `<b>value</b>`),
		)
	})
	html := fetchOK(t, srv.URL+"/admin/page_layout_rows/report")
	// The tag itself is what must not survive; the text inside it is inert once
	// the angle brackets are entities.
	for _, bad := range []string{"<script>alert(1)", "<img src=x", "<b>label</b>", "<b>H</b>"} {
		if strings.Contains(html, bad) {
			t.Errorf("%q should have been escaped", bad)
		}
	}
	if !strings.Contains(html, "&lt;img src=x") {
		t.Error("the escaped value should still be readable as text")
	}
	// Markup the caller built on purpose is left alone.
	if !strings.Contains(html, "<em>deliberate</em>") {
		t.Error("a template.HTML cell should render as markup")
	}
}

// TestLayoutShipsTheChartRuntimeOnlyWhenUsed covers the cost: Chart.js is
// larger than the whole UI bundle.
func TestLayoutShipsTheChartRuntimeOnlyWhenUsed(t *testing.T) {
	withChart := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Chart",
			steward.Card("Trend", steward.Chart(&steward.ChartData{
				Type:   steward.ChartLine,
				Labels: []string{"Jan", "Feb"},
				Series: []steward.ChartSeries{{Label: "Posts", Values: []float64{1, 2}}},
			})),
		)
	})
	html := fetchOK(t, withChart.URL+"/admin/page_layout_rows/report")
	if !strings.Contains(html, "data-steward-chart") {
		t.Error("the chart should render")
	}
	if !strings.Contains(html, "chart.umd.min.js") {
		t.Error("a page with a chart needs the runtime")
	}

	plain := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Plain", steward.Text("nothing to plot"))
	})
	if strings.Contains(fetchOK(t, plain.URL+"/admin/page_layout_rows/report"), "chart.umd.min.js") {
		t.Error("a page without a chart should not ship Chart.js")
	}
}

// TestLayoutChartWithoutData says so rather than drawing empty axes.
func TestLayoutChartWithoutData(t *testing.T) {
	srv := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Empty", steward.Chart(&steward.ChartData{Type: steward.ChartLine}))
	})
	html := fetchOK(t, srv.URL+"/admin/page_layout_rows/report")
	if !strings.Contains(html, "No data yet.") {
		t.Error("an empty series should say so")
	}
	if strings.Contains(html, "data-steward-chart-data") {
		t.Error("an empty series should not draw a canvas")
	}
}

// A dashboard tile is a Node, so the same Row and Col that compose a page
// arrange the dashboard — including the part a flat list cannot express, a
// column holding two tiles beside a taller one.
func TestDashboardRowsPlaceTiles(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&pageLayoutRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("dashboard-rows-test-secret-key"),
		AuthExcept: []string{"/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Dashboard(func(d *steward.Dashboard) {
		d.Metric("Flowed", func(c *steward.Context) (any, error) { return 7, nil })
		d.Row(
			steward.Col(8, d.Chart("Trend", func(c *steward.Context) (*steward.ChartData, error) {
				return &steward.ChartData{
					Type: steward.ChartLine, Labels: []string{"Jan", "Feb"},
					Series: []steward.ChartSeries{{Label: "Posts", Values: []float64{1, 2}}},
				}, nil
			})),
			steward.Col(4,
				d.Metric("Placed one", func(c *steward.Context) (any, error) { return 11, nil }),
				d.Metric("Placed two", func(c *steward.Context) (any, error) { return 22, nil }).Lazy(),
			),
		)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	html := fetchOK(t, srv.URL+"/admin/")
	for _, want := range []string{
		"steward-dash",       // the flowed tile keeps its own grid
		"steward-layout-row", // and the placed ones get a row
		"steward-span-8", "steward-span-4",
		">7<", ">11<", "Trend", "chart.umd.min.js",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the dashboard should contain %q", want)
		}
	}
	// A tile placed in a row must not also flow into the grid above it.
	if strings.Count(html, "Placed one") != 1 {
		t.Error("a placed tile should be rendered once")
	}
	// Lazy still works inside a row. The URL alone proves nothing: the index it
	// carries has to resolve to that tile, so the fragment is fetched.
	m := regexp.MustCompile(`/_widget/(\d+)`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("a lazy tile in a row should still fetch itself")
	}
	frag := fetchOK(t, srv.URL+"/admin/_widget/"+m[1])
	if !strings.Contains(frag, "Placed two") || !strings.Contains(frag, ">22<") {
		t.Errorf("the lazy fragment should be the tile that asked for it, got: %s", frag)
	}
}

// Where a row is declared is where it appears: tiles before it stay before it.
func TestDashboardKeepsDeclarationOrder(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("dashboard-order-test-secret-key"),
		AuthExcept: []string{"/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	num := func(n int) func(*steward.Context) (any, error) {
		return func(*steward.Context) (any, error) { return n, nil }
	}
	app.Dashboard(func(d *steward.Dashboard) {
		d.Metric("First", num(1))
		d.Row(steward.Col(12, d.Metric("Middle", num(2))))
		d.Metric("Last", num(3))
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	html := fetchOK(t, srv.URL+"/admin/")
	first := strings.Index(html, "First")
	middle := strings.Index(html, "Middle")
	last := strings.Index(html, "Last")
	if first < 0 || middle < 0 || last < 0 {
		t.Fatal("every tile should render")
	}
	if first >= middle || middle >= last {
		t.Errorf("declaration order should hold, got first=%d middle=%d last=%d", first, middle, last)
	}
	// And the row is a row, not folded into the grid around it.
	if !strings.Contains(html, "steward-layout-row") {
		t.Error("the placed tile should be in a row")
	}
	if n := strings.Count(html, "steward-dash"); n < 2 {
		t.Errorf("the run before and the run after should be separate grids, got %d", n)
	}
}

// A metric can carry a glyph and a colour, on a page and on the dashboard, and
// both are checked at boot rather than found missing on the screen.
func TestMetricIconAndColour(t *testing.T) {
	srv := newPageLayoutServer(t, func(c *steward.Context) error {
		return c.Layout("Metrics",
			steward.Row(
				steward.Col(6, steward.Metric("Published", 1752, "live").
					Icon("newspaper").Color(steward.BadgeGreen)),
				steward.Col(6, steward.Metric("Plain", 3)),
			),
		)
	})
	html := fetchOK(t, srv.URL+"/admin/page_layout_rows/report")
	if !strings.Contains(html, `data-tone="green"`) {
		t.Error("the colour should reach the card")
	}
	if !strings.Contains(html, "steward-metric-icon") || !strings.Contains(html, "<svg") {
		t.Error("the glyph should be drawn")
	}
	if strings.Count(html, "steward-metric-icon") != 1 {
		t.Error("a metric without an icon should not render an empty chip")
	}
	if !strings.Contains(html, "1752") || !strings.Contains(html, ">live<") {
		t.Error("the value and hint should still render")
	}
}

func TestMetricRejectsUnknownIconAndColour(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		build      func(d *steward.Dashboard)
	}{
		{"icon", "not found", func(d *steward.Dashboard) {
			d.Metric("A", func(*steward.Context) (any, error) { return 1, nil }).Icon("no-such-glyph")
		}},
		{"colour", "unknown colour", func(d *steward.Dashboard) {
			d.Metric("A", func(*steward.Context) (any, error) { return 1, nil }).Color("chartreuse")
		}},
		{"colour in a row", "unknown colour", func(d *steward.Dashboard) {
			d.Row(steward.Col(12, steward.Metric("A", 1).Color("chartreuse")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, err := steward.New(steward.Config{
				// These exercise a prefixed mount; the default is the root.
				Prefix: "/admin",
				DB:     testDB(t), SecretKey: []byte("metric-verify-test-secret-key"),
			})
			if err != nil {
				t.Fatal(err)
			}
			app.Dashboard(tc.build)
			if err := app.Build(); err != nil {
				t.Fatal(err)
			}
			err = app.Verify()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected a boot error mentioning %q, got: %v", tc.want, err)
			}
		})
	}
}
