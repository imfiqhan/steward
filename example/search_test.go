package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type searchRow struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status string
}

// countingSearcher wraps the in-memory engine to record that it was actually
// asked — the point of the seam is that SQL LIKE stops being the thing that
// answers, and only a count proves that.
type countingSearcher struct {
	*steward.MemorySearcher
	queries atomic.Int32
	indexed atomic.Int32
	deleted atomic.Int32
}

func (c *countingSearcher) Index(ctx context.Context, docs ...steward.SearchDoc) error {
	c.indexed.Add(int32(len(docs)))
	return c.MemorySearcher.Index(ctx, docs...)
}

func (c *countingSearcher) Delete(ctx context.Context, typ string, ids ...string) error {
	c.deleted.Add(int32(len(ids)))
	return c.MemorySearcher.Delete(ctx, typ, ids...)
}

func (c *countingSearcher) Query(ctx context.Context, typ, q string, limit int) ([]steward.SearchHit, error) {
	c.queries.Add(1)
	return c.MemorySearcher.Query(ctx, typ, q, limit)
}

func newSearchServer(t *testing.T, s steward.Searcher) (*httptest.Server, *gorm.DB, *steward.Admin) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&searchRow{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []searchRow{
		{Title: "Bank Jatim sabet penghargaan", Status: "live"},
		{Title: "Koni Jatim persiapkan puslatda", Status: "live"},
		{Title: "Nothing to do with either", Status: "draft"},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("search-test-secret-key-0"),
		AuthExcept: []string{"/*"}, Searcher: s,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[searchRow](app).
		Searchable("Title").
		Command("Title").
		Grid(func(g *steward.Grid[searchRow]) {
			g.Column("Title")
			g.Column("Status")
			g.QuickSearch("Title")
			g.Filter(func(f *steward.Filters[searchRow]) {
				f.Equal("Status").Select(steward.Options{"live": "Live", "draft": "Draft"})
			})
		})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db, app
}

func rowTitles(t *testing.T, srv *httptest.Server, query string) []string {
	t.Helper()
	_, body := getAcceptJSON(t, srv.URL+"/admin/search_rows?"+query)
	var out struct {
		Items []searchRow `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("bad JSON for %q: %v (%s)", query, err, body)
	}
	titles := make([]string, 0, len(out.Items))
	for _, i := range out.Items {
		titles = append(titles, i.Title)
	}
	return titles
}

// TestQuickSearchUsesTheEngine is the point of the seam.
func TestQuickSearchUsesTheEngine(t *testing.T) {
	s := &countingSearcher{MemorySearcher: steward.NewMemorySearcher()}
	srv, _, app := newSearchServer(t, s)

	if _, err := app.Reindex(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if s.indexed.Load() != 3 {
		t.Fatalf("backfill indexed %d rows, want 3", s.indexed.Load())
	}

	got := rowTitles(t, srv, "q=jatim")
	if len(got) != 2 {
		t.Errorf("q=jatim returned %d rows: %v", len(got), got)
	}
	if s.queries.Load() == 0 {
		t.Error("the engine was never asked; the SQL path answered instead")
	}
}

// TestEngineResultsStillPassThroughFilters is the safety property. The engine
// ranks; the rows are read through the repository, so a filter still narrows
// them — an engine that returned rows directly would have to be taught the
// panel's authorization too.
func TestEngineResultsStillPassThroughFilters(t *testing.T) {
	s := &countingSearcher{MemorySearcher: steward.NewMemorySearcher()}
	srv, _, app := newSearchServer(t, s)
	if _, err := app.Reindex(t.Context(), 100); err != nil {
		t.Fatal(err)
	}

	both := rowTitles(t, srv, "q=jatim")
	if len(both) != 2 {
		t.Fatalf("expected two matches before filtering, got %v", both)
	}
	// Same query, plus a filter that only one of them satisfies.
	filtered := rowTitles(t, srv, "q=jatim&f_Status=draft")
	if len(filtered) != 0 {
		t.Errorf("the filter did not narrow the engine's hits: %v", filtered)
	}
}

// TestNoMatchesMeansNoRows: an engine returning nothing has to end as an empty
// grid. Dropping the condition instead would show every row, which reads as the
// search having been ignored.
func TestNoMatchesMeansNoRows(t *testing.T) {
	s := &countingSearcher{MemorySearcher: steward.NewMemorySearcher()}
	srv, _, app := newSearchServer(t, s)
	if _, err := app.Reindex(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	if got := rowTitles(t, srv, "q=zzzqqq"); len(got) != 0 {
		t.Errorf("a query nothing matched returned %d rows: %v", len(got), got)
	}
}

// TestWritesKeepTheIndexCurrent covers the half that is easy to forget: an index
// only fed by a backfill answers for yesterday's data.
func TestWritesKeepTheIndexCurrent(t *testing.T) {
	s := &countingSearcher{MemorySearcher: steward.NewMemorySearcher()}
	srv, _, app := newSearchServer(t, s)
	if _, err := app.Reindex(t.Context(), 100); err != nil {
		t.Fatal(err)
	}

	if code, body := putRow2(t, srv, "/admin/search_rows/3/edit", "/admin/search_rows/3",
		map[string]string{"Title": "Now mentions Jatim too", "Status": "draft"}); code >= 400 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	if got := rowTitles(t, srv, "q=jatim"); len(got) != 3 {
		t.Errorf("after an edit the index holds %d matches, want 3: %v", len(got), got)
	}

	// And a delete takes it back out.
	before := s.deleted.Load()
	if code, body := deleteRow(t, srv, "/admin/search_rows/3/edit", "/admin/search_rows/3"); code >= 400 {
		t.Fatalf("DELETE = %d %s", code, body)
	}
	if s.deleted.Load() == before {
		t.Error("deleting a record left it in the index")
	}
	if got := rowTitles(t, srv, "q=jatim"); len(got) != 2 {
		t.Errorf("after a delete the index holds %d matches, want 2: %v", len(got), got)
	}
}

// TestFallsBackToSQLWithoutASearcher keeps the default path working: a panel
// that configures no engine must behave exactly as it did.
func TestFallsBackToSQLWithoutASearcher(t *testing.T) {
	srv, _, _ := newSearchServer(t, nil)
	got := rowTitles(t, srv, "q=jatim")
	if len(got) != 2 {
		t.Errorf("the SQL path returned %d rows: %v", len(got), got)
	}
}

// TestSearchableWithoutAnEngineIndexesNothing: declaring the paths is not
// supposed to cost anything until an engine is configured.
func TestSearchableWithoutAnEngineIndexesNothing(t *testing.T) {
	_, _, app := newSearchServer(t, nil)
	if _, err := app.Reindex(t.Context(), 100); err == nil {
		t.Error("Reindex without a Searcher should say so rather than pretend")
	} else if !strings.Contains(err.Error(), "Searcher") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// deleteRow submits the delete a grid's row action sends.
func deleteRow(t *testing.T, srv *httptest.Server, editPath, delPath string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + editPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	req, err := http.NewRequest(http.MethodDelete, srv.URL+delPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", m[1])
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	b, _ := io.ReadAll(out.Body)
	return out.StatusCode, string(b)
}

// TestEngineRankingReachesTheRows is the defect this covers: the engine's IDs
// went in as an IN condition and the database returned them in its own order,
// so "the thousand best matches" became "ten of them, by id". On a grid sorted
// newest-first that meant a search showed the newest matches, never the best.
func TestEngineRankingReachesTheRows(t *testing.T) {
	s := &countingSearcher{MemorySearcher: steward.NewMemorySearcher()}
	srv, db, app := newSearchServer(t, s)

	// The strongest match must be the OLDEST row, so that the grid's own sort —
	// newest first — would put it last. Creating it newest as well proved
	// nothing: it would have come first either way, which is how the first
	// version of this test passed against code that ignored the ranking.
	var best searchRow
	if err := db.First(&best, 1).Error; err != nil {
		t.Fatal(err)
	}
	best.Title = "jatim jatim jatim strongest match"
	if err := db.Save(&best).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Reindex(t.Context(), 100); err != nil {
		t.Fatal(err)
	}

	got := rowTitles(t, srv, "q=jatim")
	if len(got) == 0 {
		t.Fatal("no rows")
	}
	if got[0] != "jatim jatim jatim strongest match" {
		t.Errorf("first row is %q; the engine ranked the oldest row first, "+
			"so the grid is still ordering by its own sort", got[0])
	}

	// A reader who picks a column to sort by has said what they want, and that
	// beats relevance.
	sorted := rowTitles(t, srv, "q=jatim&sort=Title")
	if len(sorted) < 2 {
		t.Fatalf("expected several rows, got %v", sorted)
	}
	if sorted[0] > sorted[1] {
		t.Errorf("an explicit sort was overridden by relevance: %v", sorted)
	}
}
