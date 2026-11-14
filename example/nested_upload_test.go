package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// Uploads and BelongsTo inside a HasMany row, reached through the parent's
// _upload and _options endpoints as "Relation.Path".

type nuAuthor struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type nuSlide struct {
	ID       uint `gorm:"primaryKey"`
	DeckID   uint
	Caption  string
	Photo    string
	Handout  string
	AuthorID uint
	Author   *nuAuthor `gorm:"foreignKey:AuthorID"`
}

type nuDeck struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Slides []nuSlide `gorm:"foreignKey:DeckID"`
}

type nuClient struct {
	t    *testing.T
	http *http.Client
	base string
	csrf string
}

func newNestedUploadServer(t *testing.T) (*nuClient, *gorm.DB, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "uploads")
	db := testDB(t)
	if err := db.AutoMigrate(&nuAuthor{}, &nuDeck{}, &nuSlide{}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Alice", "Bob"} {
		if err := db.Create(&nuAuthor{Name: n}).Error; err != nil {
			t.Fatal(err)
		}
	}
	deck := nuDeck{Title: "Deck", Slides: []nuSlide{{Caption: "first", AuthorID: 2}}}
	if err := db.Create(&deck).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("nested-upload-test-secret-key"),
		UploadDir:  dir,
		AuthExcept: []string{"/nu_decks*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[nuDeck](app).Form(func(f *steward.Form[nuDeck]) {
		f.Text("Title")
		steward.HasMany(f, "Slides", "DeckID", func(cf *steward.Form[nuSlide]) {
			cf.Text("Caption")
			cf.Image("Photo").Dir("slides")
			cf.File("Handout").Accept(".pdf")
			cf.BelongsTo("AuthorID", "Author", "Name", "Author")
		})
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatalf("Verify refused upload or BelongsTo fields in a row: %v", err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	c := &nuClient{t: t, http: &http.Client{Jar: jar}, base: srv.URL + "/admin/nu_decks"}
	page := c.get("/1/edit")
	m := comboCSRFRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token")
	}
	c.csrf = m[1]
	return c, db, dir
}

func (c *nuClient) get(path string) string {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("GET %s = %d %s", path, resp.StatusCode, firstLine(string(b)))
	}
	return string(b)
}

func (c *nuClient) send(method, path, contentType string, body io.Reader) (int, string) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *nuClient) upload(field, name, body string) string {
	c.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", name)
	_, _ = part.Write([]byte(body))
	_ = w.Close()
	code, out := c.send("POST", "/_upload?field="+url.QueryEscape(field)+"&id=1", w.FormDataContentType(), &buf)
	if code != http.StatusOK {
		c.t.Fatalf("upload %s = %d %s", field, code, out)
	}
	var reply struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil || reply.Path == "" {
		c.t.Fatalf("upload reply %s", out)
	}
	return reply.Path
}

const pngBytes = "\x89PNG\r\n\x1a\n0000"

func TestNestedRowRendersUploadAndBelongsTo(t *testing.T) {
	c, _, _ := newNestedUploadServer(t)
	page := c.get("/1/edit")
	for _, want := range []string{
		`data-steward-upload="/admin/nu_decks/_upload?field=Slides.Photo&amp;id=1"`,
		`data-steward-upload="/admin/nu_decks/_upload?field=Slides.Handout&amp;id=1"`,
		`data-steward-options="/admin/nu_decks/_options?field=Slides.AuthorID"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("edit page lacks %s", want)
		}
	}
	// The existing row's author is shown by name, not by id.
	if !strings.Contains(page, ">Bob<") {
		t.Error("the row's BelongsTo does not show its selected author")
	}
}

func TestNestedRowEndpoints(t *testing.T) {
	c, _, _ := newNestedUploadServer(t)

	code, body := c.send("GET", "/_options?field=Slides.AuthorID&q=Ali", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "Alice") || strings.Contains(body, "Bob") {
		t.Errorf("row BelongsTo search = %d %s", code, body)
	}
	if p := c.upload("Slides.Photo", "a.png", pngBytes); !strings.HasPrefix(p, "slides/") {
		t.Errorf("a row image should land in its field's directory, got %s", p)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "a.html")
	_, _ = part.Write([]byte("<script></script>"))
	_ = w.Close()
	if code, _ := c.send("POST", "/_upload?field=Slides.Handout&id=1", w.FormDataContentType(), &buf); code < 400 {
		t.Error("a row file field accepted a type outside its Accept")
	}
	if code, _ := c.send("POST", "/_upload?field=Slides.Caption&id=1", w.FormDataContentType(), &buf); code != http.StatusNotFound {
		t.Errorf("a non-upload row field took an upload: %d", code)
	}
}

func TestNestedRowSavesAndReplacesUpload(t *testing.T) {
	c, db, dir := newNestedUploadServer(t)
	save := func(photo string) {
		t.Helper()
		form := url.Values{
			"Title":               {"Deck"},
			"Slides[1][Caption]":  {"first"},
			"Slides[1][Photo]":    {photo},
			"Slides[1][AuthorID]": {"1"},
		}
		code, body := c.send("PUT", "/1", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if code != http.StatusOK {
			t.Fatalf("save = %d %s", code, body)
		}
	}

	first := c.upload("Slides.Photo", "a.png", pngBytes)
	save(first)
	var slide nuSlide
	db.First(&slide, 1)
	if slide.Photo != first || slide.AuthorID != 1 {
		t.Fatalf("row not saved: %+v", slide)
	}

	second := c.upload("Slides.Photo", "b.png", pngBytes)
	save(second)
	if _, err := os.Stat(filepath.Join(dir, first)); !os.IsNotExist(err) {
		t.Errorf("the replaced row image is still in storage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, second)); err != nil {
		t.Errorf("the new row image is missing: %v", err)
	}
}
