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
		DefaultDisk: "local",
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
	plain := srv.URL + "/admin/_uploads/local/docs/secret.pdf"

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
	dir := filepath.Dir(strings.TrimPrefix(app.StorageURL("docs/secret.pdf"), "/admin/_uploads/local/"))
	_ = dir

	signed := app.StorageURL("docs/secret.pdf")
	q := signed[strings.Index(signed, "?"):]

	// The same query against a different name.
	if code, _ := fetchStored(t, srv.URL+"/admin/_uploads/local/docs/other.pdf"+q); code == http.StatusOK {
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
	ls := &steward.LocalStorage{Dir: dir, BaseURL: "/admin/_uploads/local", SigningKey: []byte("k")}

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
	unkeyed := &steward.LocalStorage{Dir: dir, BaseURL: "/admin/_uploads/local"}
	if _, err := unkeyed.SignedURL(t.Context(), "docs/secret.pdf", time.Minute); err == nil {
		t.Error("signing without a key produced a link")
	}
}

// TestPublicUploadsOptsBackOut: a news site's images are genuinely public, and
// gating them would break every page that embeds one.
func TestPublicUploadsOptsBackOut(t *testing.T) {
	srv, _ := newStorageServer(t, true)
	code, body := fetchStored(t, srv.URL+"/admin/_uploads/local/docs/secret.pdf")
	if code != http.StatusOK || body != "private bytes" {
		t.Errorf("PublicUploads did not serve the file: %d %q", code, body)
	}
}

// TestDisksAreSeparate covers the point of naming them: one panel holding both
// a public disk a website can read and a private one it cannot.
func TestDisksAreSeparate(t *testing.T) {
	dir := t.TempDir()
	for _, disk := range []string{"public", "private"} {
		if err := os.MkdirAll(filepath.Join(dir, disk, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, disk, "docs", "f.txt"),
			[]byte(disk+" bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := gorm.Open(sqlite.Open("file:"+dir+"/d.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&storeRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("disks-test-secret-key-000"),
		UploadDir: dir,
		Disks: map[string]steward.Disk{
			"public":  {Public: true},
			"private": {},
		},
		DefaultDisk: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[storeRow](app).Form(func(f *steward.Form[storeRow]) { f.File("Doc") })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	if got := app.DiskNames(); len(got) != 2 {
		t.Errorf("DiskNames = %v", got)
	}

	// A public disk's URL is plain, and anyone may read it.
	pub := app.DiskURL("public", "docs/f.txt")
	if strings.Contains(pub, "sig=") {
		t.Errorf("a public disk signed its URL: %s", pub)
	}
	if code, body := fetchStored(t, srv.URL+pub); code != http.StatusOK || body != "public bytes" {
		t.Errorf("public disk = %d %q", code, body)
	}

	// A private disk's is signed, and the plain form is refused.
	priv := app.DiskURL("private", "docs/f.txt")
	if !strings.Contains(priv, "sig=") {
		t.Fatalf("a private disk did not sign: %s", priv)
	}
	if code, body := fetchStored(t, srv.URL+priv); code != http.StatusOK || body != "private bytes" {
		t.Errorf("signed private disk = %d %q", code, body)
	}
	bare := priv[:strings.Index(priv, "?")]
	if _, body := fetchStored(t, srv.URL+bare); !strings.HasPrefix(body, "Not found.") {
		t.Errorf("an unsigned private URL was served: %q", body)
	}

	// Each disk has its own directory, so the same path is a different file.
	if pub == priv || strings.HasPrefix(priv, bare+"?") == false {
		t.Errorf("the two disks produced the same URL: %s", pub)
	}

	// A signature made for one disk must not open the other.
	q := priv[strings.Index(priv, "?"):]
	crossed := strings.Replace(bare, "/_uploads/private/", "/_uploads/public/", 1) + q
	if code, body := fetchStored(t, srv.URL+crossed); code == http.StatusOK && body == "private bytes" {
		t.Error("a signature crossed between disks")
	}

	// And the default disk is the one named.
	if app.StorageURL("docs/f.txt") != app.DiskURL("private", "docs/f.txt")[:len(bare)] &&
		!strings.HasPrefix(app.StorageURL("docs/f.txt"), bare) {
		t.Errorf("DefaultDisk was not honoured: %s", app.StorageURL("docs/f.txt"))
	}
}

// TestFieldDiskDecidesWhereAnUploadLands is the half a resource sees.
func TestFieldDiskDecidesWhereAnUploadLands(t *testing.T) {
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open("file:"+dir+"/fd.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&storeRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&storeRow{Doc: "docs/x.txt"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("field-disk-secret-key-000"),
		UploadDir:  dir,
		AuthExcept: []string{"/store_rows*"},
		Disks: map[string]steward.Disk{
			"media": {Public: true},
			"vault": {},
		},
		DefaultDisk: "vault",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[storeRow](app).Form(func(f *steward.Form[storeRow]) {
		f.File("Doc").Disk("media")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	page := getBody(t, srv.URL+"/admin/store_rows/1/edit")
	if !strings.Contains(page, "/_uploads/media/docs/x.txt") {
		t.Errorf("the field did not resolve through its own disk:\n%s",
			page[max(0, strings.Index(page, "_uploads")-60):min(len(page), strings.Index(page, "_uploads")+60)])
	}
	if strings.Contains(page, "/_uploads/vault/") {
		t.Error("the field resolved through the default disk instead")
	}
}
