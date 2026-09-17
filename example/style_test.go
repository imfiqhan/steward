package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type styleRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func newStyleServer(t *testing.T, style steward.Style) (*steward.Panel, *httptest.Server) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&styleRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("style-test-secret-key-0123456789"),
		Prefix:    "/admin",
		Brand:     "Styled",
		Style:     style,
		Mailer:    nullMailer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[styleRow](app)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return app, srv
}

// The pack has to reach the standalone shells too. A login page drawn in one
// style and the grid behind it in another is the first thing a reader sees.
func TestStyleReachesEveryShell(t *testing.T) {
	app, srv := newStyleServer(t, steward.StyleMaia)
	seedUser(t, app, "root", "correct-horse")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// The shells that render without a session, each its own document.
	for _, path := range []string{"/admin/auth/login", "/admin/auth/forgot"} {
		code, body := getPath(t, client, srv.URL+path)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
			continue
		}
		assertStyled(t, path, body, steward.StyleMaia)
	}

	// And the panel behind the login.
	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatal("login failed")
	}
	for _, path := range []string{"/", "/style_rows"} {
		code, body := c.get(path)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
			continue
		}
		assertStyled(t, path, body, steward.StyleMaia)
	}
}

// An unset style draws the panel the way every panel built before the option
// existed was drawn.
func TestUnsetStyleIsTheDefault(t *testing.T) {
	_, srv := newStyleServer(t, "")
	code, body := getPath(t, &http.Client{}, srv.URL+"/admin/auth/login")
	if code != http.StatusOK {
		t.Fatalf("login page = %d, want 200", code)
	}
	assertStyled(t, "login", body, steward.DefaultStyle)
}

// New refuses a pack it has no bundle for, rather than serving another one.
func TestUnknownStyleIsRefused(t *testing.T) {
	_, err := steward.New(steward.Config{
		DB:        testDB(t),
		SecretKey: []byte("style-test-secret-key-0123456789"),
		Style:     "maya",
	})
	if err == nil {
		t.Fatal("an unknown style was accepted")
	}
	if !strings.Contains(err.Error(), "maya") {
		t.Errorf("error %q does not name the style given", err)
	}
}

// The pack carries the component layer and the shared bundle carries what it
// reads, so a page that links them the other way round draws components from
// tokens that are not there yet.
func assertStyled(t *testing.T, where, body string, want steward.Style) {
	t.Helper()
	shared := strings.Index(body, "dist/app.css")
	if shared < 0 {
		t.Errorf("%s: no shared stylesheet link", where)
		return
	}
	pack := strings.Index(body, "dist/style-"+want.String()+".css")
	if pack < 0 {
		t.Errorf("%s: page does not link the %s pack", where, want)
		return
	}
	if pack < shared {
		t.Errorf("%s: the %s pack is linked before the bundle it builds on", where, want)
	}
	for _, other := range steward.Styles() {
		if other == want {
			continue
		}
		if strings.Contains(body, "dist/style-"+other.String()+".css") {
			t.Errorf("%s: page links %s as well as %s; two packs are in force", where, other, want)
		}
	}
}
