package main

import (
	"encoding/json"
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

// Doc exercises the Policy layer; Secret exercises ViewAny denial.
type Doc struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

type Secret struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// docPolicy: anyone may list and view, nobody may create or delete, rows
// titled "locked" may not be updated, rows titled "hidden" are scoped out
// of listings.
type docPolicy struct{ steward.AllowAll[Doc] }

func (docPolicy) Create(*steward.Context) bool           { return false }
func (docPolicy) Update(_ *steward.Context, d *Doc) bool { return d.Title != "locked" }
func (docPolicy) Delete(*steward.Context, *Doc) bool     { return false }
func (docPolicy) Scope(_ *steward.Context, db *gorm.DB) *gorm.DB {
	return db.Where("title <> ?", "hidden")
}

type secretPolicy struct{ steward.AllowAll[Secret] }

func (secretPolicy) ViewAny(*steward.Context) bool { return false }

func newPolicyTestServer(t *testing.T) (*httptest.Server, map[string]uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Doc{}, &Secret{}); err != nil {
		t.Fatal(err)
	}
	docs := []Doc{{Title: "visible"}, {Title: "hidden"}, {Title: "locked"}}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("policy-test-secret-key"),
		// Skip the login dance: policies are orthogonal to authentication.
		AuthExcept: []string{"/docs*", "/secrets*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Doc](app).Policy(docPolicy{})
	steward.Register[Secret](app).Policy(secretPolicy{})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	ids := map[string]uint{}
	for _, d := range docs {
		ids[d.Title] = d.ID
	}
	return srv, ids
}

func get(t *testing.T, client *http.Client, url string, accept string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestPolicyEnforcement(t *testing.T) {
	srv, ids := newPolicyTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	// RowScoper: JSON listing excludes "hidden".
	code, body := get(t, client, base+"/docs", "application/json")
	if code != http.StatusOK {
		t.Fatalf("GET /docs JSON = %d, want 200", code)
	}
	var listing struct {
		Total int64 `json:"total"`
		Items []Doc `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if listing.Total != 2 || len(listing.Items) != 2 {
		t.Errorf("scoped listing = total %d, %d items; want 2/2", listing.Total, len(listing.Items))
	}
	if strings.Contains(body, `"hidden"`) {
		t.Error("scoped-out row leaked into the listing")
	}

	// The create button disappears and the create form is denied.
	code, html := get(t, client, base+"/docs", "")
	if code != http.StatusOK {
		t.Fatalf("GET /docs HTML = %d, want 200", code)
	}
	if strings.Contains(html, "/docs/create") {
		t.Error("create button rendered although Create() is false")
	}
	if code, _ = get(t, client, base+"/docs/create", ""); code != http.StatusForbidden {
		t.Errorf("GET /docs/create = %d, want 403", code)
	}

	// Update: the locked row's edit page is denied, others are fine.
	if code, _ = get(t, client, docURL(base, ids["locked"], "/edit"), ""); code != http.StatusForbidden {
		t.Errorf("edit locked = %d, want 403", code)
	}
	if code, _ = get(t, client, docURL(base, ids["visible"], "/edit"), ""); code != http.StatusOK {
		t.Errorf("edit visible = %d, want 200", code)
	}

	// View is allowed even for scoped-out rows: RowScoper narrows listings,
	// per-row protection is View's job (and this policy allows it).
	if code, _ = get(t, client, docURL(base, ids["hidden"], ""), ""); code != http.StatusOK {
		t.Errorf("show hidden = %d, want 200 (View allows it)", code)
	}

	// Delete: denied by policy — through the real middleware chain, so a
	// CSRF token is required first.
	token := csrfFrom(t, html)
	req, _ := http.NewRequest(http.MethodDelete, docURL(base, ids["visible"], ""), nil)
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE = %d, want 403", resp.StatusCode)
	}
	var count int64
	// The row must still exist.
	code, body = get(t, client, base+"/docs", "application/json")
	_ = code
	if err := json.Unmarshal([]byte(body), &listing); err == nil {
		count = listing.Total
	}
	if count != 2 {
		t.Errorf("rows after denied delete = %d, want 2", count)
	}

	// ViewAny=false denies the whole surface: grid, JSON, schema.
	if code, _ = get(t, client, base+"/secrets", ""); code != http.StatusForbidden {
		t.Errorf("GET /secrets = %d, want 403", code)
	}
	if code, _ = get(t, client, base+"/secrets", "application/json"); code != http.StatusForbidden {
		t.Errorf("GET /secrets JSON = %d, want 403", code)
	}
	if code, _ = get(t, client, base+"/secrets/_schema", "application/json"); code != http.StatusForbidden {
		t.Errorf("GET /secrets/_schema = %d, want 403", code)
	}
}

func docURL(base string, id uint, suffix string) string {
	return base + "/docs/" + itoa(id) + suffix
}

func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

var csrfRe = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

func csrfFrom(t *testing.T, html string) string {
	t.Helper()
	m := csrfRe.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("no csrf-token meta in page")
	}
	return m[1]
}
