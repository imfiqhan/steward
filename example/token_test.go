package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// Note is a plain resource with no policy, so writes are allowed and the test
// exercises authentication rather than authorization.
type Note struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func newTokenTestServer(t *testing.T) (*httptest.Server, *gorm.DB) {
	t.Helper()
	// A dedicated DSN: the shared-cache in-memory database is process-wide, so
	// reusing one would entangle this with the policy test.
	db, err := gorm.Open(sqlite.Open("file:tokentest?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Note{}); err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:              db,
		SecretKey:       []byte("token-test-secret-key"),
		EnableTokenAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Note](app)
	if err := app.Build(); err != nil { // runs framework migrations; seeds admin/admin
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db
}

// issue mints a token via the public endpoint and returns status + raw value.
func issue(t *testing.T, base, username, password string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username, "password": password, "name": "test-client",
	})
	resp, err := http.Post(base+"/auth/token", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Token
}

// do sends a request with an optional bearer token and no cookie jar at all,
// which is the point: these clients hold no session.
func do(t *testing.T, method, url, token string, body string) (int, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestTokenAuth(t *testing.T) {
	srv, db := newTokenTestServer(t)
	base := srv.URL + "/admin"

	// Wrong credentials are rejected.
	if code, _ := issue(t, base, "admin", "wrong-password"); code != http.StatusUnauthorized {
		t.Errorf("issue with bad password = %d, want 401", code)
	}

	// Correct credentials mint a prefixed token.
	code, token := issue(t, base, "admin", "admin")
	if code != http.StatusCreated {
		t.Fatalf("issue = %d, want 201", code)
	}
	if !strings.HasPrefix(token, "stw_") {
		t.Fatalf("token %q lacks the stw_ prefix", token)
	}

	// Only the hash is persisted.
	var stored struct {
		Hash string
	}
	if err := db.Table("admin_tokens").Select("hash").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Hash == token {
		t.Error("raw token was stored verbatim")
	}
	if len(stored.Hash) != 64 {
		t.Errorf("stored hash is %d chars, want 64 (sha256 hex)", len(stored.Hash))
	}

	// No credential at all is refused.
	if code, _ := do(t, http.MethodGet, base+"/notes", "", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /notes anonymous = %d, want 401", code)
	}

	// A garbage bearer token is refused rather than silently ignored.
	if code, _ := do(t, http.MethodGet, base+"/notes", "stw_not-a-real-token", ""); code != http.StatusUnauthorized {
		t.Errorf("GET /notes with bogus token = %d, want 401", code)
	}

	// The token authenticates a read, with no cookie in play.
	if code, _ := do(t, http.MethodGet, base+"/notes", token, ""); code != http.StatusOK {
		t.Errorf("GET /notes with token = %d, want 200", code)
	}

	// The load-bearing case: a write succeeds with no CSRF token, which a
	// cookie-authenticated caller would need.
	form := url.Values{"Title": {"written-by-token"}}.Encode()
	code, body := do(t, http.MethodPost, base+"/notes", token, form)
	if code >= 400 {
		t.Fatalf("POST /notes with token = %d, want success; body: %s", code, body)
	}
	var n int64
	db.Model(&Note{}).Where("title = ?", "written-by-token").Count(&n)
	if n != 1 {
		t.Errorf("token-authenticated write persisted %d rows, want 1", n)
	}

	// Revoking is logout for a client with no cookie.
	if code, _ := do(t, http.MethodDelete, base+"/auth/token", token, ""); code != http.StatusOK {
		t.Errorf("DELETE /auth/token = %d, want 200", code)
	}
	if code, _ := do(t, http.MethodGet, base+"/notes", token, ""); code != http.StatusUnauthorized {
		t.Errorf("revoked token still works = %d, want 401", code)
	}
}

// TestTokenAuthDisabled proves the feature is opt-in: without the flag the
// endpoint is absent and bearer credentials are ignored.
func TestTokenAuthDisabled(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:tokenoff?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Note{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("token-off-secret-key")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Note](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	base := srv.URL + "/admin"

	if code, _ := issue(t, base, "admin", "admin"); code == http.StatusCreated {
		t.Error("token endpoint is reachable with EnableTokenAuth unset")
	}
}
