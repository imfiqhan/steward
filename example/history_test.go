package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type hsRow struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	Secret    string
	Live      bool
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type hsPlain struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func newHistoryPanel(t *testing.T) (*steward.Panel, *gorm.DB, *opClient) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&hsRow{}, &hsPlain{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{Prefix: "/admin", DB: db, SecretKey: []byte("history-test-secret-key-0000")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[hsRow](app).History().
		Grid(func(g *steward.Grid[hsRow]) {
			g.Column("Title")
			g.Column("Live").Switch()
		}).
		Form(func(f *steward.Form[hsRow]) {
			f.Text("Title").Rules("required")
			f.Password("Secret")
			f.Switch("Live")
		})
	steward.Register[hsPlain](app)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	all := steward.Permission{Name: "All", Slug: "hs-all", HTTPPath: "*"}
	if err := db.Create(&all).Error; err != nil {
		t.Fatal(err)
	}
	addUser(t, app, "editor", []steward.Permission{all})
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return app, db, signInAs(t, srv, "editor")
}

func formBody(v url.Values) *strings.Reader { return strings.NewReader(v.Encode()) }

func historyOf(t *testing.T, app *steward.Panel, slug, key string) []steward.RecordChange {
	t.Helper()
	entries, err := app.History(t.Context(), slug, key, 50)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestHistoryRecordsEveryChange(t *testing.T) {
	app, db, c := newHistoryPanel(t)
	const form = "application/x-www-form-urlencoded"

	if code, body := c.do("POST", "/hs_rows", form, formBody(url.Values{"Title": {"First"}, "Secret": {"hunter22"}})); code != http.StatusOK {
		t.Fatalf("create = %d %s", code, body)
	}
	// A save that changes nothing writes nothing.
	c.do("PUT", "/hs_rows/1", form, formBody(url.Values{"Title": {"First"}}))
	if code, body := c.do("PUT", "/hs_rows/1", form, formBody(url.Values{"Title": {"Second"}})); code != http.StatusOK {
		t.Fatalf("update = %d %s", code, body)
	}
	// An inline switch goes through the same save.
	c.do("PUT", "/hs_rows/1", form, formBody(url.Values{"Live": {"on"}, "_present_Live": {"1"}, "_inline": {"1"}}))
	c.do("DELETE", "/hs_rows/1", "", nil)
	c.do("POST", "/hs_rows/_restore", form, formBody(url.Values{"ids": {"1"}}))

	entries := historyOf(t, app, "hs_rows", "1")
	var actions []string
	for i := len(entries) - 1; i >= 0; i-- {
		actions = append(actions, entries[i].Action)
	}
	want := []string{"created", "updated", "updated", "deleted", "restored"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %v, want %v", actions, want)
	}

	var editor steward.User
	db.Where("username = ?", "editor").First(&editor)
	created := entries[len(entries)-1]
	if created.UserID != editor.ID {
		t.Errorf("the entry should name the account that made it: %d", created.UserID)
	}
	ch := created.FieldChanges()
	if ch["Title"].After != "First" || ch["Title"].Before != nil {
		t.Errorf("a create should record the value it wrote: %+v", ch["Title"])
	}
	if s, _ := ch["Secret"].After.(string); strings.Contains(s, "hunter") || s == "" {
		t.Errorf("a password must be masked in history, got %q", s)
	}
	retitled := entries[len(entries)-2].FieldChanges()
	if retitled["Title"].Before != "First" || retitled["Title"].After != "Second" || len(retitled) != 1 {
		t.Errorf("an update should record only what moved, before and after: %+v", retitled)
	}
	if live := entries[len(entries)-3].FieldChanges()["Live"]; live.Before != false || live.After != true {
		t.Errorf("the inline switch = %+v", live)
	}

	detail := htmlGet(t, c, "/hs_rows/1")
	for _, want := range []string{"data-steward-history", "editor", "First", "Second", "Restored"} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail page's history lacks %q", want)
		}
	}

	// Permanent delete is the last thing history can say about a row.
	c.do("DELETE", "/hs_rows/1", "", nil)
	c.do("POST", "/hs_rows/_force_delete", form, formBody(url.Values{"ids": {"1"}}))
	if e := historyOf(t, app, "hs_rows", "1"); e[0].Action != steward.ChangeForceDeleted {
		t.Errorf("latest entry after a permanent delete = %s", e[0].Action)
	}
}

func TestHistoryIsOptIn(t *testing.T) {
	app, _, c := newHistoryPanel(t)
	c.do("POST", "/hs_plains", "application/x-www-form-urlencoded", formBody(url.Values{"Title": {"x"}}))
	if e := historyOf(t, app, "hs_plains", "1"); len(e) != 0 {
		t.Errorf("a resource without History() recorded %d entries", len(e))
	}
	if strings.Contains(htmlGet(t, c, "/hs_plains/1"), "data-steward-history") {
		t.Error("a resource without History() should show no history card")
	}
}

// htmlGet fetches a page as a browser would, not as JSON.
func htmlGet(t *testing.T, c *opClient, path string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
