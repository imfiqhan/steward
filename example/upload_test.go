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

// TestUploadAcceptIsEnforced covers Accept becoming a rule rather than a hint.
// It is an attribute the browser reads; until now the server never did, so a
// field declaring PDF-only stored a .docx or a .zip just the same.
func TestUploadAcceptIsEnforced(t *testing.T) {
	srv, _ := newUploadServer(t)

	// Doc declares Accept("application/pdf").
	for _, name := range []string{"sheet.xlsx", "archive.zip", "photo.png", "notes.txt"} {
		code, body := postUpload(t, srv, "Doc", name, "x")
		if code < 400 {
			t.Errorf("%s was stored on a PDF-only field: %d %s", name, code, body)
		}
	}
	if code, body := postUpload(t, srv, "Doc", "report.pdf", "%PDF-1.4"); code != http.StatusOK {
		t.Errorf("report.pdf = %d: %s", code, body)
	}
	// The message names what the field takes, rather than only refusing.
	if _, body := postUpload(t, srv, "Doc", "sheet.xlsx", "x"); !strings.Contains(body, "PDF") {
		t.Errorf("the refusal does not say what is allowed: %s", body)
	}

	// Image carries Accept("image/*") by default; the wildcard has to match
	// every image extension it already allows.
	for _, name := range []string{"a.png", "a.jpg", "a.jpeg", "a.gif", "a.webp", "a.avif"} {
		if code, body := postUpload(t, srv, "Pic", name, "x"); code != http.StatusOK {
			t.Errorf("%s was refused by the default image accept: %d %s", name, code, body)
		}
	}
}

// TestAcceptResolvesTypesTheSameEverywhere guards the deployment trap: Go types
// an extension from the host's MIME database, which a scratch container has
// none of. An office type has to resolve from the table shipped in the binary,
// or a rule that holds in development rejects the same file in production.
func TestAcceptResolvesTypesTheSameEverywhere(t *testing.T) {
	for _, tc := range []struct {
		accept, ext string
		want        bool
	}{
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx", true},
		{"application/vnd.ms-excel", ".xls", true},
		{"text/csv", ".csv", true},
		{"application/zip", ".zip", true},
		{"application/pdf", ".docx", false},
		// Extensions are the form that never depends on a lookup at all.
		{".docx,.pdf", ".pdf", true},
		{".docx,.pdf", ".png", false},
		{"image/*", ".png", true},
		{"image/*", ".pdf", false},
		{"", ".anything", true},
	} {
		if got := steward.AcceptAllowsForTest(tc.accept, tc.ext); got != tc.want {
			t.Errorf("Accept(%q) with %s = %v, want %v", tc.accept, tc.ext, got, tc.want)
		}
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

// TestFilesFieldHoldsAJSONArray covers the column's shape. The list in the DOM
// and the value in the column are the same thing said twice, so the array is
// rebuilt from the list rather than edited alongside it.
func TestFilesFieldHoldsAJSONArray(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/files.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uploadRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uploadRow{
		Doc: `["docs/20260101-aaaaaaaaaaaa-Notulen.pdf","docs/20260101-bbbbbbbbbbbb-Lampiran.pdf"]`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("files-field-test-secret-key"),
		AuthExcept: []string{"/upload_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[uploadRow](app).Form(func(f *steward.Form[uploadRow]) {
		f.Files("Doc").Dir("docs").MaxFiles(3)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	page := getBody(t, srv.URL+"/admin/upload_rows/1/edit")

	if n := strings.Count(page, "data-steward-upload-item"); n != 2 {
		t.Errorf("the stored array rendered %d rows, want 2", n)
	}
	for _, want := range []string{"Notulen.pdf", "Lampiran.pdf", `data-multiple="1"`, `data-max-files="3"`, "max 3"} {
		if !strings.Contains(page, want) {
			t.Errorf("the field is missing %s", want)
		}
	}
	// A column promoted from File to Files still holds its one path, and losing
	// it on the first edit would be silent.
	if err := db.Model(&uploadRow{}).Where("id = 1").
		Update("doc", "docs/old-single.pdf").Error; err != nil {
		t.Fatal(err)
	}
	page = getBody(t, srv.URL+"/admin/upload_rows/1/edit")
	if n := strings.Count(page, "data-steward-upload-item"); n != 1 {
		t.Errorf("a bare path rendered %d rows, want 1", n)
	}
	if !strings.Contains(page, "old-single.pdf") {
		t.Error("a bare path was dropped rather than shown")
	}
}
