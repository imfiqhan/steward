package main

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// Badge colours a value; Using replaces it with display text. A status column
// wants both — the green of "published" and the word for it — so the colour has
// to stay keyed on what is stored while the text comes from the map.

type badgeRow struct {
	ID     uint `gorm:"primaryKey"`
	Status int16
}

var (
	badgeColors = map[any]steward.BadgeColor{int16(0): steward.BadgeSecondary, int16(1): steward.BadgeGreen}
	badgeLabels = map[any]string{int16(0): "Tidak dipublikasikan", int16(1): "Dipublikasikan"}
)

// newBadgeServer builds a panel whose grid column and detail field are both
// configured by the caller, so one test can drive either order of the two.
func newBadgeServer(t *testing.T, grid func(*steward.Grid[badgeRow]), detail func(*steward.Detail[badgeRow])) string {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&badgeRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&badgeRow{Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("badge-test-secret-key"),
		AuthExcept: []string{"/badge_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[badgeRow](app)
	res.Grid(grid)
	res.Detail(detail)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestBadgeAndUsingCompose(t *testing.T) {
	cases := []struct {
		name   string
		grid   func(*steward.Grid[badgeRow])
		detail func(*steward.Detail[badgeRow])
	}{
		{"badge then using",
			func(g *steward.Grid[badgeRow]) { g.Column("Status").Badge(badgeColors).Using(badgeLabels) },
			func(d *steward.Detail[badgeRow]) { d.Field("Status").Badge(badgeColors).Using(badgeLabels) }},
		{"using then badge",
			func(g *steward.Grid[badgeRow]) { g.Column("Status").Using(badgeLabels).Badge(badgeColors) },
			func(d *steward.Detail[badgeRow]) { d.Field("Status").Using(badgeLabels).Badge(badgeColors) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newBadgeServer(t, tc.grid, tc.detail)
			for _, page := range []struct{ what, url string }{
				{"grid", base + "/admin/badge_rows"},
				{"detail", base + "/admin/badge_rows/1"},
			} {
				html := fetchOK(t, page.url)
				if !strings.Contains(html, "Dipublikasikan") {
					t.Errorf("%s: the text should come from Using", page.what)
				}
				if !strings.Contains(html, "badge") {
					t.Errorf("%s: the value should still be a badge", page.what)
				}
				// The colour is the half that fails quietly: an unmatched key
				// falls back to the default variant, which still looks like a
				// badge.
				if !strings.Contains(html, "text-green-700") {
					t.Errorf("%s: status 1 should be green, got: %s", page.what, badgeSpan(html))
				}
			}
		})
	}
}

// TestBadgeAloneShowsTheStoredValue keeps the single-use behaviour: with no
// Using, a badge's text is the value itself.
func TestBadgeAloneShowsTheStoredValue(t *testing.T) {
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Badge(badgeColors) },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Badge(badgeColors) })
	for _, url := range []string{base + "/admin/badge_rows", base + "/admin/badge_rows/1"} {
		html := fetchOK(t, url)
		span := badgeSpan(html)
		if !strings.Contains(span, ">1<") || !strings.Contains(span, "text-green-700") {
			t.Errorf("a badge without Using should show the stored value in colour, got: %s", span)
		}
	}
}

// TestUsingAloneShowsPlainText keeps the other single-use behaviour: Using
// without Badge is text, not a badge.
func TestUsingAloneShowsPlainText(t *testing.T) {
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Using(badgeLabels) },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Using(badgeLabels) })
	for _, url := range []string{base + "/admin/badge_rows", base + "/admin/badge_rows/1"} {
		html := fetchOK(t, url)
		if !strings.Contains(html, "Dipublikasikan") {
			t.Error("Using should map the value to its text")
		}
		if strings.Contains(html, `class="badge`) {
			t.Error("Using alone should not render a badge")
		}
	}
}

// TestExportKeepsUsingText covers the path that does not go through the badge:
// a CSV cell is the mapped text, not the markup and not the raw number.
func TestExportKeepsUsingText(t *testing.T) {
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Badge(badgeColors).Using(badgeLabels) },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Badge(badgeColors).Using(badgeLabels) })
	csv := fetchOK(t, base+"/admin/badge_rows?export=all")
	if !strings.Contains(csv, "Dipublikasikan") {
		t.Errorf("the export should carry the mapped text, got: %s", csv)
	}
	if strings.Contains(csv, "badge") || strings.Contains(csv, "span") {
		t.Errorf("the export should carry no markup, got: %s", csv)
	}
}

// badgeSpan returns the first badge element, for readable failures.
func badgeSpan(html string) string {
	i := strings.Index(html, `class="badge`)
	if i < 0 {
		return "(no badge)"
	}
	start := strings.LastIndex(html[:i], "<")
	end := strings.Index(html[i:], "</span>")
	if start < 0 || end < 0 {
		return "(malformed)"
	}
	return html[start : i+end+len("</span>")]
}

