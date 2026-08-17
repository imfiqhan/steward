package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// Article exercises Form.Richtext and Field.Show together: Body holds HTML,
// Status is a privileged field only an editor may set.
type Article struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Body   string `gorm:"type:text"`
	Status string `gorm:"size:20"`
}

// showTestRole switches what the request's "user" may do. The auth layer is
// bypassed in this harness, so the predicate reads a header instead of
// c.User.HasRole — the mechanism under test is the gating, not how the role is
// discovered.
func isEditor(c *steward.Context) bool {
	return c.R.Header.Get("X-Test-Role") == "editor"
}

func newShowTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&Article{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Article{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Article{Title: "seed", Body: "<p>seed</p>", Status: "draft"}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix:     "/admin",
		DB:         db,
		SecretKey:  []byte("richtext-show-test-secret-key"),
		AuthExcept: []string{"/articles*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[Article](app)
	res.Form(func(f *steward.Form[Article]) {
		f.Text("Title").Rules("required")
		f.Richtext("Body")
		f.Select("Status").Options(steward.Options{"draft": "Draft", "published": "Published"}).
			Default("draft").Show(isEditor)
		// A field hidden by Show never decodes, so nothing writes the column.
		// This is the documented way to supply the value it would have carried.
		f.Saving(func(c *steward.Context, a *Article) error {
			if !isEditor(c) {
				a.Status = "draft"
			}
			return nil
		})
	})
	res.Detail(func(d *steward.Detail[Article]) {
		d.Field("Title")
		d.Field("Body").HTML()
		d.Field("Status")
	})
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

// post submits a form as the given role and returns status plus body. The CSRF
// token is read from a freshly rendered page, which also seeds the session
// cookie the token is bound to.
func post(t *testing.T, client *http.Client, base, target, role string, form url.Values) (int, string) {
	t.Helper()
	_, page := getAs(t, client, base+"/articles/create", role, "")
	req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", csrfFrom(t, page))
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func getAs(t *testing.T, client *http.Client, target, role, accept string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if role != "" {
		req.Header.Set("X-Test-Role", role)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// TestShowHidesFieldFromForm covers the visible half: the field renders for one
// role and not the other.
func TestShowHidesFieldFromForm(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	_, editorHTML := getAs(t, client, base+"/articles/create", "editor", "")
	if !strings.Contains(editorHTML, `name="Status"`) {
		t.Error("an editor should see the Status field")
	}
	_, writerHTML := getAs(t, client, base+"/articles/create", "writer", "")
	if strings.Contains(writerHTML, `name="Status"`) {
		t.Error("a writer should not see the Status field")
	}
	// Both roles still get the unrestricted fields.
	for _, html := range []string{editorHTML, writerHTML} {
		if !strings.Contains(html, `name="Title"`) || !strings.Contains(html, `name="Body"`) {
			t.Error("unrestricted fields should render for every role")
		}
	}
}

// TestShowRejectsForgedSubmission is the property that matters: hiding a field
// must also refuse to write it, or the gate is decoration.
func TestShowRejectsForgedSubmission(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	code, body := post(t, client, base, base+"/articles", "writer", url.Values{
		"Title":  {"Forged"},
		"Body":   {"<p>hello</p>"},
		"Status": {"published"}, // not offered to this role
	})
	if code != http.StatusOK {
		t.Fatalf("create as writer = %d (%s), want 200", code, body)
	}

	// Read it back as an editor, who can see Status.
	_, listing := getAs(t, client, base+"/articles", "editor", "application/json")
	var out struct {
		Items []Article `json:"items"`
	}
	if err := json.Unmarshal([]byte(listing), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	var forged *Article
	for i := range out.Items {
		if out.Items[i].Title == "Forged" {
			forged = &out.Items[i]
		}
	}
	if forged == nil {
		t.Fatal("the created row is missing")
	}
	if forged.Status == "published" {
		t.Error("a hidden field was written from a forged submission")
	}
	// The Saving hook supplied the value the hidden field would have carried.
	if forged.Status != "draft" {
		t.Errorf("Status = %q, want the Saving hook's %q", forged.Status, "draft")
	}
}

// TestShowAllowsPrivilegedSubmission confirms the gate is not simply always
// closed.
func TestShowAllowsPrivilegedSubmission(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	code, body := post(t, client, base, base+"/articles", "editor", url.Values{
		"Title":  {"Published by editor"},
		"Body":   {"<p>ok</p>"},
		"Status": {"published"},
	})
	if code != http.StatusOK {
		t.Fatalf("create as editor = %d (%s), want 200", code, body)
	}
	_, listing := getAs(t, client, base+"/articles", "editor", "application/json")
	if !strings.Contains(listing, `"published"`) {
		t.Error("an editor's Status value should have been saved")
	}
}

// TestSchemaReflectsShow keeps the headless contract honest: a client should not
// be told about a field its submissions cannot write.
func TestSchemaReflectsShow(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	_, editorSchema := getAs(t, client, base+"/articles/_schema", "editor", "application/json")
	if !strings.Contains(editorSchema, `"Status"`) {
		t.Error("an editor's schema should include Status")
	}
	_, writerSchema := getAs(t, client, base+"/articles/_schema", "writer", "application/json")
	if strings.Contains(writerSchema, `"Status"`) {
		t.Error("a writer's schema should omit Status")
	}
	if !strings.Contains(writerSchema, `"richtext"`) {
		t.Error("the schema should report the richtext field kind")
	}
}

// TestRichtextSanitizesOnSave checks the field's server-side cleaning end to
// end, and that the detail view renders the surviving markup as markup.
func TestRichtextSanitizesOnSave(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	code, body := post(t, client, base, base+"/articles", "editor", url.Values{
		"Title": {"Hostile"},
		"Body": {`<p>Hello <strong>world</strong></p>` +
			`<script>alert(1)</script>` +
			`<img src=x onerror=alert(1)>` +
			`<a href="javascript:alert(1)">bad link</a>`},
	})
	if code != http.StatusOK {
		t.Fatalf("create = %d (%s), want 200", code, body)
	}

	_, listing := getAs(t, client, base+"/articles", "editor", "application/json")
	var out struct {
		Items []Article `json:"items"`
	}
	if err := json.Unmarshal([]byte(listing), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	var stored string
	var id uint
	for _, a := range out.Items {
		if a.Title == "Hostile" {
			stored, id = a.Body, a.ID
		}
	}
	if id == 0 {
		t.Fatal("the created row is missing")
	}
	// Stored, not just displayed, safely. An image is content, so it stays; what
	// must not survive is the handler hanging off it.
	for _, bad := range []string{"<script", "onerror", "javascript:", "href="} {
		if strings.Contains(strings.ToLower(stored), bad) {
			t.Errorf("stored body still contains %q: %s", bad, stored)
		}
	}
	if !strings.Contains(stored, "<strong>world</strong>") {
		t.Errorf("legitimate formatting was lost: %s", stored)
	}
	if !strings.Contains(stored, `<img src="x">`) {
		t.Errorf("the image was dropped rather than disarmed: %s", stored)
	}

	// The detail view emits it as markup rather than escaped text.
	_, html := getAs(t, client, base+"/articles/"+itoa(id), "editor", "")
	if !strings.Contains(html, "<strong>world</strong>") {
		t.Error("Detail.HTML should render the markup unescaped")
	}
	if strings.Contains(html, "&lt;strong&gt;") {
		t.Error("Detail.HTML double-escaped the markup")
	}
}

// TestRichtextKeepsEditorialMarkupOnSave is the round trip a person performs by
// opening a record and pressing save. Dropping an unknown tag keeps its
// children, so an unlisted table does not lose its borders — its cells run
// together into one line, and an image vanishes with nothing left behind.
func TestRichtextKeepsEditorialMarkupOnSave(t *testing.T) {
	srv := newShowTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := srv.URL + "/admin"

	body := `<p style="text-align: justify;" class="MsoNormal">` +
		`<span lang="EN-US" style="font-family: Arial; font-size: 12pt;">Jatim Newsroom</span></p>` +
		`<table border="1"><tbody><tr><td>Jan</td><td>100</td></tr></tbody></table>` +
		`<figure><img src="/uploads/foto.jpg" alt="Foto" width="800"><figcaption>Ket</figcaption></figure>`

	code, resp := post(t, client, base, base+"/articles", "editor", url.Values{
		"Title": {"Round trip"}, "Body": {body},
	})
	if code != http.StatusOK {
		t.Fatalf("create = %d (%s)", code, resp)
	}

	_, listing := getAs(t, client, base+"/articles", "editor", "application/json")
	var out struct {
		Items []Article `json:"items"`
	}
	if err := json.Unmarshal([]byte(listing), &out); err != nil {
		t.Fatal(err)
	}
	var stored string
	for _, a := range out.Items {
		if a.Title == "Round trip" {
			stored = a.Body
		}
	}
	for _, want := range []string{
		"<table>", "<td>Jan</td>", `<img src="/uploads/foto.jpg" alt="Foto" width="800">`,
		"<figcaption>", `style="text-align: justify"`,
	} {
		if !strings.Contains(stored, want) {
			t.Errorf("saving lost %q\nstored: %s", want, stored)
		}
	}
	// House style owns type and colour; a pasted document does not bring its own.
	for _, gone := range []string{"font-family", "font-size", "class=", "lang="} {
		if strings.Contains(stored, gone) {
			t.Errorf("%q survived: %s", gone, stored)
		}
	}
}
