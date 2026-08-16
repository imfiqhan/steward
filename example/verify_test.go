package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type verifyRow struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status string
	Doc    string
}

// verifyWith builds a panel with the given configuration and returns whatever
// Verify has to say about it.
func verifyWith(t *testing.T, configure func(*steward.Resource[verifyRow]), disks map[string]steward.Disk) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/v.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&verifyRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("verify-test-secret-key-00"),
		UploadDir: t.TempDir(), Disks: disks, DefaultDisk: defaultDiskFor(disks),
	})
	if err != nil {
		t.Fatal(err)
	}
	configure(steward.Register[verifyRow](app))
	if err := app.Verify(); err != nil {
		return err.Error()
	}
	return ""
}

// defaultDiskFor names a default when the test configured disks at all.
func defaultDiskFor(disks map[string]steward.Disk) string {
	if len(disks) == 0 {
		return ""
	}
	return "media"
}

// TestVerifyCatchesAnUnknownRule is the quiet one. The rule engine's switch has
// no default case, so a rule it does not recognise is skipped without a word:
// "requried" removes a required check and the field looks validated.
func TestVerifyCatchesAnUnknownRule(t *testing.T) {
	got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Form(func(f *steward.Form[verifyRow]) {
			f.Text("Title").Rules("requried|max:255")
		})
	}, nil)

	if !strings.Contains(got, `unknown validation rule "requried"`) {
		t.Errorf("Verify said %q", got)
	}
	// The message names what was allowed, not only what was not.
	if !strings.Contains(got, "required") || !strings.Contains(got, "known rules:") {
		t.Errorf("the error does not list the known rules: %q", got)
	}

	// The rules that exist pass, including the ones taking an argument.
	if got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Form(func(f *steward.Form[verifyRow]) {
			f.Text("Title").Rules("required|max:255|min:2")
			f.Text("Status").Rules("in:draft,live").CreationRules("required")
		})
	}, nil); got != "" {
		t.Errorf("a valid rule set was rejected: %s", got)
	}
}

// TestVerifyCatchesAnUnknownDisk: a field naming a disk that was never
// configured would quietly store to the default one instead.
func TestVerifyCatchesAnUnknownDisk(t *testing.T) {
	disks := map[string]steward.Disk{"media": {}, "public": {Public: true}}

	got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Form(func(f *steward.Form[verifyRow]) { f.File("Doc").Disk("pubic") })
	}, disks)
	if !strings.Contains(got, `unknown disk "pubic"`) {
		t.Errorf("Verify said %q", got)
	}
	if !strings.Contains(got, "configured: media, public") {
		t.Errorf("the error does not list the configured disks: %q", got)
	}

	if got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Form(func(f *steward.Form[verifyRow]) { f.File("Doc").Disk("public") })
	}, disks); got != "" {
		t.Errorf("a configured disk was rejected: %s", got)
	}
}

// TestVerifyCatchesAnUnknownBadgeColour covers the type's blind spot: a
// BadgeColor is a string type, so a custom value compiles — which is the point —
// and Verify is what tells you when the custom value was a typo.
func TestVerifyCatchesAnUnknownBadgeColour(t *testing.T) {
	got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Grid(func(g *steward.Grid[verifyRow]) {
			g.Column("Status").Badge(map[any]steward.BadgeColor{"live": "grene"})
		})
	}, nil)
	if !strings.Contains(got, `unknown badge colour "grene"`) {
		t.Errorf("Verify said %q", got)
	}
	if !strings.Contains(got, "green") {
		t.Errorf("the error does not list the known colours: %q", got)
	}

	// The named constants pass, on both a column and a detail field.
	if got := verifyWith(t, func(r *steward.Resource[verifyRow]) {
		r.Grid(func(g *steward.Grid[verifyRow]) {
			g.Column("Status").Badge(map[any]steward.BadgeColor{
				"live": steward.BadgeGreen, "draft": steward.BadgeSecondary,
			})
		})
		r.Detail(func(d *steward.Detail[verifyRow]) {
			d.Field("Status").Badge(map[any]steward.BadgeColor{"live": steward.BadgeAzure})
		})
	}, nil); got != "" {
		t.Errorf("the named colours were rejected: %s", got)
	}
}

// TestChartRuntimeShipsWithTheModule is the contract behind committing it: a
// panel built from a released module must be able to serve Chart.js. It used to
// be fetched by `make vendor-chart` and left uncommitted, so a consumer — who
// cannot run this repo's Makefile — had charts that could never draw.
func TestChartRuntimeShipsWithTheModule(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/c.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chartRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("chart-test-secret-key-000"),
		AuthExcept: []string{"/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[chartRow](app)
	app.Dashboard(func(d *steward.Dashboard) {
		d.Chart("Per month", func(*steward.Context) (*steward.ChartData, error) {
			return &steward.ChartData{}, nil
		})
	})
	if err := app.Verify(); err != nil {
		t.Fatalf("a dashboard with a chart does not verify: %v", err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	// The dashboard names the runtime's URL; follow it as a browser would.
	page := getBody(t, srv.URL+"/admin/")
	m := regexp.MustCompile(`src="([^"]*chart[.]umd[.]min[.]js[^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("the dashboard does not load the chart runtime")
	}
	code, body := getStatus(t, srv.URL+m[1])
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d", m[1], code)
	}
	if len(body) < 100000 || !strings.Contains(body, "Chart") {
		t.Errorf("that URL served %d bytes and does not look like Chart.js", len(body))
	}

	// MIT asks for the notice to travel with the code.
	if _, err := os.Stat("../assets/dist/chart.umd.min.LICENSE"); err != nil {
		t.Errorf("the licence does not ship beside the runtime: %v", err)
	}
}

type chartRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}
