package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type Record struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Kind  string
}

// exportRows is how many rows the fixtures hold. Above the threshold set below,
// so the same table can exercise both paths.
const exportRows = 60

func newExportApp(t *testing.T, threshold int, uploadDir string) (*steward.Admin, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&Record{}); err != nil {
		t.Fatal(err)
	}
	rows := make([]Record, 0, exportRows)
	for i := 1; i <= exportRows; i++ {
		kind := "keep"
		if i%3 == 0 {
			kind = "skip"
		}
		rows = append(rows, Record{Title: fmt.Sprintf("row %03d", i), Kind: kind})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:                   db,
		SecretKey:            []byte("export-job-test-secret-key"),
		Prefix:               "/admin",
		UploadDir:            uploadDir,
		BackgroundExportRows: threshold,
		// The test drives the runner itself, so a worker racing it would make
		// the assertions depend on timing.
		DisableExportWorker: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Record](app).Grid(func(g *steward.Grid[Record]) {
		g.Column("Title")
		g.Column("Kind")
		g.Filter(func(f *steward.Filters[Record]) {
			f.Equal("Kind").Select(steward.Options{"keep": "Keep", "skip": "Skip"})
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	return app, db
}

func exportClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	return client
}

// A whole-table export below the threshold is still a download, and it must
// carry every row exactly once — the keyset walk is what could drop or repeat
// one at a batch boundary.
func TestStreamedExportCoversEveryRowOnce(t *testing.T) {
	app, _ := newExportApp(t, -1, t.TempDir()) // negative: never in the background
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)

	resp, err := client.Get(srv.URL + "/admin/records?export=all")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "csv") {
		t.Fatalf("export came back as %q, want CSV", ct)
	}

	recs, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != exportRows+1 { // + the header
		t.Fatalf("export has %d lines, want %d", len(recs), exportRows+1)
	}
	seen := map[string]int{}
	for _, r := range recs[1:] {
		seen[r[0]]++
	}
	if len(seen) != exportRows {
		t.Fatalf("%d distinct rows in a %d-row table", len(seen), exportRows)
	}
	for title, n := range seen {
		if n != 1 {
			t.Errorf("%q appears %d times", title, n)
		}
	}
}

