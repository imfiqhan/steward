package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// An upload is user-supplied bytes served from the panel's own origin. The file
// server types them from their extension, so an uploaded .html came back as
// text/html and ran its script as the panel — an editor could hand an
// administrator a link that acts as them.

type uploadRow struct {
	ID  uint `gorm:"primaryKey"`
	Doc string
	Pic string
}

func newUploadServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open("file:"+dir+"/up.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uploadRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uploadRow{}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("upload-test-secret-key"),
		UploadDir:  filepath.Join(dir, "uploads"),
		AuthExcept: []string{"/upload_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[uploadRow](app)
	res.Form(func(f *steward.Form[uploadRow]) {
		f.File("Doc").Accept("application/pdf")
		f.Image("Pic")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, dir
}

// post uploads one file and returns the decoded reply.
func postUpload(t *testing.T, srv *httptest.Server, field, name, body string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	page, err := client.Get(srv.URL + "/admin/upload_rows/create")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token")
	}

	req, err := http.NewRequest("POST",
		srv.URL+"/admin/upload_rows/_upload?field="+field, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-CSRF-Token", m[1])
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestUploadRefusesActiveContent covers the extension check. The disposition
// header below is the boundary; this refuses outright, so a File field cannot
// become a page-hosting service by accident.
func TestUploadRefusesActiveContent(t *testing.T) {
	srv, _ := newUploadServer(t)

	for _, name := range []string{"x.html", "x.svg", "x.js", "x.xhtml", "x.php"} {
		code, body := postUpload(t, srv, "Doc", name, "<script>alert(1)</script>")
		if code < 400 || strings.Contains(body, `"status":true`) {
			t.Errorf("%s was accepted on a File field: %d %s", name, code, body)
		}
	}
	// An ordinary document still goes through.
	if code, body := postUpload(t, srv, "Doc", "report.pdf", "%PDF-1.4"); code != http.StatusOK {
		t.Errorf("report.pdf = %d: %s", code, body)
	}
	// An Image field keeps its own, narrower rule.
	if code, _ := postUpload(t, srv, "Pic", "x.html", "<script>"); code < 400 {
		t.Error("an Image field accepted .html")
	}
	if code, body := postUpload(t, srv, "Pic", "photo.png", "\x89PNG"); code != http.StatusOK {
		t.Errorf("photo.png = %d: %s", code, body)
	}
}

// TestUploadsAreServedInert covers the boundary itself, for the files already
// on disk from before the check existed and for anything the check misses.
func TestUploadsAreServedInert(t *testing.T) {
	srv, dir := newUploadServer(t)

	// Written directly, standing in for a file stored before the rule existed.
	sub := filepath.Join(dir, "uploads", "legacy")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "old.html"),
		[]byte("<script>alert(document.domain)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/admin/_uploads/legacy/old.html")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Errorf("Content-Disposition = %q; a visit to this renders it as a page", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}
