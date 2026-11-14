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

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type qcRow struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status string
	Body   string
}

type qcDenyCreate struct{ steward.AllowAll[qcRow] }

func (qcDenyCreate) Create(*steward.Context) bool { return false }

func newQuickPanel(t *testing.T, policy steward.Policy[qcRow], grid func(*steward.Grid[qcRow]), form func(*steward.Form[qcRow])) (*httptest.Server, *gorm.DB, error) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&qcRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("quick-create-test-secret-key"),
		AuthExcept: []string{"/qc_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[qcRow](app).Grid(grid).Form(form)
	if policy != nil {
		res.Policy(policy)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		return nil, db, err
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return srv, db, nil
}

func qcGrid(g *steward.Grid[qcRow]) {
	g.Column("Title")
	g.QuickCreate("Title", "Status")
}

func qcForm(f *steward.Form[qcRow]) {
	f.Text("Title").Rules("required|max:20")
	f.Select("Status").Options(steward.Options{"draft": "Draft", "live": "Live"})
	f.Textarea("Body")
}

func TestQuickCreateRendersAndSaves(t *testing.T) {
	srv, db, err := newQuickPanel(t, nil, qcGrid, qcForm)
	if err != nil {
		t.Fatal(err)
	}
	page := getBody(t, srv.URL+"/admin/qc_rows")
	i := strings.Index(page, `aria-label="Quick create"`)
	if i < 0 {
		t.Fatal("no quick-create row on the grid")
	}
	row := page[i:]
	row = row[:strings.Index(row, "</form>")]
	for _, want := range []string{`data-steward-field="Title"`, `data-steward-field="Status"`, `name="_quick" value="1"`} {
		if !strings.Contains(row, want) {
			t.Errorf("quick-create row lacks %s", want)
		}
	}
	if strings.Contains(row, `data-steward-field="Body"`) {
		t.Error("a field not named in QuickCreate reached the row")
	}

	code, body := putRowMethod(t, srv, "/admin/qc_rows", http.MethodPost, "/admin/qc_rows",
		map[string]string{"_quick": "1", "Title": "Quick one", "Status": "live"})
	if code != http.StatusOK {
		t.Fatalf("quick create = %d %s", code, body)
	}
	var env struct {
		Data struct {
			Then struct {
				Action string `json:"action"`
			} `json:"then"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &env)
	if env.Data.Then.Action != "refresh" {
		t.Errorf("a quick create should reload the list, not redirect: %s", body)
	}
	var got qcRow
	if err := db.Where("title = ?", "Quick one").First(&got).Error; err != nil || got.Status != "live" {
		t.Errorf("row not saved as submitted: %+v %v", got, err)
	}

	// The form's rules still apply.
	code, body = putRowMethod(t, srv, "/admin/qc_rows", http.MethodPost, "/admin/qc_rows",
		map[string]string{"_quick": "1", "Title": strings.Repeat("x", 30)})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"Title"`) {
		t.Errorf("an over-long title should fail validation: %d %s", code, body)
	}
}

func TestQuickCreateHiddenWithoutCreate(t *testing.T) {
	srv, _, err := newQuickPanel(t, qcDenyCreate{}, qcGrid, qcForm)
	if err != nil {
		t.Fatal(err)
	}
	if page := getBody(t, srv.URL+"/admin/qc_rows"); strings.Contains(page, `aria-label="Quick create"`) {
		t.Error("a reader who may not create should not see the quick-create row")
	}
}

func TestQuickCreateVerify(t *testing.T) {
	for _, tc := range []struct {
		name string
		grid func(*steward.Grid[qcRow])
		want string
	}{
		{"unknown field", func(g *steward.Grid[qcRow]) { g.QuickCreate("Title", "Nope") }, "not a field of the form"},
		{"long text", func(g *steward.Grid[qcRow]) { g.QuickCreate("Title", "Body") }, "needs the full form"},
		{"required left out", func(g *steward.Grid[qcRow]) { g.QuickCreate("Status") }, `leaves out "Title"`},
	} {
		_, _, err := newQuickPanel(t, nil, tc.grid, qcForm)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}
}

// putRowMethod loads pagePath for its CSRF token and submits values to path
// with the given method, asking for the JSON envelope.
func putRowMethod(t *testing.T, srv *httptest.Server, pagePath, method, path string, values map[string]string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + pagePath)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", m[1])
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	b, _ := io.ReadAll(out.Body)
	return out.StatusCode, string(b)
}
