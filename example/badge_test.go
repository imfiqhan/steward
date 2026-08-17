package main

import (
	"context"
	"html/template"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	steward "github.com/imfiqhan/steward"
	"gorm.io/gorm"
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

// The built-in RBAC pages have to say what a user holds and what a role
// permits; a list of names on the index is not the same as the record itself
// answering the question.
func TestRBACDetailPagesShowTheirGrants(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("rbac-detail-test-secret-key")})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")

	perm := steward.Permission{Name: "Manage posts", Slug: "manage-posts"}
	if err := db.Create(&perm).Error; err != nil {
		t.Fatal(err)
	}
	role := steward.Role{Name: "Editor", Slug: "editor", Permissions: []steward.Permission{perm}}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	user := steward.AdminUser{Username: "ed", Name: "Ed", Roles: []steward.Role{role}}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatalf("login failed: %d", code)
	}
	if _, html := c.get("/auth/users/" + itoa(user.ID)); !strings.Contains(html, "Editor") {
		t.Error("a user's page should name the roles it holds")
	}
	if _, html := c.get("/auth/roles/" + itoa(role.ID)); !strings.Contains(html, "Manage posts") {
		t.Error("a role's page should name what it permits")
	}
}

// What a palette row reads is otherwise guessed from the grid's first two text
// columns, which cannot reach a relation and cannot combine two values.

type paletteCategory struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type paletteRow struct {
	ID         uint `gorm:"primaryKey"`
	Title      string
	Slug       string
	CategoryID *uint
	Category   *paletteCategory
	PostDate   time.Time
}

func TestCommandDisplayNamesWhatARowShows(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteCategory{}, &paletteRow{}); err != nil {
		t.Fatal(err)
	}
	cat := paletteCategory{Name: "Politics"}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	if err := db.Create(&paletteRow{Title: "A headline", Slug: "a-headline", CategoryID: &cat.ID, PostDate: when}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("command-display-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).
		Command("Title").
		CommandDisplay("Title", "Category.Name", "PostDate")
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")

	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatalf("login failed: %d", code)
	}
	_, body := c.get("/_command?q=headline")
	if !strings.Contains(body, `"title":"A headline"`) {
		t.Errorf("the first path should be the row's title, got: %s", body)
	}
	// The rest join into the dimmer line, so one row can carry a category and a
	// date without either replacing the headline.
	if !strings.Contains(body, `"subtitle":"Politics · 2026-07-31"`) {
		t.Errorf("the remaining paths should join into the subtitle, got: %s", body)
	}
}

// TestCommandDisplayRejectsAnUnknownPath keeps a typo from quietly showing an
// empty line.
func TestCommandDisplayRejectsAnUnknownPath(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteCategory{}, &paletteRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("command-display-bad-secret")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).Command("Title").CommandDisplay("Titel")
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	err = app.Verify()
	if err == nil || !strings.Contains(err.Error(), "command display") {
		t.Errorf("an unknown display path should be a boot error, got: %v", err)
	}
}

// A palette search cut short by the deadline returns nothing, which is the
// shape of "no matches". The response says which it was.
func TestCommandSearchReportsBeingCutShort(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteCategory{}, &paletteRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&paletteRow{Title: "A headline"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("command-partial-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).Command("Title")
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")

	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatal("login failed")
	}
	_, body := c.get("/_command?q=headline")
	if !strings.Contains(body, `"partial":false`) {
		t.Errorf("a search that finished should say so, got: %s", body)
	}
}

// TestCommandSearchSkipsTheCount covers what made a large table time out: the
// palette shows a fixed few rows, so the COUNT that pages a grid is work whose
// answer nobody reads.
func TestCommandSearchSkipsTheCount(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow{}); err != nil {
		t.Fatal(err)
	}
	var counted bool
	db = db.Session(&gorm.Session{})
	// After, not before: the statement's SQL is built by the query callback, so
	// before it there is nothing to read.
	db.Callback().Query().After("gorm:query").Register("count-probe", func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "count(") {
			counted = true
		}
	})
	for i := 0; i < 3; i++ {
		if err := db.Create(&paletteRow{Title: "A headline"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("command-count-test-secret")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).Command("Title")
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")

	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatal("login failed")
	}
	counted = false
	_, body := c.get("/_command?q=headline")
	if !strings.Contains(body, "A headline") {
		t.Fatalf("the search should find the row, got: %s", body)
	}
	if counted {
		t.Error("the palette should not ask for a total it never shows")
	}
}

