package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// menuItemRe matches the opening tag of a dropdown menu item.
var menuItemRe = regexp.MustCompile(`<[a-z]+[^>]*role="menuitem"[^>]*>`)

// TestMenuItemsStayOutOfTheTabOrder guards the dropdown component's contract:
// focus stays on the trigger while the highlighted item is tracked through
// aria-activedescendant. A focusable item would be reachable by Tab with the
// component's own index still at -1, and its Enter handler — which activates
// that index and swallows the key — would then do nothing at all.
func TestMenuItemsStayOutOfTheTabOrder(t *testing.T) {
	srv, a := new2FAServer(t, false)
	seedUser(t, a, "editor", "editor-password")

	c := new2FAClient(t, srv)
	if code, body := c.login("editor", "editor-password"); code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, body)
	}
	_, page := c.get("/auth/profile")

	items := menuItemRe.FindAllString(page, -1)
	if len(items) == 0 {
		t.Fatal("the header's user menu did not render")
	}
	for _, item := range items {
		if !strings.Contains(item, `tabindex="-1"`) {
			t.Errorf("menu item is in the tab order: %s", item)
		}
		if !strings.Contains(item, ` id="`) {
			t.Errorf("menu item has no id for aria-activedescendant: %s", item)
		}
	}
	// A menu's children have to be items, groups, or separators, so the form
	// wrapping the sign-out item is marked presentational.
	if !strings.Contains(page, `action="/admin/auth/logout" role="none"`) {
		t.Error("the sign-out form should be out of the menu's accessibility tree")
	}
}

// builtStylesheet serves the panel and returns the CSS it links to. The URL
// carries a cache-busting version, so it is read off a rendered page rather
// than guessed.
func builtStylesheet(t *testing.T) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/css.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("stylesheet-test-secret-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	page, err := http.Get(srv.URL + "/admin/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = page.Body.Close() }()
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`href="([^"]+app\.css)"`).FindStringSubmatch(string(body))
	if m == nil {
		t.Fatal("no stylesheet link on the login page")
	}

	resp, err := http.Get(srv.URL + m[1])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", m[1], resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestRowMenuStylesheetRules guards two CSS rules that no markup assertion can
// reach, both of which failed visibly before they existed.
//
// A pinned cell is positioned with a z-index, which makes it a stacking
// context: its open menu paints inside that context however high the popover's
// own z-index is, and cells tied at the same z-index paint in tree order — so
// every row below the open one covered the menu.
//
// The popover's shared transition is transition-all, and left/top interpolate,
// so promoting it to script-computed fixed coordinates animated it in from
// wherever the stylesheet had put it instead of from the trigger.
func TestRowMenuStylesheetRules(t *testing.T) {
	css := builtStylesheet(t)

	for _, want := range []string{
		`steward-col-actions:has([aria-expanded=true]){z-index:3}`,
		`[data-steward-menu]>[data-popover]{transition-property:opacity,transform,visibility}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the built stylesheet is missing %s", want)
		}
	}
}

// TestGridHeaderPinning covers the header row holding still while the rows
// scroll under it, which takes two rules that only work together.
//
// Sticky resolves against the nearest scrollport, and overflow-x on the
// container makes overflow-y compute to auto as well — so the container, not
// the page, is that scrollport. Unbounded it grows to fit every row, which
// leaves a header nothing to hold still against and hands the vertical
// scrolling to the page. The height is what makes the pinning real.
//
// The corner cell is sticky on both axes at once, so it has to out-rank both
// the header row it sits in and the actions column passing beneath it.
func TestGridHeaderPinning(t *testing.T) {
	css := builtStylesheet(t)

	for _, want := range []string{
		`.table-container[data-steward-hscroll]{max-height:var(--steward-table-max-height,max(16rem, calc(100dvh - 19.5rem)))}`,
		`.table[data-steward-grid] thead th{z-index:4;background-color:var(--color-card);` +
			`box-shadow:inset 0 -1px 0 var(--color-border);position:sticky;inset-block-start:0}`,
		`.table[data-steward-grid] thead tr:nth-child(2) th{inset-block-start:var(--steward-header-row-h,0px)}`,
		`.table[data-steward-grid] thead th.steward-col-actions{z-index:5}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the built stylesheet is missing %s", want)
		}
	}

	// The hooks both rules select on have to be on the rendered grid.
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/hdr.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&wideRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("grid-header-test-secret-key"),
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
	for _, want := range []string{`data-steward-hscroll`, `data-steward-grid="wide_rows"`} {
		if !strings.Contains(html, want) {
			t.Errorf("the grid is missing the %s hook the stylesheet selects on", want)
		}
	}
}
