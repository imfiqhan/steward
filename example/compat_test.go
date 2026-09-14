package main

import (
	"net/http"
	"net/http/cookiejar"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// Admin and Panel are one type, so a package written against either compiles
// against the other and the two interoperate.
func TestAdminAliasesPanel(t *testing.T) {
	app, err := steward.New(steward.Config{
		DB: testDB(t), SecretKey: []byte("compat-test-secret-key-000"), Prefix: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	var old *steward.Admin = app //nolint:staticcheck // the deprecated name is what this checks
	if old != app {
		t.Fatal("the two names hold different values")
	}
	// A function declared against the old name takes a panel built under the new.
	take := func(a *steward.Admin) *steward.Panel { return a } //nolint:staticcheck // as above
	if take(app) != app {
		t.Error("a *Panel does not pass as a *Admin")
	}
}

// A field cannot be aliased, so Context carries both and they are one pointer.
func TestContextCarriesBothNames(t *testing.T) {
	app, err := steward.New(steward.Config{
		DB: testDB(t), SecretKey: []byte("compat-test-secret-key-000"), Prefix: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	steward.Register[logRow](app).Page("GET", "both", func(c *steward.Context) error {
		seen = true
		if c.Panel == nil {
			t.Error("Context.Panel is nil")
		}
		if c.Admin != c.Panel { //nolint:staticcheck // as above
			t.Error("Context.Admin and Context.Panel are different panels")
		}
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	resp, err := client.Get(srv.URL + "/admin/log_rows/both")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET the page = %d", resp.StatusCode)
	}
	if !seen {
		t.Fatal("the handler never ran")
	}
}
