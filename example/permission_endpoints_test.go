package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	steward "github.com/imfiqhan/steward"
)

// opRow is a resource reached by roles whose permissions are written per
// operation, the way dcat-admin rows are: "POST /op_rows", "PUT /op_rows/*".
type opRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Pic   string
	Kind  string
}

func newOpServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&opRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&opRow{Title: "first"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("op-endpoint-test-secret-key"),
		UploadDir: filepath.Join(t.TempDir(), "uploads"),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[opRow](app)
	res.Form(func(f *steward.Form[opRow]) {
		f.Text("Title")
		f.Image("Pic")
		f.Select("Kind").Options(steward.Options{"a": "Alpha", "b": "Beta"})
	})
	res.Grid(func(g *steward.Grid[opRow]) {
		g.Column("Title")
		g.BatchAction(steward.NewAction("touch", "Touch",
			func(*steward.Context, []string) (*steward.Envelope, error) { return nil, nil }))
		g.ToolAction(steward.NewAction("sync", "Sync",
			func(*steward.Context, []string) (*steward.Envelope, error) { return nil, nil }))
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}

	// The rows a dcat-admin import carries for one resource, split the way it
	// splits them: a listing, a create submit and an edit submit.
	perms := map[string]steward.Permission{
		"list":   {Name: "List", Slug: "op.list", HTTPMethod: "GET", HTTPPath: "/op_rows"},
		"create": {Name: "Create", Slug: "op.create", HTTPMethod: "POST", HTTPPath: "/op_rows"},
		"edit":   {Name: "Edit", Slug: "op.edit", HTTPMethod: "PUT", HTTPPath: "/op_rows/*"},
	}
	for k, p := range perms {
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		perms[k] = p
	}
	addUser(t, app, "writer", []steward.Permission{perms["list"], perms["create"], perms["edit"]})
	addUser(t, app, "reader", []steward.Permission{perms["list"]})

	srv := serve(t, app)
	return srv
}

func addUser(t *testing.T, app *steward.Panel, username string, perms []steward.Permission) {
	t.Helper()
	role := steward.Role{Name: username, Slug: username, Permissions: perms}
	if err := app.DB().Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("op-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := steward.User{Username: username, Name: username, Password: string(hash),
		Roles: []steward.Role{role}}
	if err := app.DB().Create(&u).Error; err != nil {
		t.Fatal(err)
	}
}

type opClient struct {
	t    *testing.T
	http *http.Client
	base string
	csrf string
}

func signInAs(t *testing.T, srv *httptest.Server, username string) *opClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &opClient{t: t, http: &http.Client{Jar: jar}, base: srv.URL + "/admin"}
	_, page := c.do("GET", "/auth/login", "", nil)
	m := formTokenRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token on the login page")
	}
	form := url.Values{"username": {username}, "password": {"op-password"}, "_token": {m[1]}}
	if code, body := c.do("POST", "/auth/login", "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode())); code >= 400 {
		t.Fatalf("login as %s = %d %s", username, code, firstLine(body))
	}
	_, profile := c.do("GET", "/auth/profile", "", nil)
	mm := metaTokenRe2.FindStringSubmatch(profile)
	if mm == nil {
		t.Fatal("no CSRF meta on the profile page")
	}
	c.csrf = mm[1]
	return c
}

func (c *opClient) do(method, path, contentType string, body io.Reader) (int, string) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		c.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (c *opClient) upload(query string) (int, string) {
	c.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", "pic.png")
	// A PNG signature, so the image check accepts the bytes.
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\n0000"))
	_ = w.Close()
	return c.do("POST", "/op_rows/_upload?"+query, w.FormDataContentType(), &buf)
}

func (c *opClient) action(name, ids string) (int, string) {
	c.t.Helper()
	form := url.Values{}
	if ids != "" {
		form.Set("ids", ids)
	}
	return c.do("POST", "/op_rows/_action/"+name, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
}

// TestPerOperationPermissionsReachTheirEndpoints covers a resource's own
// endpoints under permissions that name only the operations: an upload from a
// form is reached through that form's submit route, an action through the
// rows it changes, an options search through the listing.
func TestPerOperationPermissionsReachTheirEndpoints(t *testing.T) {
	srv := newOpServer(t)

	writer := signInAs(t, srv, "writer")
	for _, tc := range []struct {
		name string
		call func() (int, string)
	}{
		{"upload on create", func() (int, string) { return writer.upload("field=Pic") }},
		{"upload on edit", func() (int, string) { return writer.upload("field=Pic&id=1") }},
		{"batch action", func() (int, string) { return writer.action("touch", "1") }},
		{"tool action", func() (int, string) { return writer.action("sync", "") }},
		{"options", func() (int, string) { return writer.do("GET", "/op_rows/_options?field=Kind", "", nil) }},
		{"schema", func() (int, string) { return writer.do("GET", "/op_rows/_schema", "", nil) }},
	} {
		if code, body := tc.call(); code != http.StatusOK {
			t.Errorf("writer: %s = %d %s", tc.name, code, firstLine(body))
		}
	}

	reader := signInAs(t, srv, "reader")
	for _, tc := range []struct {
		name string
		call func() (int, string)
	}{
		{"upload on create", func() (int, string) { return reader.upload("field=Pic") }},
		{"upload on edit", func() (int, string) { return reader.upload("field=Pic&id=1") }},
		{"batch action", func() (int, string) { return reader.action("touch", "1") }},
		{"tool action", func() (int, string) { return reader.action("sync", "") }},
	} {
		if code, _ := tc.call(); code != http.StatusForbidden {
			t.Errorf("reader: %s = %d, want 403", tc.name, code)
		}
	}
	if code, body := reader.do("GET", "/op_rows/_options?field=Kind", "", nil); code != http.StatusOK {
		t.Errorf("reader: options = %d %s", code, firstLine(body))
	}
}
