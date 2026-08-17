package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

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
	db := testDB(t)
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
	db := testDB(t)
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
	db := testDB(t)
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
//
// It is fixed even while closed, because an absolutely positioned box pads its
// scroll container's scrollable overflow whether or not it is visible: a closed
// menu below the last row left every grid scrollable past the end of its table.
func TestRowMenuStylesheetRules(t *testing.T) {
	css := builtStylesheet(t)

	if !strings.Contains(css, `steward-col-actions:has([aria-expanded=true]){z-index:3}`) {
		t.Error("the built stylesheet is missing the open cell's z-index")
	}

	// The rule has to reach the menu in both places it can be: inside its
	// wrapper, and moved out of the table while it is open. A selector that
	// covers only the first leaves the moved one absolutely positioned against
	// the document and animating its way in.
	i := strings.Index(css, "[data-steward-menu]>[data-popover]")
	if i < 0 {
		t.Fatal("the built stylesheet does not position the row menu at all")
	}
	rule := css[i : i+strings.Index(css[i:], "}")+1]
	for _, want := range []string{
		"[data-popover][data-steward-portal]",
		"position:fixed",
		"transition-property:opacity,transform,visibility",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("the row menu rule is missing %s: %s", want, rule)
		}
	}
}

// TestGridHeaderPinning covers the header row holding still while the rows
// scroll under it, which takes two rules that only work together.
//
// Sticky resolves against the nearest scrollport, and overflow-x on the
// container makes overflow-y compute to auto as well — so the container, not
// the page, is that scrollport. Grown to fit every row it gives a header nothing
// to hold still against, so the container's height is what makes the pinning
// real, and that height is handed down a chain: shell → content pane → page →
// card → section → container. Any one link missing collapses it back to
// content-sized, so every link is asserted.
//
// The corner cell is sticky on both axes at once, so it has to out-rank both
// the header row it sits in and the actions column passing beneath it.
func TestGridHeaderPinning(t *testing.T) {
	css := builtStylesheet(t)

	for _, want := range []string{
		`.table-container[data-steward-hscroll]{max-height:var(--steward-table-max-height,none)}`,
		`.table[data-steward-grid] thead th{z-index:4;background-color:var(--color-card);` +
			`box-shadow:inset 0 -1px 0 var(--color-border);position:sticky;inset-block-start:0}`,
		`.table[data-steward-grid] thead tr:nth-child(2) th{inset-block-start:var(--steward-header-row-h,0px)}`,
		`.table[data-steward-grid] thead th.steward-col-actions{z-index:5}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("the built stylesheet is missing %s", want)
		}
	}

	// The hooks the rules select on, and the height chain, both have to be on
	// the rendered page.
	db := testDB(t)
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
	for _, link := range []struct{ what, class string }{
		{"the shell is the viewport", `<main class="h-dvh flex flex-col">`},
		{"the content pane fills it and can shrink", `id="page-content" class="flex-1 min-h-0 flex flex-col overflow-y-auto"`},
		{"the page fills the pane, card row taking the leftover", `grid-rows-[auto_minmax(0,1fr)] gap-4 flex-1 min-h-0`},
		{"the card is capped by the page rather than stretched", `self-start max-h-full min-h-0`},
		{"the table's section takes the card's leftover", `px-0 min-w-0 flex flex-col flex-auto min-h-0`},
		{"the container takes the section's, on a content basis", `table-container flex-auto min-h-0`},
	} {
		if !strings.Contains(html, link.class) {
			t.Errorf("the height chain is broken where %s (%s)", link.what, link.class)
		}
	}
	// The window cannot scroll, so navigation resets the pane instead.
	if !strings.Contains(html, `hx-swap="innerHTML scroll:#page-content:top"`) {
		t.Error("sidebar navigation still scrolls the window, which no longer scrolls")
	}
}

// TestFilterGridStylesheetRules covers what the panel needs to divide into
// twelve columns, and the thing that broke it while it was being written: a
// [class*=] rule written after the per-span ones takes every span with it,
// because they match at the same specificity and the later one wins.
func TestFilterGridStylesheetRules(t *testing.T) {
	css := builtStylesheet(t)

	if !strings.Contains(css, ".steward-filter-grid") {
		t.Fatal("the built stylesheet has no filter grid")
	}
	// Inside the desktop breakpoint every span must resolve to its own width.
	for n := 1; n <= 12; n++ {
		rule := fmt.Sprintf(".steward-filter-grid>.steward-span-%d{grid-column:span %d}", n, n)
		if !strings.Contains(css, rule) {
			t.Errorf("missing %s", rule)
		}
	}
}
