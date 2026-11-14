package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type psRow struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Order int
}

func newPermSyncPanel(t *testing.T, db *gorm.DB, title string) *steward.Panel {
	t.Helper()
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("permission-sync-test-secret-key"),
		SyncPermissions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[psRow](app).Title(title).
		Grid(func(g *steward.Grid[psRow]) {
			g.Column("Title")
			g.Reorderable("/admin/ps_rows/_order")
		}).
		Page("POST", "_order", func(c *steward.Context) error {
			return c.Envelope(steward.Success("saved"))
		})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	return app
}

func permByKey(t *testing.T, db *gorm.DB, key string) steward.Permission {
	t.Helper()
	var p steward.Permission
	if err := db.Where("code_key = ?", key).First(&p).Error; err != nil {
		t.Fatalf("no synced permission %q: %v", key, err)
	}
	return p
}

func TestSyncPermissionsWritesTree(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&psRow{}); err != nil {
		t.Fatal(err)
	}
	newPermSyncPanel(t, db, "Rows")

	root := permByKey(t, db, "ps_rows")
	if root.Slug != "ps_rows" || root.ParentID != 0 || root.Source != steward.MenuSourceCode {
		t.Errorf("root row = %+v", root)
	}
	update := permByKey(t, db, "ps_rows#update")
	if update.ParentID != root.ID {
		t.Errorf("update's parent = %d, want %d", update.ParentID, root.ID)
	}
	if !strings.Contains(update.HTTPPath, "POST:/ps_rows/_order") {
		t.Errorf("a custom POST page should be update-level, got %q", update.HTTPPath)
	}
	for _, op := range []string{"view", "create", "delete"} {
		p := permByKey(t, db, "ps_rows#"+op)
		if p.Slug != "ps_rows-"+op || p.ParentID != root.ID {
			t.Errorf("%s row = %+v", op, p)
		}
	}
	// Built-in resources are synced as well.
	permByKey(t, db, "auth/users#view")
}

func TestSyncPermissionsReconciles(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&psRow{}); err != nil {
		t.Fatal(err)
	}
	newPermSyncPanel(t, db, "Rows")

	// An operator edits one synced row in the UI and makes one by hand.
	edited := permByKey(t, db, "ps_rows#delete")
	if err := db.Model(&edited).Updates(map[string]any{"name": "Remove rows", "overridden": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&steward.Permission{Name: "Mine", Slug: "mine", HTTPPath: "/x"}).Error; err != nil {
		t.Fatal(err)
	}

	// The resource is retitled in code and the panel boots again.
	app := newPermSyncPanel(t, db, "Records")
	res, err := app.SyncPermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 || res.Updated != 0 {
		t.Errorf("a second sync with nothing changed wrote rows: %+v", res)
	}

	if p := permByKey(t, db, "ps_rows#view"); p.Name != "Records: view" {
		t.Errorf("a code row should follow the code, got %q", p.Name)
	}
	if p := permByKey(t, db, "ps_rows#delete"); p.Name != "Remove rows" {
		t.Errorf("an overridden row should keep its edit, got %q", p.Name)
	}
	var mine steward.Permission
	if err := db.Where("slug = ?", "mine").First(&mine).Error; err != nil || mine.CodeKey != nil {
		t.Errorf("a hand-made row should be untouched: %+v %v", mine, err)
	}
	var n int64
	db.Model(&steward.Permission{}).Where("code_key LIKE ?", "ps_rows%").Count(&n)
	if n != 5 {
		t.Errorf("synced rows for one resource = %d, want 5", n)
	}
}

func TestSyncPermissionsSkipsTakenSlug(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&psRow{}, &steward.Permission{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&steward.Permission{Name: "Old", Slug: "ps_rows-view", HTTPPath: "/old"}).Error; err != nil {
		t.Fatal(err)
	}
	app := newPermSyncPanel(t, db, "Rows")
	res, err := app.SyncPermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, "ps_rows#view") {
			found = true
		}
	}
	if !found {
		t.Errorf("a slug held by a hand-made row should be reported, got %v", res.Skipped)
	}
	var old steward.Permission
	db.Where("slug = ?", "ps_rows-view").First(&old)
	if old.HTTPPath != "/old" || old.CodeKey != nil {
		t.Errorf("the hand-made row was changed: %+v", old)
	}
}

// The rows the sync writes are the rules the middleware enforces: a role
// holding only "view" lists the rows and is refused a create.
func TestSyncedPermissionsGrantTheirOperation(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&psRow{}); err != nil {
		t.Fatal(err)
	}
	app := newPermSyncPanel(t, db, "Rows")
	addUser(t, app, "viewer", []steward.Permission{permByKey(t, db, "ps_rows#view")})
	addUser(t, app, "orderer", []steward.Permission{
		permByKey(t, db, "ps_rows#view"), permByKey(t, db, "ps_rows#update"),
	})
	addUser(t, app, "owner", []steward.Permission{permByKey(t, db, "ps_rows")})
	srv := serve(t, app)
	t.Cleanup(srv.Close)

	form := url.Values{"Title": {"new"}}.Encode()
	post := func(c *opClient, path string) int {
		code, _ := c.do("POST", path, "application/x-www-form-urlencoded", strings.NewReader(form))
		return code
	}

	viewer := signInAs(t, srv, "viewer")
	if code, body := viewer.do("GET", "/ps_rows", "", nil); code != http.StatusOK {
		t.Errorf("viewer: list = %d %s", code, firstLine(body))
	}
	if code := post(viewer, "/ps_rows"); code != http.StatusForbidden {
		t.Errorf("viewer: create = %d, want 403", code)
	}
	if code := post(viewer, "/ps_rows/_order"); code != http.StatusForbidden {
		t.Errorf("viewer: reorder = %d, want 403", code)
	}

	orderer := signInAs(t, srv, "orderer")
	if code := post(orderer, "/ps_rows/_order"); code != http.StatusOK {
		t.Errorf("orderer: reorder = %d, want 200", code)
	}

	owner := signInAs(t, srv, "owner")
	if code := post(owner, "/ps_rows"); code >= 400 {
		t.Errorf("owner: create = %d", code)
	}
}