// The batch is 1,000 rows, so a table smaller than that never crosses a
// boundary. This walks the repository directly with a batch of one.
func TestKeysetWalkCrossesBatchBoundaries(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&Record{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		if err := db.Create(&Record{Title: fmt.Sprintf("row %02d", i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo, err := steward.NewGormRepository[Record](db)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]int{}
	var cursor any = uint(0)
	for range 20 { // a bound, so a broken cursor loops forever in the test rather than the suite
		items, _, err := repo.List(context.Background(), &steward.ListQuery{PerPage: 3, After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			seen[it.Title]++
		}
		cursor = items[len(items)-1].ID
	}
	if len(seen) != 10 {
		t.Fatalf("walked %d distinct rows, want 10", len(seen))
	}
	for title, n := range seen {
		if n != 1 {
			t.Errorf("%q came back %d times", title, n)
		}
	}
}

// Past the threshold the request stops being a download: it answers at once,
// leaves a pending job, and the account is notified when the file is built.
func TestLargeExportBecomesAJob(t *testing.T) {
	dir := t.TempDir()
	app, _ := newExportApp(t, 10, dir) // 60 rows, threshold 10
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)
	ctx := context.Background()

	resp, err := client.Get(srv.URL + "/admin/records?export=all")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "csv") {
		t.Fatal("a large export was still streamed")
	}
	if !strings.Contains(body, "background") {
		t.Fatalf("the answer does not say it was queued: %.200s", body)
	}

	jobs, err := app.Exports(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("%d jobs queued, want 1", len(jobs))
	}
	if jobs[0].Status != steward.ExportPending {
		t.Fatalf("job status %q, want pending", jobs[0].Status)
	}

	// Nothing is running it, so the file appears only when asked for.
	done, err := app.RunPendingExports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if done != 1 {
		t.Fatalf("ran %d jobs, want 1", done)
	}

	jobs, _ = app.Exports(ctx, 1, 0)
	job := jobs[0]
	if job.Status != steward.ExportDone {
		t.Fatalf("job status %q (%s), want done", job.Status, job.Err)
	}
	if job.Rows != exportRows {
		t.Errorf("job wrote %d rows, want %d", job.Rows, exportRows)
	}
	if job.Path == "" || job.Bytes == 0 {
		t.Fatalf("job records no file: path=%q bytes=%d", job.Path, job.Bytes)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(job.Path))); err != nil {
		t.Fatalf("the stored file is not there: %v", err)
	}

	// The account hears about it, and the notification leads to the download.
	notes, err := app.Notifications(ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Fatal("no notification for a finished export")
	}
	if notes[0].Type != "export.ready" {
		t.Fatalf("notification type %q, want export.ready", notes[0].Type)
	}
	dl, err := client.Get(srv.URL + notes[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dl.Body.Close() }()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("downloading the export = %d, want 200", dl.StatusCode)
	}
	recs, err := csv.NewReader(dl.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != exportRows+1 {
		t.Fatalf("the built file has %d lines, want %d", len(recs), exportRows+1)
	}
}

// The job covers what the reader was looking at, not the whole table: the
// query string is carried into the background.
func TestJobKeepsTheFiltersItWasAskedWith(t *testing.T) {
	app, _ := newExportApp(t, 10, t.TempDir())
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)
	ctx := context.Background()

	resp, err := client.Get(srv.URL + "/admin/records?export=all&f_Kind=keep")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	if _, err := app.RunPendingExports(ctx); err != nil {
		t.Fatal(err)
	}

	jobs, _ := app.Exports(ctx, 1, 0)
	if len(jobs) != 1 {
		t.Fatalf("%d jobs, want 1", len(jobs))
	}
	// 60 rows, every third one "skip".
	want := int64(exportRows - exportRows/3)
	if jobs[0].Rows != want {
		t.Fatalf("the job exported %d rows, want the %d matching the filter", jobs[0].Rows, want)
	}
}

// An export belongs to the account that asked for it: its rows are whatever
// that account's policies allowed it to read.
func TestOneAccountCannotDownloadAnothersExport(t *testing.T) {
	dir := t.TempDir()
	app, db := newExportApp(t, 10, dir)
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)
	ctx := context.Background()

	other := steward.AdminUser{Username: "someone-else", Name: "Else"}
	if err := db.Where("username = ?", other.Username).FirstOrCreate(&other).Error; err != nil {
		t.Fatal(err)
	}
	// The file has to exist, or a 404 could come from the missing file rather
	// than from the check being tested.
	if err := os.MkdirAll(filepath.Join(dir, "exports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exports", "theirs.csv"), []byte("Title\nsecret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := steward.ExportJob{
		UserID: other.ID, Slug: "records", Status: steward.ExportDone,
		Path: "exports/theirs.csv", Disk: "local", Rows: 1, Bytes: 14,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get(srv.URL + fmt.Sprintf("/admin/_exports/%d/download", job.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("downloading another account's export = %d, want 404", resp.StatusCode)
	}
	_ = ctx
}

// Two runners must not build the same file twice.
func TestAJobIsClaimedOnce(t *testing.T) {
	app, _ := newExportApp(t, 10, t.TempDir())
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)
	ctx := context.Background()

	resp, err := client.Get(srv.URL + "/admin/records?export=all")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)

	first, err := app.RunPendingExports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.RunPendingExports(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("runs claimed %d then %d jobs, want 1 then 0", first, second)
	}
}

func TestPruneExportsRemovesTheFileToo(t *testing.T) {
	dir := t.TempDir()
	app, db := newExportApp(t, 10, dir)
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	client := exportClient(t, srv)
	ctx := context.Background()

	resp, err := client.Get(srv.URL + "/admin/records?export=all")
	if err != nil {
		t.Fatal(err)
	}
	_ = readBody(t, resp)
	if _, err := app.RunPendingExports(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, _ := app.Exports(ctx, 1, 0)
	path := filepath.Join(dir, filepath.FromSlash(jobs[0].Path))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("setup: no file at %s", path)
	}

	if err := db.Exec("UPDATE admin_exports SET created_at = ?", time.Now().Add(-90*24*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	gone, err := app.PruneExports(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if gone != 1 {
		t.Fatalf("pruned %d jobs, want 1", gone)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the file outlived the job that owned it")
	}
}
