package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// getAuth fetches a page as an authenticated caller. A bearer token is used
// rather than AuthExcept because the built-in dashboard reads .Page.User, so
// these pages must be exercised with a real principal.
func getAuth(t *testing.T, url, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// dashboardServer starts a panel, optionally with declared widgets, and returns
// its base URL plus a token for the seeded admin.
func dashboardServer(t *testing.T, dsn string, widgets func(*steward.Dashboard)) (string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix:          "/admin",
		DB:              db,
		SecretKey:       []byte("dashboard-test-secret-key"),
		EnableTokenAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if widgets != nil {
		app.Dashboard(widgets)
	}
	if err := app.Build(); err != nil { // runs migrations; seeds admin/admin
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	base := srv.URL + "/admin"

	code, token := issue(t, base, "admin", "admin")
	if code != http.StatusCreated {
		t.Fatalf("could not mint a token for the test: %d", code)
	}
	return base, token
}

func TestDashboardWidgets(t *testing.T) {
	base, token := dashboardServer(t, "file:dashtest?mode=memory&cache=shared", func(d *steward.Dashboard) {
		d.Metric("Widgets", func(*steward.Context) (any, error) { return 42, nil }).
			Span(1).Hint("all time")
		d.Metric("Broken", func(*steward.Context) (any, error) {
			return nil, errors.New("boom")
		}).Span(1)
		d.Metric("Deferred", func(*steward.Context) (any, error) { return 7, nil }).
			Span(1).Lazy()
	})

	code, body := getAuth(t, base+"/", token)
	if code != http.StatusOK {
		t.Fatalf("GET dashboard = %d, want 200", code)
	}

	// The eager metric renders its value inline.
	if !strings.Contains(body, "42") {
		t.Error("eager metric value missing from the page")
	}
	if !strings.Contains(body, "all time") {
		t.Error("metric hint missing")
	}

	// A failing widget reports in place; the rest of the page still renders.
	if !strings.Contains(body, "Could not load.") {
		t.Error("failing widget did not report its error in place")
	}
	if !strings.Contains(body, "Broken") {
		t.Error("failing widget lost its title")
	}
	if strings.Contains(body, "boom") {
		t.Error("internal error text leaked to the page")
	}

	// The lazy widget ships a skeleton plus the fetch, not its value.
	if !strings.Contains(body, `hx-get="/admin/_widget/2"`) {
		t.Errorf("lazy widget did not emit its fetch; body:\n%s", body)
	}
	if strings.Contains(body, ">7<") {
		t.Error("lazy widget resolved eagerly")
	}
	// Span survives into the markup so the grid CSS can act on it.
	if !strings.Contains(body, `data-span="1"`) {
		t.Error("data-span attribute missing")
	}
}

func TestDashboardLazyFragment(t *testing.T) {
	base, token := dashboardServer(t, "file:dashlazy?mode=memory&cache=shared", func(d *steward.Dashboard) {
		d.Metric("Deferred", func(*steward.Context) (any, error) { return 7, nil }).Span(2).Lazy()
	})

	code, body := getAuth(t, base+"/_widget/0", token)
	if code != http.StatusOK {
		t.Fatalf("GET widget fragment = %d, want 200", code)
	}
	if !strings.Contains(body, "7") {
		t.Errorf("fragment missing the resolved value; body:\n%s", body)
	}
	// The fragment carries its own wrapper, so an outerHTML swap keeps the span.
	if !strings.Contains(body, `data-span="2"`) {
		t.Error("fragment dropped its data-span wrapper")
	}
	// It must not re-arm the lazy fetch, or the tile would loop forever.
	if strings.Contains(body, "hx-get") {
		t.Error("fragment re-armed hx-get; the tile would fetch in a loop")
	}

	// Out-of-range and non-numeric indexes are refused, not panicked on.
	for _, bad := range []string{"99", "abc"} {
		if code, _ := getAuth(t, base+"/_widget/"+bad, token); code != http.StatusNotFound {
			t.Errorf("GET /_widget/%s = %d, want 404", bad, code)
		}
	}
}

// TestDashboardDefaultsWithoutBuilder proves the built-in overview still serves
// when Admin.Dashboard was never called.
func TestDashboardDefaultsWithoutBuilder(t *testing.T) {
	base, token := dashboardServer(t, "file:dashdefault?mode=memory&cache=shared", nil)

	code, body := getAuth(t, base+"/", token)
	if code != http.StatusOK {
		t.Fatalf("GET default dashboard = %d, want 200", code)
	}
	if !strings.Contains(body, "Welcome to Steward") {
		t.Error("built-in overview page did not render")
	}
}

// TestDashboardWidgetRouteAbsentWithoutBuilder: with no widgets declared the
// fragment route has nothing to serve.
func TestDashboardWidgetRouteAbsentWithoutBuilder(t *testing.T) {
	base, token := dashboardServer(t, "file:dashnowidget?mode=memory&cache=shared", nil)
	if code, _ := getAuth(t, base+"/_widget/0", token); code != http.StatusNotFound {
		t.Errorf("GET /_widget/0 with no widgets = %d, want 404", code)
	}
}

func TestDashboardChartWidget(t *testing.T) {
	base, token := dashboardServer(t, "file:dashchart?mode=memory&cache=shared", func(d *steward.Dashboard) {
		d.Chart("Visitors", func(*steward.Context) (*steward.ChartData, error) {
			return &steward.ChartData{
				Type:   steward.ChartBar,
				Labels: []string{"Jan", "Feb"},
				Series: []steward.ChartSeries{
					{Label: "Desktop", Values: []float64{186, 305}},
				},
				Legend: true,
			}, nil
		}).Span(2)
	})

	code, body := getAuth(t, base+"/", token)
	if code != http.StatusOK {
		t.Fatalf("GET dashboard = %d, want 200", code)
	}
	// The canvas and its payload are server-rendered.
	if !strings.Contains(body, "data-steward-chart") {
		t.Error("chart container missing")
	}
	if !strings.Contains(body, "<canvas") {
		t.Error("canvas missing")
	}
	if !strings.Contains(body, `"labelKey":"label"`) || !strings.Contains(body, `"Desktop"`) {
		t.Errorf("chart payload missing or malformed; body:\n%s", body)
	}
	// The runtime is pulled in once, only because a chart is present.
	if !strings.Contains(body, "chart.umd.min.js") {
		t.Error("chart runtime script not included on a page with a chart")
	}
	// The note is present so a missing runtime explains itself.
	if !strings.Contains(body, "data-steward-chart-note") {
		t.Error("self-explaining note missing")
	}
}

func TestDashboardOmitsChartRuntimeWithoutCharts(t *testing.T) {
	base, token := dashboardServer(t, "file:dashnochart?mode=memory&cache=shared", func(d *steward.Dashboard) {
		d.Metric("Plain", func(*steward.Context) (any, error) { return 1, nil })
	})
	_, body := getAuth(t, base+"/", token)
	if strings.Contains(body, "chart.umd.min.js") {
		t.Error("chart runtime loaded on a page with no charts")
	}
}

func TestDashboardChartReportsBadData(t *testing.T) {
	base, token := dashboardServer(t, "file:dashbadchart?mode=memory&cache=shared", func(d *steward.Dashboard) {
		// Values shorter than Labels is a caller bug; the tile must say so
		// rather than emit a broken payload.
		d.Chart("Broken", func(*steward.Context) (*steward.ChartData, error) {
			return &steward.ChartData{
				Labels: []string{"Jan", "Feb"},
				Series: []steward.ChartSeries{{Label: "a", Values: []float64{1}}},
			}, nil
		})
	})
	code, body := getAuth(t, base+"/", token)
	if code != http.StatusOK {
		t.Fatalf("GET dashboard = %d, want 200", code)
	}
	if !strings.Contains(body, "Could not load.") {
		t.Error("malformed chart did not report in place")
	}
}
