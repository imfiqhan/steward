package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// The icon picker and the boot-time check on Resource.Icon. Both exist because
// an unresolved icon renders as blank space at runtime — a defect nobody notices
// until a sidebar looks wrong.

type iconWidget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
	Icon string
}

func newIconServer(t *testing.T, resourceIcon string) (*httptest.Server, *steward.Admin) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&iconWidget{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&iconWidget{Name: "seed", Icon: "folder"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("icon-picker-test-secret-key"),
		AuthExcept: []string{"/icon_widgets*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[iconWidget](app)
	if resourceIcon != "" {
		res.Icon(resourceIcon)
	}
	res.Form(func(f *steward.Form[iconWidget]) {
		f.Text("Name")
		f.Icon("Icon")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, app
}

func TestIconPickerOffersEveryLucideIcon(t *testing.T) {
	srv, app := newIconServer(t, "")
	available := app.Icons()
	// The vendored sprite carries the whole Lucide set.
	if len(available) < 1000 {
		t.Fatalf("expected the full Lucide set, got %d icons", len(available))
	}
	for _, want := range []string{"image", "video", "calendar", "tag", "newspaper", "house"} {
		found := false
		for _, n := range available {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q should be available", want)
		}
	}

	resp, err := http.Get(srv.URL + "/admin/icon_widgets/create")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET create = %d", resp.StatusCode)
	}

	// Every icon is offered as an option of the select that submits. The option
	// carries no value attribute — its text is its value.
	missing := 0
	for _, name := range available {
		if !strings.Contains(html, "<option>"+name+"</option>") {
			if missing < 5 {
				t.Errorf("icon %q is missing from the picker", name)
			}
			missing++
		}
	}
	if missing > 5 {
		t.Errorf("... and %d more missing", missing-5)
	}
	// A "no icon" choice, so a value can be cleared.
	if !strings.Contains(html, `<option value="">`) {
		t.Error("the picker should offer an empty choice")
	}
	// A real select, so the field works with scripting off.
	if !strings.Contains(html, `name="Icon"`) || !strings.Contains(html, "<select") {
		t.Error("the picker should submit through a select")
	}
	// The glyphs must NOT be inlined — 1,600 inline SVGs would add roughly half
	// a megabyte to every render of this form.
	if n := strings.Count(html, "<svg"); n > 50 {
		t.Errorf("%d inline SVGs: the grid should reference the sprite, not inline it", n)
	}
	// It points at the sprite the grid draws from.
	if !strings.Contains(html, "lucide-sprite.svg") {
		t.Error("the picker should reference the vendored sprite")
	}
}

// TestIconPickerIsCollapsedAndShowsSelection covers the two things asked of the
// field: it does not dump a grid into the form, and it shows what is chosen.
func TestIconPickerIsCollapsedAndShowsSelection(t *testing.T) {
	srv, _ := newIconServer(t, "")
	html := fetch(t, srv.URL+"/admin/icon_widgets/1/edit")

	// No grid markup server-side at all — the popover is built on first open.
	if strings.Contains(html, "steward-iconpicker-grid") {
		t.Error("the selection grid should not be rendered by default")
	}
	// The stored icon is inlined once, so the closed field shows it immediately
	// rather than waiting for the sprite to load.
	if !strings.Contains(html, "lucide-folder") {
		t.Error("the selected icon should be shown in the field")
	}
	// And it is the selected option of the select.
	if !strings.Contains(html, "<option selected>folder</option>") {
		t.Error("the stored icon should be the selected option")
	}
	if n := strings.Count(html, "<option selected>"); n != 1 {
		t.Errorf("exactly one option should be selected, found %d", n)
	}
}

// TestLegacyIconAliasesStillResolve guards the upgrade path: Lucide renamed
// these, and a panel that references the old name must not go blank.
func TestLegacyIconAliasesStillResolve(t *testing.T) {
	for _, name := range []string{"home", "news", "filter", "menu-2", "columns", "info-circle"} {
		_, app := newIconServer(t, name)
		if err := app.Verify(); err != nil {
			t.Errorf("legacy alias %q should still resolve: %v", name, err)
		}
	}
}

// fetch GETs a page and returns its body.
func fetch(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	return string(raw)
}

// TestUnknownResourceIconFailsVerify is the guard that would have caught a
// sidebar full of blank icons.
func TestUnknownResourceIconFailsVerify(t *testing.T) {
	_, app := newIconServer(t, "definitely-not-an-icon")
	err := app.Verify()
	if err == nil {
		t.Fatal("an unresolvable resource icon should fail Verify")
	}
	if !strings.Contains(err.Error(), "definitely-not-an-icon") {
		t.Errorf("the error should name the icon: %v", err)
	}
	// And it should say what is available, so the fix is obvious.
	if !strings.Contains(err.Error(), "available:") {
		t.Errorf("the error should list the available icons: %v", err)
	}
}

// TestKnownResourceIconPassesVerify keeps the check from being vacuously strict.
func TestKnownResourceIconPassesVerify(t *testing.T) {
	_, app := newIconServer(t, "news")
	if err := app.Verify(); err != nil {
		t.Errorf("a resolvable icon should pass Verify: %v", err)
	}
}

// TestUnknownIconDoesNotBreakBuild pins the proportionality: a missing icon is
// cosmetic, so it must not stop a panel from serving.
func TestUnknownIconDoesNotBreakBuild(t *testing.T) {
	srv, _ := newIconServer(t, "definitely-not-an-icon")
	resp, err := http.Get(srv.URL + "/admin/icon_widgets")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the grid should still serve with a bad icon, got %d", resp.StatusCode)
	}
}

// TestIconPickerSavesWithoutValueAttribute guards the size optimisation: the
// options omit a value attribute and rely on it defaulting to the option's text.
// If that assumption were wrong, every icon would save as an empty string.
func TestIconPickerSavesWithoutValueAttribute(t *testing.T) {
	srv, _ := newIconServer(t, "")

	html := fetch(t, srv.URL+"/admin/icon_widgets/create")
	// The options really do carry no value attribute.
	if strings.Contains(html, `<option value="calendar"`) {
		t.Error("options should not repeat the name in a value attribute")
	}
	if !strings.Contains(html, "<option>calendar</option>") {
		t.Errorf("expected a bare option for calendar")
	}

	// A submission carrying the name saves it, which is what a browser sends for
	// a valueless option. The CSRF token comes from the page just fetched, whose
	// session cookie the jar below carries.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	_, page0 := getAs(t, client, srv.URL+"/admin/icon_widgets/create", "", "")
	form := url.Values{"Name": {"picked"}, "Icon": {"calendar"}}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/icon_widgets",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrfFrom(t, page0))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create = %d: %s", resp.StatusCode, body)
	}

	// Read it back: the edit form should show it selected and inlined.
	page := fetch(t, srv.URL+"/admin/icon_widgets/2/edit")
	if !strings.Contains(page, "<option selected>calendar</option>") {
		t.Error("the saved icon should come back selected")
	}
	if !strings.Contains(page, "lucide-calendar") {
		t.Error("the saved icon should be inlined in the closed field")
	}
}
