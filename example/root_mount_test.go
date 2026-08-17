package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

var stylesheetHref = regexp.MustCompile(`<link[^>]+rel="stylesheet"[^>]+href="([^"]+)"`)

type Memo struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

// A panel with no Prefix serves from the root, and every path it builds for
// itself must be absolute-from-root rather than carrying an empty segment.
func newRootServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Memo{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Memo{Title: "kept"}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:         db,
		SecretKey:  []byte("root-mount-test-secret-key"),
		Prefix:     prefix,
		AuthExcept: []string{"/memos*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Memo](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

func TestRootIsTheDefaultMount(t *testing.T) {
	srv := newRootServer(t, "")

	// No redirect following: a wrong pattern shows up as a 301 to "//", which
	// a browser reads as a protocol-relative URL to another host.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/auth/login", http.StatusOK}, // the login page
		{"/memos", http.StatusOK},      // a resource grid, exempted from auth
		{"/", http.StatusFound},        // the dashboard, which wants a session
		{"/nope", http.StatusFound},    // unknown, so also sent to the login
	} {
		resp, err := client.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, resp.StatusCode, tc.want)
		}
		// Where an unauthenticated visitor is sent is the sharpest check on a
		// root mount: built by concatenation this reads "//auth/login", which a
		// browser resolves as a URL on a host called "auth".
		if loc := resp.Header.Get("Location"); loc != "" && !strings.HasPrefix(loc, "/auth/login") {
			t.Errorf("GET %s redirected to %q, want a path under /auth/login", tc.path, loc)
		}
	}
}

// The stylesheet is fetched by URL built from the prefix and served by a
// handler that trims that same prefix back off; an empty prefix broke both
// halves independently.
func TestRootServesItsOwnAssets(t *testing.T) {
	for _, prefix := range []string{"", "/admin"} {
		name := prefix
		if name == "" {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			srv := newRootServer(t, prefix)
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar}

			resp, err := client.Get(srv.URL + prefix + "/auth/login")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)

			href := ""
			if m := stylesheetHref.FindStringSubmatch(body); m != nil {
				href = m[1]
			}
			if href == "" {
				t.Fatal("no stylesheet link in the login page")
			}
			if strings.HasPrefix(href, "//") || !strings.HasPrefix(href, "/") {
				t.Fatalf("stylesheet href %q is not rooted at a single slash", href)
			}
			if want := prefix + "/_assets/"; !strings.HasPrefix(href, want) {
				t.Fatalf("stylesheet href %q does not start with %q", href, want)
			}

			css, err := client.Get(srv.URL + href)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = css.Body.Close() }()
			if css.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", href, css.StatusCode)
			}
			if ct := css.Header.Get("Content-Type"); !strings.Contains(ct, "css") {
				t.Fatalf("stylesheet served as %q", ct)
			}
		})
	}
}

// A session cookie with an empty Path is scoped by the browser to the
// directory of the request that set it, so a login on /auth/login would not
// be sent to /notes.
func TestRootSessionCookieIsScopedToTheRoot(t *testing.T) {
	srv := newRootServer(t, "")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	var seen bool
	for _, ck := range resp.Cookies() {
		if ck.Name != "steward_session" {
			continue
		}
		seen = true
		if ck.Path != "/" {
			t.Fatalf("session cookie Path = %q, want %q", ck.Path, "/")
		}
	}
	if !seen {
		t.Skip("login page issued no session cookie")
	}
}

// What `serve` puts in front of the panel. Registering the bare-prefix and
// catch-all patterns unconditionally panicked at the root, where the prefix is
// empty and "" is not a pattern — and the panel never reached a request.
func TestServeMuxRoutesBothMounts(t *testing.T) {
	for _, prefix := range []string{"", "/admin"} {
		name := prefix
		if name == "" {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AutoMigrate(&Memo{}); err != nil {
				t.Fatal(err)
			}
			app, err := steward.New(steward.Config{
				DB:         db,
				SecretKey:  []byte("serve-mux-test-secret-key"),
				Prefix:     prefix,
				AuthExcept: []string{"/memos*"},
			})
			if err != nil {
				t.Fatal(err)
			}
			steward.Register[Memo](app)
			if err := app.Build(); err != nil {
				t.Fatal(err)
			}

			srv := httptest.NewServer(steward.ServeMux(app))
			t.Cleanup(srv.Close)
			client := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}

			// The grid answers through the mux the command builds.
			resp, err := client.Get(srv.URL + prefix + "/memos")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s/memos = %d, want 200", prefix, resp.StatusCode)
			}

			// Under a prefix, the root redirects to the panel; at the root it
			// is the panel.
			resp, err = client.Get(srv.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if prefix == "" {
				if resp.StatusCode == http.StatusNotFound {
					t.Fatal("the root is not served by the panel mounted there")
				}
			} else if loc := resp.Header.Get("Location"); loc != prefix+"/" {
				t.Fatalf("the root redirected to %q, want %q", loc, prefix+"/")
			}
		})
	}
}
