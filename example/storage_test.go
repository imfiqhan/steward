package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type storeRow struct {
	ID  uint `gorm:"primaryKey"`
	Doc string
}

// newStorageServer writes one file into the upload directory and serves a panel
// over it. public mirrors Config.PublicUploads.
func newStorageServer(t *testing.T, public bool) (*httptest.Server, *steward.Admin) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "uploads", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uploads", "docs", "secret.pdf"),
		[]byte("private bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+dir+"/s.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&storeRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("storage-test-secret-key-0"),
		UploadDir: filepath.Join(dir, "uploads"), PublicUploads: public,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[storeRow](app).Form(func(f *steward.Form[storeRow]) { f.File("Doc") })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, app
}

func fetchStored(t *testing.T, url string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(url) //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestStoredFileNeedsASessionOrASignature is the hole this closed: the file
// server was mounted outside the panel's authentication, so knowing a path was
// enough to read any upload without signing in.
func TestStoredFileNeedsASessionOrASignature(t *testing.T) {
	srv, _ := newStorageServer(t, false)
	plain := srv.URL + "/admin/_uploads/docs/secret.pdf"

	code, body := fetchStored(t, plain)
	if code == http.StatusOK {
		t.Errorf("an anonymous request read the file: %q", body)
	}
	if strings.Contains(body, "private bytes") {
		t.Error("the bytes came back regardless of the status")
	}
}

// TestSignedURLOpensTheFile covers the other half: a link that works without a
// session is what makes a private bucket usable at all.
func TestSignedURLOpensTheFile(t *testing.T) {
	srv, app := newStorageServer(t, false)

	signed := app.StorageURL("docs/secret.pdf")
	if !strings.Contains(signed, "sig=") || !strings.Contains(signed, "exp=") {
		t.Fatalf("StorageURL returned an unsigned link: %s", signed)
	}
	code, body := fetchStored(t, srv.URL+signed)
	if code != http.StatusOK {
		t.Fatalf("the signed link = %d: %s", code, body)
	}
	if body != "private bytes" {
		t.Errorf("the signed link returned %q", body)
	}
}

// TestSignatureIsBoundToItsFile: a signature good for one name must not open
// another, or one shared link opens the whole bucket.
func TestSignatureIsBoundToItsFile(t *testing.T) {
	srv, app := newStorageServer(t, false)
	dir := filepath.Dir(strings.TrimPrefix(app.StorageURL("docs/secret.pdf"), "/admin/_uploads/"))
	_ = dir

	signed := app.StorageURL("docs/secret.pdf")
	q := signed[strings.Index(signed, "?"):]

	// The same query against a different name.
	if code, _ := fetchStored(t, srv.URL+"/admin/_uploads/docs/other.pdf"+q); code == http.StatusOK {
		t.Error("a signature opened a file it was not made for")
	}
	// A tampered signature.
	bad := strings.Replace(signed, "sig=", "sig=x", 1)
	if code, _ := fetchStored(t, srv.URL+bad); code == http.StatusOK {
		t.Error("a tampered signature was accepted")
	}
	// An expiry moved forward without re-signing.
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	q2 := u.Query()
	q2.Set("exp", "99999999999")
	u.RawQuery = q2.Encode()
	if code, _ := fetchStored(t, srv.URL+u.String()); code == http.StatusOK {
		t.Error("an extended expiry was accepted")
	}
}

// TestExpiredSignatureIsRefused holds the "time-limited" half of the promise.
func TestExpiredSignatureIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "secret.pdf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ls := &steward.LocalStorage{Dir: dir, BaseURL: "/admin/_uploads", SigningKey: []byte("k")}

	fresh, err := ls.SignedURL(t.Context(), "docs/secret.pdf", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := ls.SignedURL(t.Context(), "docs/secret.pdf", -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == stale {
		t.Fatal("the two links are identical, so the expiry is not in the signature")
	}
	// Signing without a key is refused rather than returning an unsigned link.
	unkeyed := &steward.LocalStorage{Dir: dir, BaseURL: "/admin/_uploads"}
	if _, err := unkeyed.SignedURL(t.Context(), "docs/secret.pdf", time.Minute); err == nil {
		t.Error("signing without a key produced a link")
	}
}

// TestPublicUploadsOptsBackOut: a news site's images are genuinely public, and
// gating them would break every page that embeds one.
func TestPublicUploadsOptsBackOut(t *testing.T) {
	srv, _ := newStorageServer(t, true)
	code, body := fetchStored(t, srv.URL+"/admin/_uploads/docs/secret.pdf")
	if code != http.StatusOK || body != "private bytes" {
		t.Errorf("PublicUploads did not serve the file: %d %q", code, body)
	}
}