// A thumbnail sized for a row, and a stored file that is only a path, both open
// full size rather than sending the reader to another tab.
func TestImagesAndFilesPreview(t *testing.T) {
	srv := newMediaServer(t, mediaRow{Cover: "images/cover.jpg", Doc: "files/report.pdf"})

	grid := fetchOK(t, srv.URL+"/admin/media_rows")
	if !strings.Contains(grid, "data-steward-preview=") {
		t.Error("a grid thumbnail should open a preview")
	}
	if !strings.Contains(grid, "steward-preview-trigger") {
		t.Error("the thumbnail's trigger should carry no button chrome")
	}

	detail := fetchOK(t, srv.URL+"/admin/media_rows/1")
	if strings.Count(detail, "data-steward-preview=") < 2 {
		t.Error("both the image and the stored file should be previewable on a detail page")
	}
	if !strings.Contains(detail, ">Preview</button>") {
		t.Error("a stored file needs its own trigger, since its link is text")
	}
}

// TestPreviewSkipsWhatItCannotShow keeps the button off a link that points
// somewhere else, and off a file the viewer would render as a blank frame.
func TestPreviewSkipsWhatItCannotShow(t *testing.T) {
	for _, tc := range []struct{ name, stored string }{
		{"an absolute URL", "https://cdn.example.com/a.zip"},
		{"a format the viewer cannot show", "files/archive.zip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMediaServer(t, mediaRow{Doc: tc.stored})
			detail := fetchOK(t, srv.URL+"/admin/media_rows/1")
			if strings.Contains(detail, ">Preview</button>") {
				t.Errorf("%s should not offer a preview", tc.name)
			}
		})
	}
}

// The asset URL carries a content hash in development as well. A fixed segment
// lets a browser pair this build's markup with a stylesheet cached from an
// earlier one, which is how a layout loses its gaps while the CSS on disk is
// correct.
func TestAssetURLChangesWithTheAssets(t *testing.T) {
	url := func(dev bool, extra fstest.MapFS) string {
		db := testDB(t)
		cfg := steward.Config{DB: db, SecretKey: []byte("asset-version-test-secret"), Dev: dev}
		if extra != nil {
			cfg.AssetsFS = extra
		}
		app, err := steward.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Build(); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(app)
		t.Cleanup(srv.Close)
		m := regexp.MustCompile(`/admin/_assets/([^/]+)/`).FindStringSubmatch(fetchOK(t, srv.URL+"/admin/login"))
		if m == nil {
			t.Fatal("no asset URL on the page")
		}
		return m[1]
	}

	base := url(true, nil)
	if base == "dev" {
		t.Error("a development asset URL should still change when the assets do")
	}
	changed := url(true, fstest.MapFS{
		"dist/app.css": &fstest.MapFile{Data: []byte("/* an overlay */")},
	})
	if changed == base {
		t.Error("overlaying an asset should change the URL")
	}
	if url(false, nil) != base {
		t.Error("the same assets should hash the same either way")
	}
}

// A link and a file look like any other cell until they are hovered. The glyph
// says which one it is before that: something to open, or somewhere to go.
func TestLinksAndFilesAreMarked(t *testing.T) {
	srv := newMediaServer(t, mediaRow{Cover: "images/cover.jpg", Doc: "files/report.pdf"})
	detail := fetchOK(t, srv.URL+"/admin/media_rows/1")
	// The paperclip marks a stored file.
	if !strings.Contains(detail, "M16 6-8.414") && !strings.Contains(detail, "m16 6-8.414") {
		t.Error("a stored file should carry the file glyph")
	}
	if !strings.Contains(detail, "files/report.pdf") {
		t.Error("the value should still read as the stored path")
	}

	external := newMediaServer(t, mediaRow{Doc: "https://example.com/a.pdf"})
	out := fetchOK(t, external.URL+"/admin/media_rows/1")
	// An absolute URL leaves the panel, so it carries the external-link glyph.
	if !strings.Contains(out, "M15 3h6v6") {
		t.Error("an absolute URL should carry the link glyph")
	}
	if strings.Contains(out, "m16 6-8.414") {
		t.Error("an absolute URL is not a stored file")
	}
}

