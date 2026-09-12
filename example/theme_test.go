package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type themeRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

const themeTokens = `:root{--radius:0rem;--primary:oklch(55% 0.2 265)}`

// Every shell the panel serves, including the ones that render without the
// layout: a theme that reaches the grid but not the login page leaves the
// first screen a reader sees on the framework's own colours.
func newThemeServer(t *testing.T, css string) (*steward.Admin, *httptest.Server) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&themeRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("theme-test-secret-key-0123456789"),
		Prefix:    "/admin",
		Brand:     "Themed",
		ThemeCSS:  css,
		// A mailer, because that is what mounts the forgot and reset pages —
		// two of the shells this is checking.
		Mailer: nullMailer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[themeRow](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return app, srv
}

func TestThemeReachesEveryShell(t *testing.T) {
	app, srv := newThemeServer(t, themeTokens)
	seedUser(t, app, "root", "correct-horse")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// The pages that render without a session, each its own document. A reset
	// link with no token renders the same shell behind a 403, which is the page
	// someone following a stale link actually lands on.
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/admin/auth/login", http.StatusOK},
		{"/admin/auth/forgot", http.StatusOK},
		{"/admin/auth/reset", http.StatusForbidden},
	} {
		code, body := getPath(t, client, srv.URL+tc.path)
		if code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, code, tc.want)
			continue
		}
		assertThemed(t, tc.path, body)
	}

	// And the panel itself, behind the login.
	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatal("login failed")
	}
	for _, path := range []string{"/", "/theme_rows", "/auth/profile"} {
		code, body := c.get(path)
		if code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
			continue
		}
		assertThemed(t, path, body)
	}
}

// The tokens have to arrive as CSS and after the stylesheet: escaped, or placed
// before it, they change nothing.
func assertThemed(t *testing.T, path, body string) {
	t.Helper()
	i := strings.Index(body, themeTokens)
	if i < 0 {
		t.Errorf("%s: the theme is not in the page", path)
		return
	}
	link := strings.Index(body, `dist/app.css`)
	if link < 0 {
		t.Errorf("%s: no stylesheet link", path)
		return
	}
	if i < link {
		t.Errorf("%s: the theme is emitted before the stylesheet it overrides", path)
	}
	if strings.Contains(body, "ZgotmplZ") {
		t.Errorf("%s: the theme was escaped rather than emitted as CSS", path)
	}
}

// Nothing is emitted when nothing is configured, so a panel that never themes
// carries no empty style element.
func TestNoThemeNoStyleElement(t *testing.T) {
	_, srv := newThemeServer(t, "")
	code, body := getPath(t, &http.Client{}, srv.URL+"/admin/auth/login")
	if code != http.StatusOK {
		t.Fatalf("login page = %d, want 200", code)
	}
	if strings.Contains(body, "<style>") {
		t.Error("an unset theme still rendered a style element")
	}
}

// nullMailer mounts the password-reset pages without sending anything.
type nullMailer struct{}

func (nullMailer) Send(ctx context.Context, m steward.Mail) error { return nil }

// The customization page shows this, and names the tokens it can set. A token
// that is not in the stylesheet sets nothing, silently.
//
// steward-site/content/docs/customization.md#theme
func TestDocumentedThemeTokensExist(t *testing.T) {
	_, srv := newThemeServer(t, `
		:root {
			--primary: oklch(55% 0.20 265);
			--primary-foreground: oklch(99% 0 0);
			--ring: oklch(55% 0.20 265);
			--radius: 0.25rem;
		}
		.dark { --primary: oklch(70% 0.18 265); }
	`)

	// The stylesheet the panel serves, read the way a browser reads it.
	code, page := getPath(t, &http.Client{}, srv.URL+"/admin/auth/login")
	if code != http.StatusOK {
		t.Fatalf("login page = %d", code)
	}
	href := ""
	if m := themeStylesheet.FindStringSubmatch(page); m != nil {
		href = m[1]
	}
	if href == "" {
		t.Fatal("no stylesheet on the page")
	}
	code, css := getPath(t, &http.Client{}, srv.URL+href)
	if code != http.StatusOK {
		t.Fatalf("stylesheet = %d", code)
	}

	// Every token the page lists, so the table cannot name one the stylesheet
	// stopped defining.
	for _, token := range []string{
		"--background", "--foreground",
		"--card", "--card-foreground",
		"--popover", "--popover-foreground",
		"--primary", "--primary-foreground",
		"--secondary", "--secondary-foreground",
		"--accent", "--accent-foreground",
		"--muted", "--muted-foreground",
		"--destructive", "--border", "--input", "--ring", "--radius",
		"--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5",
		"--sidebar", "--sidebar-foreground",
		"--sidebar-primary", "--sidebar-primary-foreground",
		"--sidebar-accent", "--sidebar-accent-foreground",
		"--sidebar-border", "--sidebar-ring",
		"--sidebar-width", "--sidebar-mobile-width",
		"--scrollbar-width", "--scrollbar-sm-width",
		"--scrollbar-thumb", "--scrollbar-track", "--scrollbar-radius",
		"--check-icon", "--chevron-down-icon", "--chevron-down-icon-50",
		"--font-sans", "--font-mono",
	} {
		if !strings.Contains(css, token+":") {
			t.Errorf("the page documents %s, which the stylesheet does not define", token)
		}
	}
}

var themeStylesheet = regexp.MustCompile(`<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"`)
