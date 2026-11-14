package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type imRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Count int
	Live  bool
}

type imNoCreate struct{ steward.AllowAll[imRow] }

func (imNoCreate) Create(*steward.Context) bool { return false }

func newImportPanel(t *testing.T, policy steward.Policy[imRow]) (*steward.Panel, *gorm.DB, *httptest.Server) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&imRow{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&imRow{Title: "Old", Count: 1})
	app, err := steward.New(steward.Config{Prefix: "/admin", DB: db, SecretKey: []byte("import-test-secret-key-00000"),
		UploadDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[imRow](app).
		Grid(func(g *steward.Grid[imRow]) {
			g.Column("Title")
			g.Column("Count")
			g.Import()
		}).
		Form(func(f *steward.Form[imRow]) {
			f.Text("Title").Rules("required")
			f.Number("Count").Min(0)
			f.Switch("Live")
		})
	if policy != nil {
		res.Policy(policy)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	all := steward.Permission{Name: "All", Slug: "im-all", HTTPPath: "*"}
	db.Create(&all)
	addUser(t, app, "importer", []steward.Permission{all})
	addUser(t, app, "other", []steward.Permission{all})
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = app.Close() })
	return app, db, srv
}

var importRunRe = regexp.MustCompile(`/admin/im_rows/_import\?run=(\d+)`)

func uploadCSV(t *testing.T, c *opClient, name, body string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("file", name)
	_, _ = part.Write([]byte(body))
	_ = w.Close()
	req, _ := http.NewRequest("POST", c.base+"/im_rows/_import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-CSRF-Token", c.csrf)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get("Location")
}

func TestImportRunsRowsThroughTheForm(t *testing.T) {
	app, db, srv := newImportPanel(t, nil)
	c := signInAs(t, srv, "importer")

	if !strings.Contains(htmlGet(t, c, "/im_rows"), `data-steward-import`) {
		t.Fatal("the grid should offer Import")
	}
	csvBody := "ID,Title,Count,Live,Note\n" +
		",First,3,ya,x\n" +
		",,5,no,y\n" +
		"1,Renamed,9,1,z\n" +
		"99,Ghost,1,0,\n" +
		",,,,\n"
	code, loc := uploadCSV(t, c, "rows.csv", csvBody)
	m := importRunRe.FindStringSubmatch(loc)
	if code != http.StatusFound || m == nil {
		t.Fatalf("upload = %d → %q", code, loc)
	}
	page := htmlGet(t, c, "/im_rows/_import?run="+m[1])
	for col, want := range map[string]string{"col_0": "ID", "col_1": "Title", "col_2": "Count", "col_3": "Live"} {
		if !regexp.MustCompile(`name="` + col + `"[\s\S]*?value="` + want + `" selected`).MatchString(page) {
			t.Errorf("%s should be matched to %s from its header", col, want)
		}
	}

	form := url.Values{"run": {m[1]}, "col_0": {"ID"}, "col_1": {"Title"}, "col_2": {"Count"}, "col_3": {"Live"}, "col_4": {""}}
	if code, _ := c.do("POST", "/im_rows/_import", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())); code >= 400 {
		t.Fatalf("confirm = %d", code)
	}

	var run steward.ImportRun
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, _ = app.RunQueuedJobs(t.Context())
		db.First(&run, m[1])
		if run.Status == steward.ImportDone || run.Status == steward.ImportFailed || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if run.Status != steward.ImportDone || run.Created != 1 || run.Updated != 1 || run.Failed != 2 || run.Total != 4 {
		t.Fatalf("run = status %s, total %d, created %d, updated %d, failed %d (%s)",
			run.Status, run.Total, run.Created, run.Updated, run.Failed, run.Err)
	}
	if len(run.Data) != 0 {
		t.Error("the uploaded file should be dropped once the run finishes")
	}

	var first, renamed imRow
	db.Where("title = ?", "First").First(&first)
	db.First(&renamed, 1)
	if first.Count != 3 || !first.Live {
		t.Errorf("created row = %+v", first)
	}
	if renamed.Title != "Renamed" || renamed.Count != 9 || !renamed.Live {
		t.Errorf("a row naming a key should update that record: %+v", renamed)
	}

	_, report := c.do("GET", "/_imports/"+m[1]+"/report", "", nil)
	for _, want := range []string{"Line 3:", "Title is required.", "Line 5:", "No record has the key 99."} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
	other := signInAs(t, srv, "other")
	if code, _ := other.do("GET", "/_imports/"+m[1]+"/report", "", nil); code != http.StatusNotFound {
		t.Errorf("another account fetched the report: %d", code)
	}

	var n steward.Notification
	if err := db.Where("type = ?", "import.finished").First(&n).Error; err != nil || !strings.Contains(n.Body, "1 created, 1 updated, 2 failed.") {
		t.Errorf("the importer should be notified with the counts: %+v %v", n, err)
	}
}

func TestImportNeedsCreate(t *testing.T) {
	_, _, srv := newImportPanel(t, imNoCreate{})
	c := signInAs(t, srv, "importer")
	if strings.Contains(htmlGet(t, c, "/im_rows"), `data-steward-import`) {
		t.Error("a reader who may not create should not see Import")
	}
	if code, _ := uploadCSV(t, c, "rows.csv", "Title\nx\n"); code != http.StatusForbidden {
		t.Errorf("upload without Create = %d, want 403", code)
	}
}

// The report belongs to the account that ran the import, whatever its role:
// a role that may only list and create still downloads its own report.
func TestImportReportNeedsNoPermissionOfItsOwn(t *testing.T) {
	app, db, srv := newImportPanel(t, nil)
	perms := []steward.Permission{
		{Name: "List", Slug: "im-list", HTTPMethod: "GET", HTTPPath: "/im_rows\n/im_rows/*"},
		{Name: "Create", Slug: "im-create", HTTPMethod: "POST", HTTPPath: "/im_rows"},
	}
	for i := range perms {
		db.Create(&perms[i])
	}
	addUser(t, app, "clerk", perms)
	c := signInAs(t, srv, "clerk")
	code, loc := uploadCSV(t, c, "rows.csv", "Title,Count\nok,1\nbad,-5\n")
	m := importRunRe.FindStringSubmatch(loc)
	if code != http.StatusFound || m == nil {
		t.Fatalf("upload = %d → %q", code, loc)
	}
	// The second row breaks Count's minimum, so there is a report.
	if _, err := uploadCSVRow(t, c, m[1]); err != nil {
		t.Fatal(err)
	}
	var run steward.ImportRun
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		_, _ = app.RunQueuedJobs(t.Context())
		db.First(&run, m[1])
		if run.Status == steward.ImportDone {
			break
		}
	}
	if run.ReportPath == "" {
		t.Fatalf("the run has no report: %+v", run)
	}
	if code, body := c.do("GET", "/_imports/"+m[1]+"/report", "", nil); code != http.StatusOK {
		t.Errorf("the clerk's own report = %d %s", code, firstLine(body))
	}
}

func uploadCSVRow(t *testing.T, c *opClient, run string) (int, error) {
	t.Helper()
	form := url.Values{"run": {run}, "col_0": {"Title"}, "col_1": {"Count"}}
	code, _ := c.do("POST", "/im_rows/_import", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	return code, nil
}
