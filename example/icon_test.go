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
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/icon.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
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

func TestIconPickerOffersOnlyResolvableIcons(t *testing.T) {
	srv, app := newIconServer(t, "")
	available := app.Icons()
	if len(available) < 10 {
		t.Fatalf("expected a populated icon set, got %d", len(available))
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

	// Every available icon is offered, as a radio carrying its name.
	for _, name := range available {
		if !strings.Contains(html, `value="`+name+`"`) {
			t.Errorf("icon %q is missing from the picker", name)
		}
	}
	// The glyphs are inlined, not just the names.
	if strings.Count(html, "<svg") < len(available) {
		t.Errorf("expected at least %d inlined glyphs, got %d",
			len(available), strings.Count(html, "<svg"))
	}
	// A "no icon" choice, so a value can be cleared.
	if !strings.Contains(html, `value=""`) {
		t.Error("the picker should offer an empty choice")
	}
	// Radios rather than a scripted widget, so the field works without JS.
	if !strings.Contains(html, `type="radio" name="Icon"`) {
		t.Error("the picker should submit through radio inputs")
	}
}

func TestIconPickerMarksCurrentValue(t *testing.T) {
	srv, _ := newIconServer(t, "")
	resp, err := http.Get(srv.URL + "/admin/icon_widgets/1/edit")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	// The seeded row holds "folder", so exactly that radio is checked.
	i := strings.Index(html, `value="folder"`)
	if i < 0 {
		t.Fatal("the stored icon is not in the picker")
	}
	if !strings.Contains(html[i:min(i+80, len(html))], "checked") {
		t.Error("the stored icon should be pre-selected")
	}
	if strings.Count(html, "checked") != 1 {
		t.Errorf("exactly one radio should be checked, found %d", strings.Count(html, "checked"))
	}
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
