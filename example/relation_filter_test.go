package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// Filtering and quick search across a one-hop relation, for each relationship
// shape. These paths used to pass Verify() and then fail at query time, so the
// point of these tests is that boot-time verification and runtime agree.

type rfWriter struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type rfLabel struct {
	ID    uint `gorm:"primaryKey"`
	Label string
}

type rfNote struct {
	ID        uint `gorm:"primaryKey"`
	ArticleID uint
	Body      string
}

type rfArticle struct {
	ID       uint `gorm:"primaryKey"`
	Title    string
	WriterID uint
	Writer   *rfWriter `gorm:"foreignKey:WriterID"`  // belongs to
	Notes    []rfNote  `gorm:"foreignKey:ArticleID"` // has many
	Labels   []rfLabel `gorm:"many2many:rf_article_labels"`
}

func newRelationServer(t *testing.T) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/rel.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&rfWriter{}, &rfLabel{}, &rfNote{}, &rfArticle{}); err != nil {
		t.Fatal(err)
	}

	writers := []rfWriter{{Name: "Alice"}, {Name: "Bob"}}
	if err := db.Create(&writers).Error; err != nil {
		t.Fatal(err)
	}
	labels := []rfLabel{{Label: "urgent"}, {Label: "archive"}}
	if err := db.Create(&labels).Error; err != nil {
		t.Fatal(err)
	}

	// Alice: one urgent article with a note; Bob: one archived article.
	a1 := rfArticle{Title: "Bridge opens", WriterID: writers[0].ID,
		Labels: []rfLabel{labels[0]}, Notes: []rfNote{{Body: "checked with the ministry"}}}
	a2 := rfArticle{Title: "Old ferry retired", WriterID: writers[1].ID,
		Labels: []rfLabel{labels[1]}}
	// A third article shares Alice *and* the urgent label, to prove the
	// many-to-many subquery does not duplicate rows.
	a3 := rfArticle{Title: "Ring road update", WriterID: writers[0].ID,
		Labels: []rfLabel{labels[0], labels[1]}}
	for _, a := range []*rfArticle{&a1, &a2, &a3} {
		if err := db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
	}

	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("relation-filter-test-secret-key"),
		AuthExcept: []string{"/rf_articles*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := steward.NewGormRepository[rfArticle](db)
	if err != nil {
		t.Fatal(err)
	}
	repo.With("Writer", "Labels", "Notes")

	steward.Register[rfArticle](app).Repository(repo).
		Grid(func(g *steward.Grid[rfArticle]) {
			g.Column("ID").Sortable()
			g.Column("Title")
			g.Column("Writer.Name", "Writer")
			// Quick search spans a direct column and two relation paths.
			g.QuickSearch("Title", "Writer.Name", "Labels.Label")
			g.Filter(func(f *steward.Filters[rfArticle]) {
				f.Equal("Writer.Name", "Writer") // belongs to
				f.Equal("Labels.Label", "Label") // many to many
				f.Equal("Labels.ID", "Label ID") // many to many, on the key
				f.Like("Notes.Body", "Note")     // has many
			})
		})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	// The whole point: these paths must be accepted at boot, because they work.
	if err := app.Verify(); err != nil {
		t.Fatalf("Verify rejected a filterable relation path: %v", err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db
}

// listing runs a JSON listing and returns the titles it contains, plus the
// reported total.
func listing(t *testing.T, srv *httptest.Server, query string) ([]string, int64) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/rf_articles?"+query, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?%s = %d: %s", query, resp.StatusCode, body)
	}
	var out struct {
		Total int64 `json:"total"`
		Items []struct {
			Title string `json:"Title"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("bad JSON for ?%s: %v", query, err)
	}
	titles := make([]string, 0, len(out.Items))
	for _, it := range out.Items {
		titles = append(titles, it.Title)
	}
	return titles, out.Total
}

func TestRelationFilters(t *testing.T) {
	srv, _ := newRelationServer(t)

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"belongs to", "f_Writer.Name=Alice", []string{"Bridge opens", "Ring road update"}},
		{"belongs to, other side", "f_Writer.Name=Bob", []string{"Old ferry retired"}},
		{"belongs to, no match", "f_Writer.Name=Nobody", nil},
		{"many to many", "f_Labels.Label=urgent", []string{"Bridge opens", "Ring road update"}},
		{"many to many, other label", "f_Labels.Label=archive", []string{"Old ferry retired", "Ring road update"}},
		{"many to many on the key", "f_Labels.ID=1", []string{"Bridge opens", "Ring road update"}},
		{"has many", "f_Notes.Body=ministry", []string{"Bridge opens"}},
		{"has many, no match", "f_Notes.Body=absent", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total := listing(t, srv, tc.query)
			if !sameSet(got, tc.want) {
				t.Errorf("titles = %v, want %v", got, tc.want)
			}
			// Count and the page must agree — the reason these are subqueries
			// rather than joins.
			if total != int64(len(tc.want)) {
				t.Errorf("total = %d, want %d (a join would over-report here)", total, len(tc.want))
			}
		})
	}
}

// TestManyToManyFilterDoesNotDuplicateRows is the specific failure a JOIN-based
// implementation would show: an article carrying two matching labels appearing
// twice.
func TestManyToManyFilterDoesNotDuplicateRows(t *testing.T) {
	srv, _ := newRelationServer(t)
	// "Ring road update" holds both labels, so an IN-list matching both must
	// still return it once.
	got, total := listing(t, srv, "f_Labels.Label=urgent&f_Labels.Label_to=")
	seen := map[string]int{}
	for _, title := range got {
		seen[title]++
	}
	for title, n := range seen {
		if n > 1 {
			t.Errorf("%q appeared %d times; the subquery should not multiply rows", title, n)
		}
	}
	if total != int64(len(got)) {
		t.Errorf("total %d disagrees with %d returned rows", total, len(got))
	}
}

func TestRelationQuickSearch(t *testing.T) {
	srv, _ := newRelationServer(t)

	// A writer's name is not on the article table, so this only works if quick
	// search reaches through the relation.
	got, _ := listing(t, srv, "q="+url.QueryEscape("Alice"))
	if !sameSet(got, []string{"Bridge opens", "Ring road update"}) {
		t.Errorf("searching a relation column returned %v", got)
	}

	// A many-to-many label, likewise.
	got, _ = listing(t, srv, "q="+url.QueryEscape("archive"))
	if !sameSet(got, []string{"Old ferry retired", "Ring road update"}) {
		t.Errorf("searching a many-to-many column returned %v", got)
	}

	// A direct column still works, and the OR group does not leak into the
	// other conditions.
	got, _ = listing(t, srv, "q="+url.QueryEscape("ferry"))
	if !sameSet(got, []string{"Old ferry retired"}) {
		t.Errorf("searching a direct column returned %v", got)
	}
}

// TestRelationSortIsRejectedAtBoot pins the other half of the fix. Sorting
// cannot go through a subquery, and it used to be silently ignored at click
// time; now it fails Verify instead.
func TestRelationSortIsRejectedAtBoot(t *testing.T) {
	build := func(t *testing.T, configure func(*steward.Grid[rfArticle])) error {
		t.Helper()
		db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/sort.db"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&rfWriter{}, &rfLabel{}, &rfNote{}, &rfArticle{}); err != nil {
			t.Fatal(err)
		}
		app, err := steward.New(steward.Config{
			DB: db, SecretKey: []byte("relation-sort-test-secret-key"),
		})
		if err != nil {
			t.Fatal(err)
		}
		steward.Register[rfArticle](app).Grid(configure)
		if err := app.Build(); err != nil {
			return err
		}
		return app.Verify()
	}

	err := build(t, func(g *steward.Grid[rfArticle]) {
		g.Column("Title")
		g.Column("Writer.Name").Sortable()
	})
	if err == nil {
		t.Error("a Sortable relation column should fail Verify")
	} else if !strings.Contains(err.Error(), "cannot be Sortable") {
		t.Errorf("unhelpful error: %v", err)
	}

	err = build(t, func(g *steward.Grid[rfArticle]) {
		g.Column("Title")
		g.Column("Writer.Name")
		g.DefaultSort("Writer.Name", false)
	})
	if err == nil {
		t.Error("a relation DefaultSort should fail Verify")
	} else if !strings.Contains(err.Error(), "relation path") {
		t.Errorf("unhelpful error: %v", err)
	}

	// A relation column that is merely displayed stays fine.
	if err := build(t, func(g *steward.Grid[rfArticle]) {
		g.Column("Title")
		g.Column("Writer.Name")
		g.Filter(func(f *steward.Filters[rfArticle]) { f.Equal("Writer.Name") })
	}); err != nil {
		t.Errorf("displaying and filtering a relation column should be fine: %v", err)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := map[string]int{}
	for _, g := range got {
		counts[g]++
	}
	for _, w := range want {
		counts[w]--
	}
	for _, n := range counts {
		if n != 0 {
			return false
		}
	}
	return true
}