// TestBoolTakesItsOwnWords covers a panel that is not in English: Bool says Yes
// and No unless it is given two words.
func TestBoolTakesItsOwnWords(t *testing.T) {
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Bool("Ya", "Tidak") },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Bool("Ya", "Tidak") })
	for _, url := range []string{base + "/admin/badge_rows", base + "/admin/badge_rows/1"} {
		html := fetchOK(t, url)
		if !strings.Contains(html, ">Ya<") {
			t.Error("Bool should use the words it was given")
		}
		if strings.Contains(html, ">Yes<") {
			t.Error("the English default should be gone")
		}
	}
}

// TestBoolDefaultsToEnglish keeps the existing call sites working.
func TestBoolDefaultsToEnglish(t *testing.T) {
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Bool() },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Bool() })
	if !strings.Contains(fetchOK(t, base+"/admin/badge_rows"), ">Yes<") {
		t.Error("Bool with no labels should still say Yes")
	}
}

// TestBoolRejectsOneLabel is the arity Verify has to catch: one word is a
// mistake, and guessing which half it is would render the wrong text.
func TestBoolRejectsOneLabel(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&badgeRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("bool-arity-test-secret-key")})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[badgeRow](app)
	res.Grid(func(g *steward.Grid[badgeRow]) { g.Column("Status").Bool("Ya") })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	err = app.Verify()
	if err == nil {
		t.Fatal("Bool with one label should not verify")
	}
	if !strings.Contains(err.Error(), "exactly two") {
		t.Errorf("the error should name the arity, got: %v", err)
	}
}

// TestBadgeColoursKeyedOnTheReplacement covers the older spelling, where the
// colour map is keyed on what Using produced rather than on what is stored.
func TestBadgeColoursKeyedOnTheReplacement(t *testing.T) {
	byText := map[any]steward.BadgeColor{"Dipublikasikan": steward.BadgeGreen}
	base := newBadgeServer(t,
		func(g *steward.Grid[badgeRow]) { g.Column("Status").Using(badgeLabels).Badge(byText) },
		func(d *steward.Detail[badgeRow]) { d.Field("Status").Using(badgeLabels).Badge(byText) })
	for _, url := range []string{base + "/admin/badge_rows", base + "/admin/badge_rows/1"} {
		html := fetchOK(t, url)
		if !strings.Contains(html, "text-green-700") {
			t.Errorf("a colour keyed on Using's text should still apply, got: %s", badgeSpan(html))
		}
	}
}

// A detail page can only show what a struct path names, which leaves out
// anything computed from the whole record — a collection, a summary. FieldFunc
// is the grid's ColumnFunc on the show view.

func TestDetailFieldFunc(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&badgeRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&badgeRow{Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("fieldfunc-test-secret-key"),
		AuthExcept: []string{"/badge_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[badgeRow](app)
	res.Detail(func(d *steward.Detail[badgeRow]) {
		d.Field("ID")
		d.FieldFunc("tags", "Tag", func(r *badgeRow) template.HTML {
			return template.HTML(`<span class="badge">row-` + itoa(r.ID) + `</span>`)
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatalf("a computed row has no path to verify: %v", err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	html := fetchOK(t, srv.URL+"/admin/badge_rows/1")
	if !strings.Contains(html, "row-1") {
		t.Error("the computed value should be rendered")
	}
	if !strings.Contains(html, ">Tag<") {
		t.Error("the label should be the one given")
	}
}

// A relation named only by a detail field still has to be loaded. Preloads were
// collected from grid columns alone, so a detail row whose relation the grid
// did not also name rendered as if the value were unset.

type relOwner struct {
	ID      uint `gorm:"primaryKey"`
	Name    string
	OwnerID *uint
	Owner   *relOwner `gorm:"foreignKey:OwnerID"`
}

func TestDetailPreloadsItsOwnRelations(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&relOwner{}); err != nil {
		t.Fatal(err)
	}
	parent := relOwner{Name: "the parent"}
	if err := db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&relOwner{Name: "the child", OwnerID: &parent.ID}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("detail-preload-test-secret-key"),
		AuthExcept: []string{"/rel_owners*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[relOwner](app)
	// The grid deliberately names no relation, which is the case that used to
	// leave the detail row empty.
	res.Grid(func(g *steward.Grid[relOwner]) { g.Column("Name") })
	res.Detail(func(d *steward.Detail[relOwner]) {
		d.Field("Name")
		d.Field("Owner.Name", "Parent")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	html := fetchOK(t, srv.URL+"/admin/rel_owners/2")
	if !strings.Contains(html, "the parent") {
		t.Error("the detail page should show the related row's value")
	}
}
