package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type cmdPost struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string
}

type cmdSecret struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// hiddenPolicy refuses the listing, which is what the palette has to respect:
// a row the grid would not show must not arrive through search instead.
type hiddenPolicy struct{}

func (hiddenPolicy) ViewAny(*steward.Context) bool            { return false }
func (hiddenPolicy) View(*steward.Context, *cmdSecret) bool   { return false }
func (hiddenPolicy) Create(*steward.Context) bool             { return false }
func (hiddenPolicy) Update(*steward.Context, *cmdSecret) bool { return false }
func (hiddenPolicy) Delete(*steward.Context, *cmdSecret) bool { return false }

func newCommandServer(t *testing.T, withSource bool) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&cmdPost{}, &cmdSecret{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []cmdPost{
		{Title: "Bank Jatim wins an award", Body: "irrelevant"},
		{Title: "Something else entirely", Body: "bank jatim in the body only"},
	} {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&cmdSecret{Name: "bank jatim classified"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("command-test-secret-key-0"),
		AuthExcept: []string{"/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Searchable, over Title only — Body is deliberately left out.
	steward.Register[cmdPost](app).Command("Title").Grid(func(g *steward.Grid[cmdPost]) {
		g.Column("Title")
	})
	// Searchable, but nobody may list it.
	steward.Register[cmdSecret](app).Command("Name").Policy(hiddenPolicy{}).
		Grid(func(g *steward.Grid[cmdSecret]) { g.Column("Name") })

	if withSource {
		app.CommandSource("Help", func(_ *steward.Context, q string) []steward.CommandResult {
			return []steward.CommandResult{{Title: "How to " + q, URL: "/docs/" + q}}
		})
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

func commandSearch(t *testing.T, srv *httptest.Server, q string) []steward.CommandResult {
	t.Helper()
	code, body := getStatus(t, srv.URL+"/admin/_command?q="+strings.ReplaceAll(q, " ", "%20"))
	if code != http.StatusOK {
		t.Fatalf("search %q = %d: %s", q, code, body)
	}
	var out struct {
		Results []steward.CommandResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, body)
	}
	return out.Results
}

// TestCommandSearchesOnlyDeclaredPaths covers the opt-in. Following QuickSearch
// automatically meant every table with a search box was queried on every
// keystroke: measured on one panel, eleven of them cost 1.5–4.2 seconds a press,
// worst on queries that matched nothing.
func TestCommandSearchesOnlyDeclaredPaths(t *testing.T) {
	srv := newCommandServer(t, false)

	got := commandSearch(t, srv, "bank jatim")
	if len(got) != 1 {
		t.Fatalf("want one hit, got %d: %+v", len(got), got)
	}
	if got[0].Title != "Bank Jatim wins an award" {
		t.Errorf("matched the wrong row: %+v", got[0])
	}
	if got[0].Group != "cmd Posts" {
		t.Errorf("group = %q", got[0].Group)
	}
	if !strings.HasSuffix(got[0].URL, "/admin/cmd_posts/1") {
		t.Errorf("url = %q", got[0].URL)
	}
	// The second row holds the phrase in Body, which was not declared.
	for _, r := range got {
		if strings.Contains(r.Title, "Something else") {
			t.Error("a path that was never declared was searched")
		}
	}
}

// TestCommandRespectsThePolicy is the one that matters: the palette must not
// become a way around a resource nobody may list.
func TestCommandRespectsThePolicy(t *testing.T) {
	srv := newCommandServer(t, false)
	for _, r := range commandSearch(t, srv, "bank jatim") {
		if strings.Contains(strings.ToLower(r.Title), "classified") {
			t.Errorf("a row behind a closed policy was returned: %+v", r)
		}
	}
}

// TestCommandNeedsTwoCharacters keeps a single keypress off the database.
func TestCommandNeedsTwoCharacters(t *testing.T) {
	srv := newCommandServer(t, false)
	if got := commandSearch(t, srv, "b"); len(got) != 0 {
		t.Errorf("one character searched anyway: %+v", got)
	}
	if got := commandSearch(t, srv, "ba"); len(got) == 0 {
		t.Error("two characters returned nothing")
	}
}

// TestCommandSource covers the escape hatch for things that are not resources.
func TestCommandSource(t *testing.T) {
	srv := newCommandServer(t, true)
	got := commandSearch(t, srv, "backups")
	var found bool
	for _, r := range got {
		if r.Group == "Help" && r.Title == "How to backups" {
			found = true
		}
	}
	if !found {
		t.Errorf("the registered source did not answer: %+v", got)
	}
}

// TestCommandGroupsAreStable: the list is rebuilt on every keystroke, and a
// section that moves between presses is a section the reader cannot aim at.
func TestCommandGroupsAreStable(t *testing.T) {
	srv := newCommandServer(t, true)
	var first string
	for i := 0; i < 5; i++ {
		var groups []string
		for _, r := range commandSearch(t, srv, "bank") {
			groups = append(groups, r.Group)
		}
		got := fmt.Sprint(groups)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d ordered the groups %s, first run had %s", i, got, first)
		}
	}
}
