//go:build no_ui

package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// Built without a UI, a panel still answers the JSON API and refuses the HTML
// it cannot draw. Compiling is not the claim; this is.
func TestNoUIServesJSONAndRefusesHTML(t *testing.T) {
	app, err := steward.New(steward.Config{
		DB: testDB(t), SecretKey: []byte("noui-test-secret-key-00000"), Prefix: "/admin",
		AuthExcept: []string{"/log_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB().AutoMigrate(&logRow{}); err != nil {
		t.Fatal(err)
	}
	steward.Register[logRow](app).Grid(func(g *steward.Grid[logRow]) {
		g.Column("Title")
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Create(&logRow{Title: "a row"}).Error; err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// JSON: the same handler, the same data, no templates involved.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/log_rows", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the JSON list = %d: %s", resp.StatusCode, body)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("the JSON list is not JSON: %v\n%s", err, body)
	}
	if !strings.Contains(body, "a row") {
		t.Errorf("the row is missing from the payload: %s", body)
	}

	// HTML: refused, and said so rather than answering a blank page.
	resp, err = client.Get(srv.URL + "/admin/log_rows")
	if err != nil {
		t.Fatal(err)
	}
	html := readBody(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the HTML list = %d, want 503: %s", resp.StatusCode, html)
	}
	if !strings.Contains(html, "no_ui") {
		t.Errorf("the refusal does not say why: %s", html)
	}
}

// Verify has to stay useful in a build with no sprite to check names against.
func TestNoUIVerifyDoesNotReportEveryIcon(t *testing.T) {
	app, err := steward.New(steward.Config{
		DB: testDB(t), SecretKey: []byte("noui-test-secret-key-00000"), Prefix: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB().AutoMigrate(&logRow{}); err != nil {
		t.Fatal(err)
	}
	steward.Register[logRow](app).Icon("news")
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Errorf("Verify reported a panel that is fine: %v", err)
	}
}
