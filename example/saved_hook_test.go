package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type memo struct {
	ID    uint `gorm:"primaryKey"`
	Title string

	Notes []memoNote `gorm:"foreignKey:MemoID"`
}

type memoNote struct {
	ID     uint `gorm:"primaryKey"`
	MemoID uint `gorm:"index"`
	Body   string
}

// Saved runs after the row and its nested rows are written, so a check that
// depends on those rows has nowhere else to run — and what it finds has to
// reach the reader rather than the log alone.
func newSavedServer(t *testing.T, hook func(*steward.Context, *memo, bool) error) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&memo{}, &memoNote{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("saved-hook-test-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[memo](app).
		Slug("memos").
		Form(func(f *steward.Form[memo]) {
			f.Text("Title")
			steward.HasMany(f, "Notes", "MemoID", func(cf *steward.Form[memoNote]) {
				cf.Text("Body")
			}).Label("Catatan")
			if hook != nil {
				f.Saved(hook)
			}
		})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")
	return srv
}

func createMemo(t *testing.T, srv *httptest.Server) (int, map[string]any) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	page := ""
	if _, body := getPath(t, client, srv.URL+"/admin/memos/create"); true {
		page = body
	}
	m := notifyTokenRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token on the create form")
	}
	resp, err := client.PostForm(srv.URL+"/admin/memos", url.Values{
		"_token": {m[1]}, "Title": {"A memo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	var env map[string]any
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("the save did not answer an envelope: %.200s", body)
	}
	return resp.StatusCode, env
}

func TestSavedHookErrorReachesTheReader(t *testing.T) {
	srv := newSavedServer(t, func(*steward.Context, *memo, bool) error {
		return errors.New("a page has no questions")
	})
	code, env := createMemo(t, srv)
	if code != http.StatusOK {
		t.Fatalf("save = %d, want 200: the row was written", code)
	}
	data, _ := env["data"].(map[string]any)
	if data == nil {
		t.Fatalf("no data in the envelope: %v", env)
	}
	if data["type"] != "warning" {
		t.Errorf("envelope type = %v, want warning — the row saved, the check did not pass", data["type"])
	}
	if !strings.Contains(joinValues(data), "a page has no questions") {
		t.Errorf("the hook's words are nowhere in the envelope: %v", data)
	}
	// The redirect still happens: the row exists, so leaving the reader on the
	// form would suggest it did not.
	then, _ := data["then"].(map[string]any)
	if then == nil || then["action"] != "redirect" {
		t.Errorf("a saved row should still redirect, got %v", data["then"])
	}
}

func TestSavedHookSilenceIsStillSuccess(t *testing.T) {
	srv := newSavedServer(t, func(*steward.Context, *memo, bool) error { return nil })
	code, env := createMemo(t, srv)
	if code != http.StatusOK {
		t.Fatalf("save = %d, want 200", code)
	}
	data, _ := env["data"].(map[string]any)
	if data["type"] != "success" {
		t.Errorf("envelope type = %v, want success", data["type"])
	}
}

// The repeater's heading is the panel's to choose: a Go field name is English
// whatever language the panel is written in.
func TestRepeaterTakesItsLabel(t *testing.T) {
	srv := newSavedServer(t, nil)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	code, body := getPath(t, client, srv.URL+"/admin/memos/create")
	if code != http.StatusOK {
		t.Fatalf("create form = %d, want 200", code)
	}
	if !strings.Contains(body, "<legend>Catatan</legend>") {
		t.Error("the repeater did not take the label it was given")
	}
	if strings.Contains(body, "<legend>Notes</legend>") {
		t.Error("the Go field name is still the heading")
	}
	if !strings.Contains(body, "Add Catatan") {
		t.Error("the add button still names the relation rather than the label")
	}
}

func joinValues(m map[string]any) string {
	var b strings.Builder
	for _, v := range m {
		if s, ok := v.(string); ok {
			b.WriteString(s)
			b.WriteString(" ")
		}
	}
	return b.String()
}
