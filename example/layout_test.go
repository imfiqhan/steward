package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// wideRow has enough nowrap columns to make a grid table exceed any viewport,
// which is what exposes the layout bug this guards.
type wideRow struct {
	ID   uint `gorm:"primaryKey"`
	ColA string
	ColB string
	ColC string
	ColD string
	ColE string
	ColF string
	ColG string
	ColH string
	ColI string
	ColJ string
	ColK string
	ColL string
}

// TestPageWrappersCannotBeWidenedByContent guards against grid blowout.
//
// A page-level `display: grid` with an implicit auto track sizes that track to
// its widest item's max-content, and a grid item's automatic minimum size is its
// min-content width. One table of nowrap cells therefore pushed the whole page
// wider than the window instead of scrolling inside .table-container. Tailwind's
// grid-cols-1 is repeat(1, minmax(0, 1fr)), which caps the track's minimum at 0.
//
// Asserted on the markup because the failure is a computed-layout property: it
// needs a browser to observe, but it cannot happen while the cap is present.
func TestPageWrappersCannotBeWidenedByContent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/wide.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&wideRow{}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("wide-content-", 6)
	if err := db.Create(&wideRow{
		ColA: long, ColB: long, ColC: long, ColD: long, ColE: long, ColF: long,
		ColG: long, ColH: long, ColI: long, ColJ: long, ColK: long, ColL: long,
	}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("layout-width-test-secret-key"),
		AuthExcept: []string{"/wide_rows*", "/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[wideRow](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		return string(b)
	}

	for _, tc := range []struct{ name, path string }{
		{"grid", "/admin/wide_rows"},
		{"detail", "/admin/wide_rows/1"},
		{"form", "/admin/wide_rows/1/edit"},
		{"dashboard", "/admin/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html := get(tc.path)
			// The page's own wrapper must cap its track's minimum.
			if !strings.Contains(html, "p-6 grid grid-cols-1") {
				t.Error("the page wrapper is a grid without a minmax(0,…) cap, so wide " +
					"content will widen the page instead of scrolling")
			}
		})
	}

	// The grid additionally needs a scroll container around the table, and a
	// section that is allowed to shrink for it to engage.
	html := get("/admin/wide_rows")
	if !strings.Contains(html, "table-container") {
		t.Error("the table needs its overflow-x scroll container")
	}
	if !strings.Contains(html, "px-0 min-w-0") {
		t.Error("the table's section must be allowed to shrink below its content")
	}
}

// TestActionsColumnIsPinned covers the column that must stay reachable once a
// wide table starts scrolling sideways.
func TestActionsColumnIsPinned(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/pin.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&wideRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&wideRow{ColA: "a", ColB: "b"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("pinned-actions-test-secret-key"),
		AuthExcept: []string{"/wide_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[wideRow](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/wide_rows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)

	// The header cell and the row cell both have to be pinned, or the column
	// detaches from its heading as soon as it scrolls.
	if n := strings.Count(html, "steward-col-actions"); n < 2 {
		t.Errorf("expected the actions header and cell to be pinned, found %d", n)
	}
	if !strings.Contains(html, `<th class="w-[1%] steward-col-actions">`) {
		t.Error("the actions header cell is not pinned")
	}
	if !strings.Contains(html, `<td class="text-end steward-col-actions">`) {
		t.Error("the actions body cell is not pinned")
	}
	// The container is flagged so the divider can be shown only while scrolled.
	if !strings.Contains(html, "data-steward-hscroll") {
		t.Error("the scroll container is not tracked")
	}
}
