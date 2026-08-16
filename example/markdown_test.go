package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type mdRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string `gorm:"type:text"`
}

func newMarkdownServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&mdRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mdRow{Title: "a", Body: "# Judul\n\n**tebal**"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("markdown-test-secret-key"),
		AuthExcept: []string{"/md_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[mdRow](app)
	res.Form(func(f *steward.Form[mdRow]) {
		f.Text("Title")
		f.Markdown("Body")
	})
	res.Detail(func(d *steward.Detail[mdRow]) {
		d.Field("Title")
		d.Field("Body").Markdown()
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

// postPreview asks the endpoint the Preview tab uses.
func postPreview(t *testing.T, srv *httptest.Server, field, value string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	page, err := client.Get(srv.URL + "/admin/md_rows/create")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	form := url.Values{"value": {value}}
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/admin/md_rows/_preview?field="+field, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", m[1])
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestMarkdownPreviewRendersServerSide holds the preview to the same output the
// detail view produces. Rendering it in the browser instead would mean two
// parsers that agree until they do not.
func TestMarkdownPreviewRendersServerSide(t *testing.T) {
	srv := newMarkdownServer(t)

	code, body := postPreview(t, srv, "Body", "# Judul\n\n**tebal** dan [x](https://example.test)")
	if code != http.StatusOK {
		t.Fatalf("preview = %d: %s", code, body)
	}
	for _, want := range []string{"<h1>Judul</h1>", "<strong>tebal</strong>", `href="https://example.test"`} {
		if !strings.Contains(body, want) {
			t.Errorf("preview is missing %q: %s", want, body)
		}
	}

	// The same markup the detail page shows.
	detail := getBody(t, srv.URL+"/admin/md_rows/1")
	if !strings.Contains(detail, "<h1>Judul</h1>") || !strings.Contains(detail, "<strong>tebal</strong>") {
		t.Error("the detail view did not render the markdown")
	}
	if strings.Contains(detail, "# Judul") {
		t.Error("the detail view still shows the raw source")
	}
}

// TestMarkdownPreviewSanitizes: the endpoint renders whatever it is handed, so
// it is a rendering surface as much as the detail view is.
func TestMarkdownPreviewSanitizes(t *testing.T) {
	srv := newMarkdownServer(t)
	code, body := postPreview(t, srv, "Body",
		"# Judul\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>")
	if code != http.StatusOK {
		t.Fatalf("preview = %d: %s", code, body)
	}
	for _, bad := range []string{"<script", "onerror"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("preview returned %q: %s", bad, body)
		}
	}
}

// TestMarkdownPreviewRejectsOtherFields keeps the endpoint to the kind it is
// for, rather than becoming a general-purpose renderer addressed by name.
func TestMarkdownPreviewRejectsOtherFields(t *testing.T) {
	srv := newMarkdownServer(t)
	for _, field := range []string{"Title", "Nope", ""} {
		if code, body := postPreview(t, srv, field, "x"); code != http.StatusNotFound {
			t.Errorf("field %q = %d (%s), want 404", field, code, body)
		}
	}
}
