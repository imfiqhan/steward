package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// menuEntry carries tag defaults that differ from Go's zero values, the shape a
// legacy schema hands a port: a flag that is on unless switched off, and a
// rank the form never asks for.
type menuEntry struct {
	ID     uint   `gorm:"primaryKey"`
	Title  string `gorm:"not null"`
	Active bool   `gorm:"not null;default:true"`
	Rank   int    `gorm:"not null;default:5"`
}

// TestCreateStoresWhatTheFormSubmitted covers GORM replacing a zero value with
// the column's tag default on insert: a Switch left off was stored on.
func TestCreateStoresWhatTheFormSubmitted(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&menuEntry{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("create-default-test-secret"),
		AuthExcept: []string{"/menu_entries*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[menuEntry](app).Form(func(f *steward.Form[menuEntry]) {
		f.Text("Title")
		f.Switch("Active").Default(true)
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	create := func(values url.Values) {
		t.Helper()
		page, err := client.Get(srv.URL + "/admin/menu_entries/create")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(page.Body)
		_ = page.Body.Close()
		m := comboCSRFRe.FindStringSubmatch(string(raw))
		if m == nil {
			t.Fatal("no CSRF token")
		}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/menu_entries",
			strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-CSRF-Token", m[1])
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("create = %d: %s", resp.StatusCode, b)
		}
	}

	// An unchecked switch submits only its presence marker, as a browser does.
	create(url.Values{"Title": {"off"}, "_present_Active": {"1"}})
	create(url.Values{"Title": {"on"}, "_present_Active": {"1"}, "Active": {"on"}})

	var got []menuEntry
	if err := db.Order("id").Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if got[0].Active {
		t.Error("a Switch left off was stored as on")
	}
	if !got[1].Active {
		t.Error("a Switch turned on was stored as off")
	}
	for _, e := range got {
		if e.Rank != 5 {
			t.Errorf("%s: Rank = %d; a column the form does not carry should keep its default", e.Title, e.Rank)
		}
	}
}
