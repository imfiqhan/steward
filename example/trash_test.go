package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type trRow struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	Live      bool
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type trPlain struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

// trKeepPolicy lets rows be deleted but not restored or erased.
type trKeepPolicy struct{ steward.AllowAll[trRow] }

func (trKeepPolicy) Restore(*steward.Context, *trRow) bool     { return false }
func (trKeepPolicy) ForceDelete(*steward.Context, *trRow) bool { return false }

type trashFixture struct {
	app    *steward.Panel
	db     *gorm.DB
	search *steward.MemorySearcher
	c      *nuClient
}

func newTrashFixture(t *testing.T, configure func(*steward.Resource[trRow])) *trashFixture {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&trRow{}, &trPlain{}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"alpha", "bravo", "charlie"} {
		if err := db.Create(&trRow{Title: title}).Error; err != nil {
			t.Fatal(err)
		}
	}
	search := steward.NewMemorySearcher()
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("trash-test-secret-key-000000"),
		AuthExcept: []string{"/tr_rows*", "/tr_plains*"},
		Searcher:   search,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[trRow](app).Searchable("Title").
		Grid(func(g *steward.Grid[trRow]) {
			g.Column("Title")
			g.Column("Live").Switch()
			g.QuickSearch("Title")
		}).
		Form(func(f *steward.Form[trRow]) {
			f.Text("Title")
			f.Switch("Live")
		})
	if configure != nil {
		configure(res)
	}
	steward.Register[trPlain](app)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Reindex(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	c := newNuClient(t, srv.URL+"/admin/tr_rows")
	m := comboCSRFRe.FindStringSubmatch(c.get(""))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	c.csrf = m[1]
	return &trashFixture{app: app, db: db, search: search, c: c}
}

