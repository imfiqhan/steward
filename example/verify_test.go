package main

import (
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

type chartRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// TestVerifyCatchesAMissingChartRuntime covers a dashboard whose chart tiles
// would render empty. The drawing code returns early when window.Chart is not
// there, so the tile is blank and only a 404 in the console says why — and the
// files are fetched by `make vendor-chart` rather than committed, so a fresh
// clone is in exactly that state.
func TestVerifyCatchesAMissingChartRuntime(t *testing.T) {
	build := func(withChart bool) string {
		db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/c.db"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&chartRow{}); err != nil {
			t.Fatal(err)
		}
		app, err := steward.New(steward.Config{
			DB: db, SecretKey: []byte("chart-test-secret-key-000"),
		})
		if err != nil {
			t.Fatal(err)
		}
		steward.Register[chartRow](app)
		app.Dashboard(func(d *steward.Dashboard) {
			d.Metric("Rows", func(*steward.Context) (any, error) { return 1, nil })
			if withChart {
				d.Chart("Per month", func(*steward.Context) (*steward.ChartData, error) {
					return &steward.ChartData{}, nil
				})
			}
		})
		if err := app.Verify(); err != nil {
			return err.Error()
		}
		return ""
	}

	got := build(true)
	if !strings.Contains(got, "chart.umd.min.js") {
		t.Errorf("a chart widget with no runtime was not reported: %q", got)
	}
	if !strings.Contains(got, "make vendor-chart") {
		t.Errorf("the error does not say how to fix it: %q", got)
	}

	// A dashboard with no chart tile needs none of it and must stay quiet.
	if got := build(false); strings.Contains(got, "chart") {
		t.Errorf("a chartless dashboard was reported: %q", got)
	}
}