// A search served by the engine is bounded by a window, so its total is the
// number of hits taken rather than the number of matches. Reported flat it is
// a figure the reader has no reason to doubt.
func TestSearchTotalSaysWhenItIsAFloor(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("capped-total-test-secret-key"),
		AuthExcept: []string{"/palette_rows*"},
		Searcher:   &windowSearcher{n: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[paletteRow](app).Searchable("Title")
	res.Grid(func(g *steward.Grid[paletteRow]) {
		g.Column("Title")
		g.QuickSearch("Title")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := db.Create(&paletteRow{Title: "A headline"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	full := fetchOK(t, srv.URL+"/admin/palette_rows?q=headline")
	if !strings.Contains(full, "of 3+") {
		t.Errorf("a full window should be reported as a floor, got: %s", pagerLine(full))
	}
}

// TestSearchTotalIsExactWhenItFits keeps the ordinary case honest too.
func TestSearchTotalIsExactWhenItFits(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("exact-total-test-secret-key"),
		AuthExcept: []string{"/palette_rows*"},
		Searcher:   &windowSearcher{n: 3}, // fewer matches than the window
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[paletteRow](app).Searchable("Title")
	res.Grid(func(g *steward.Grid[paletteRow]) {
		g.Column("Title")
		g.QuickSearch("Title")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := db.Create(&paletteRow{Title: "A headline"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	out := fetchOK(t, srv.URL+"/admin/palette_rows?q=headline")
	if strings.Contains(out, "of 3+") {
		t.Errorf("a total below the window is exact, got: %s", pagerLine(out))
	}
}

// pagerLine pulls the "Showing …" line out for readable failures.
func pagerLine(html string) string {
	i := strings.Index(html, "Showing")
	if i < 0 {
		return "(no pager)"
	}
	return strings.Join(strings.Fields(html[i:min(len(html), i+60)]), " ")
}

// windowSearcher answers with the ids of rows the test created, repeated until
// the window is full, which is what an engine holding more matches than the
// window does. It does not depend on Index: a test that writes with GORM never
// goes through the repository that indexes.
type windowSearcher struct{ n int }

func (w *windowSearcher) Index(_ context.Context, _ ...steward.SearchDoc) error { return nil }

func (w *windowSearcher) Delete(_ context.Context, _ string, _ ...string) error { return nil }

func (w *windowSearcher) Query(_ context.Context, _, _ string, limit int) ([]steward.SearchHit, error) {
	want := min(limit, w.n)
	out := make([]steward.SearchHit, 0, want)
	for i := 0; i < want; i++ {
		out = append(out, steward.SearchHit{ID: itoa(uint(i%3 + 1))})
	}
	return out, nil
}

// The filter panel divides into the same twelve columns a form does. Flowed
// instead, a range — which needs room for two controls — came out the
// narrowest thing in the row.
func TestFilterSpans(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("filter-span-test-secret-key"),
		AuthExcept: []string{"/palette_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).Grid(func(g *steward.Grid[paletteRow]) {
		g.Column("Title")
		g.Filter(func(f *steward.Filters[paletteRow]) {
			f.Equal("Title", "Exact").Span(2)
			f.Like("Slug", "Slug")            // default for a text filter
			f.DateRange("PostDate", "Posted") // default for a range
			f.Like("Title", "Wide").Span(12)
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	html := fetchOK(t, srv.URL+"/admin/palette_rows")
	if !strings.Contains(html, "steward-filter-grid") {
		t.Error("the panel should lay out on the grid")
	}
	for _, want := range []string{
		"steward-span-2",  // what Span asked for
		"steward-span-3",  // a text filter's default
		"steward-span-6",  // a range's default: room for two controls
		"steward-span-12", // clamped to the row
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the panel should contain %q", want)
		}
	}
}

// TestFilterSpanIsClamped keeps a nonsense value from producing a class no
// stylesheet has a rule for, which would silently take the full width.
func TestFilterSpanIsClamped(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&paletteRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("filter-clamp-test-secret-key"),
		AuthExcept: []string{"/palette_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[paletteRow](app).Grid(func(g *steward.Grid[paletteRow]) {
		g.Column("Title")
		g.Filter(func(f *steward.Filters[paletteRow]) {
			f.Like("Title", "Too wide").Span(99)
			f.Like("Slug", "Too narrow").Span(-3)
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	html := fetchOK(t, srv.URL+"/admin/palette_rows")
	for _, bad := range []string{"steward-span-99", "steward-span--3", "steward-span-0"} {
		if strings.Contains(html, bad) {
			t.Errorf("%q should have been clamped", bad)
		}
	}
	if !strings.Contains(html, "steward-span-12") || !strings.Contains(html, "steward-span-1") {
		t.Error("the clamped values should be 12 and 1")
	}
}