func (f *trashFixture) titles(t *testing.T, query string) []string {
	t.Helper()
	code, body := f.c.send("GET", "?"+query, "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET ?%s = %d %s", query, code, body)
	}
	var out struct {
		Items []struct{ Title string } `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range out.Items {
		titles = append(titles, it.Title)
	}
	return titles
}

func (f *trashFixture) post(t *testing.T, path, ids string) (int, string) {
	t.Helper()
	return f.c.send("POST", path, "application/x-www-form-urlencoded", strings.NewReader("ids="+ids))
}

func TestTrashRestoresAndErases(t *testing.T) {
	f := newTrashFixture(t, nil)
	if code, body := f.c.send("DELETE", "/2", "", nil); code != http.StatusOK {
		t.Fatalf("delete = %d %s", code, body)
	}
	if got := f.titles(t, ""); !sameSet(got, []string{"alpha", "charlie"}) {
		t.Errorf("the list shows %v after a soft delete", got)
	}
	if got := f.titles(t, "trashed=1"); !sameSet(got, []string{"bravo"}) {
		t.Errorf("the trash shows %v", got)
	}
	if got := f.titles(t, "trashed=1&q=brav"); !sameSet(got, []string{"bravo"}) {
		t.Errorf("searching the trash found %v; the engine no longer holds the row", got)
	}

	page := f.c.get("")
	if !strings.Contains(page, "data-steward-trash-toggle") || !strings.Contains(page, `trashed=1`) {
		t.Error("the grid should offer its trash")
	}
	trash := f.c.get("?trashed=1")
	for _, want := range []string{"/admin/tr_rows/_restore", "/admin/tr_rows/_force_delete", "Back to list"} {
		if !strings.Contains(trash, want) {
			t.Errorf("the trash view lacks %s", want)
		}
	}
	for _, unwanted := range []string{"data-steward-inline-switch", `href="/admin/tr_rows/create"`, "/admin/tr_rows/2/edit"} {
		if strings.Contains(trash, unwanted) {
			t.Errorf("the trash view should not offer %s", unwanted)
		}
	}

	if code, body := f.post(t, "/_restore", "2"); code != http.StatusOK || !strings.Contains(body, "Restored record.") {
		t.Fatalf("restore = %d %s", code, body)
	}
	if got := f.titles(t, ""); !sameSet(got, []string{"alpha", "bravo", "charlie"}) {
		t.Errorf("after restore the list shows %v", got)
	}
	if hits, _ := f.search.Query(context.Background(), "tr_rows", "bravo", 10); len(hits) != 1 {
		t.Error("a restored row should be back in the search index")
	}

	// Permanent delete takes only what is in the trash.
	if code, _ := f.post(t, "/_force_delete", "1"); code != http.StatusOK {
		t.Fatalf("force delete of a live row = %d", code)
	}
	var n int64
	f.db.Unscoped().Model(&trRow{}).Where("id = 1").Count(&n)
	if n != 1 {
		t.Error("a live row was erased by a permanent delete")
	}
	f.c.send("DELETE", "/1", "", nil)
	if code, body := f.post(t, "/_force_delete", "1"); code != http.StatusOK || !strings.Contains(body, "permanently") {
		t.Fatalf("force delete = %d %s", code, body)
	}
	f.db.Unscoped().Model(&trRow{}).Where("id = 1").Count(&n)
	if n != 0 {
		t.Error("a permanently deleted row is still in the table")
	}
}

func TestTrashFollowsModelAndSwitches(t *testing.T) {
	f := newTrashFixture(t, func(r *steward.Resource[trRow]) {
		r.Grid(func(g *steward.Grid[trRow]) {
			g.Column("Title")
			g.DisableTrash()
		})
	})
	if strings.Contains(f.c.get(""), "data-steward-trash-toggle") {
		t.Error("DisableTrash should hide the trash")
	}
	if code, _ := f.post(t, "/_restore", "1"); code != http.StatusNotFound {
		t.Errorf("restore without a trash = %d, want 404", code)
	}
	plain := newNuClient(t, strings.Replace(f.c.base, "tr_rows", "tr_plains", 1))
	if strings.Contains(plain.get(""), "data-steward-trash-toggle") {
		t.Error("a model without DeletedAt has nothing to put in a trash")
	}
}

func TestTrashPolicy(t *testing.T) {
	f := newTrashFixture(t, func(r *steward.Resource[trRow]) { r.Policy(trKeepPolicy{}) })
	f.c.send("DELETE", "/3", "", nil)
	if code, _ := f.post(t, "/_restore", "3"); code != http.StatusForbidden {
		t.Errorf("restore denied by the policy = %d, want 403", code)
	}
	if code, _ := f.post(t, "/_force_delete", "3"); code != http.StatusForbidden {
		t.Errorf("force delete denied by the policy = %d, want 403", code)
	}
	if got := f.titles(t, "trashed=1"); !sameSet(got, []string{"charlie"}) {
		t.Errorf("a refused request changed the trash: %v", got)
	}
}

// A role that may only list cannot restore; one that may delete can.
func TestTrashPermissions(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&trRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&trRow{Title: "gone"}).Error; err != nil {
		t.Fatal(err)
	}
	db.Delete(&trRow{}, 1)
	app, err := steward.New(steward.Config{Prefix: "/admin", DB: db, SecretKey: []byte("trash-perm-secret-key-00000")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[trRow](app)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	list := steward.Permission{Name: "List", Slug: "tr-list", HTTPMethod: "GET", HTTPPath: "/tr_rows\n/tr_rows/*"}
	del := steward.Permission{Name: "Delete", Slug: "tr-delete", HTTPMethod: "DELETE", HTTPPath: "/tr_rows/*"}
	for _, p := range []*steward.Permission{&list, &del} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	addUser(t, app, "lister", []steward.Permission{list})
	addUser(t, app, "deleter", []steward.Permission{list, del})
	srv := serve(t, app)
	t.Cleanup(srv.Close)

	restore := func(c *opClient) int {
		code, _ := c.do("POST", "/tr_rows/_restore", "application/x-www-form-urlencoded", strings.NewReader("ids=1"))
		return code
	}
	if code := restore(signInAs(t, srv, "lister")); code != http.StatusForbidden {
		t.Errorf("lister: restore = %d, want 403", code)
	}
	if code := restore(signInAs(t, srv, "deleter")); code != http.StatusOK {
		t.Errorf("deleter: restore = %d, want 200", code)
	}
}

// The repository's own guard: restore and permanent delete touch only rows in
// the trash, whatever ids a caller passes.
func TestGormRepositoryTrashTouchesOnlyTrash(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&trRow{}); err != nil {
		t.Fatal(err)
	}
	live := trRow{Title: "live"}
	gone := trRow{Title: "gone"}
	db.Create(&live)
	db.Create(&gone)
	db.Delete(&gone)
	repo, err := steward.NewGormRepository[trRow](db)
	if err != nil {
		t.Fatal(err)
	}
	if !repo.Trashable() {
		t.Fatal("a model with DeletedAt should be trashable")
	}
	ids := []string{"1", "2"}
	if err := repo.ForceDelete(t.Context(), ids); err != nil {
		t.Fatal(err)
	}
	var left []trRow
	db.Unscoped().Order("id").Find(&left)
	if len(left) != 1 || left[0].Title != "live" {
		t.Errorf("ForceDelete over a live and a deleted row left %+v; only the deleted one should go", left)
	}
	plain, _ := steward.NewGormRepository[trPlain](db)
	if plain.Trashable() {
		t.Error("a model without DeletedAt is not trashable")
	}
}
