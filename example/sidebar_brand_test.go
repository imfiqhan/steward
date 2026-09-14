package main

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

func sidebarOf(t *testing.T, cfg steward.Config) string {
	t.Helper()
	cfg.DB = testDB(t)
	cfg.SecretKey = []byte("sidebar-brand-test-secret0")
	cfg.Prefix = "/admin"
	app, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[logRow](app).Title("Records").Icon("list")
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	resp, err := client.Get(srv.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	return readBody(t, resp)
}

// The rail shows marks, not labels, so a panel that configured no icon would
// collapse to a column of blanks. The brand's first letter stands in.
func TestBrandFallsBackToItsInitial(t *testing.T) {
	html := sidebarOf(t, steward.Config{Brand: "Kominfo"})
	if !strings.Contains(html, `class="steward-brand-mark"`) {
		t.Fatal("the sidebar rendered no brand mark")
	}
	if !strings.Contains(html, ">K<") && !strings.Contains(html, ">\n          K\n        <") {
		t.Errorf("the brand's initial is not in the mark: %s", brandMarkOf(html))
	}
}

func TestBrandIconReplacesTheInitial(t *testing.T) {
	html := sidebarOf(t, steward.Config{Brand: "Kominfo", BrandIcon: "newspaper"})
	mark := brandMarkOf(html)
	if !strings.Contains(mark, "<svg") {
		t.Errorf("BrandIcon did not reach the mark: %s", mark)
	}
	if strings.Contains(mark, ">K<") {
		t.Errorf("the initial is still there beside the icon: %s", mark)
	}
}

// A label the rail hides still has to name its icon on hover, and the entry's
// own mark is what a menu item without an icon falls back to.
func TestMenuEntriesCarryATitleAndALabel(t *testing.T) {
	html := sidebarOf(t, steward.Config{Brand: "Kominfo"})
	if !strings.Contains(html, `title="Records"`) {
		t.Error("a menu entry carries no title for the rail to show on hover")
	}
	if !strings.Contains(html, `class="steward-menu-label">Records<`) {
		t.Error("a menu entry's label is not in a class the rail can hide")
	}
}

// An unknown BrandIcon renders blank, which on a rail is the whole mark. Verify
// reports it the way it reports a resource's.
func TestVerifyCatchesAnUnknownBrandIcon(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("sidebar-brand-test-secret0"), Prefix: "/admin",
		Brand: "Kominfo", BrandIcon: "newspapr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	err = app.Verify()
	if err == nil {
		t.Fatal("an unknown BrandIcon passed Verify")
	}
	for _, want := range []string{`BrandIcon "newspapr"`, "did you mean: newspaper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report is missing %q: %v", want, err)
		}
	}
}

// brandMarkOf returns just the brand mark element, for an assertion that should
// not be satisfied by markup elsewhere on the page.
func brandMarkOf(html string) string {
	i := strings.Index(html, `class="steward-brand-mark"`)
	if i < 0 {
		return ""
	}
	rest := html[i:]
	if j := strings.Index(rest, "</span>"); j >= 0 {
		return rest[:j]
	}
	return rest
}
