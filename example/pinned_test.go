package main

import (
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type pinRow struct {
	ID          uint `gorm:"primaryKey"`
	Title, Code string
	Notes       string
}

func newPinPanel(t *testing.T, grid func(*steward.Grid[pinRow])) (*steward.Panel, error) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&pinRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&pinRow{Title: "a", Code: "c"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("pinned-column-test-secret-key"),
		AuthExcept: []string{"/pin_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[pinRow](app).Grid(grid)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	return app, app.Verify()
}

func TestPinnedColumnsRender(t *testing.T) {
	app, err := newPinPanel(t, func(g *steward.Grid[pinRow]) {
		g.Column("ID").Pin()
		g.Column("Title").Pin()
		g.Column("Code")
		g.Column("Notes")
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	page := getBody(t, srv.URL+"/admin/pin_rows")

	for _, want := range []string{
		`data-col="0" data-steward-pin="0"`,
		`data-col="1" data-steward-pin="1"`,
		// The row selector pins with the columns, header and body.
		`<th class="w-[1%]" data-steward-pin="sel">`,
		`<td data-steward-pin="sel">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(page, `data-steward-pin="2"`) {
		t.Error("an unpinned column carries the pin marker")
	}
}

func TestPinnedColumnsWithoutPinRenderPlain(t *testing.T) {
	app, err := newPinPanel(t, func(g *steward.Grid[pinRow]) {
		g.Column("ID")
		g.Column("Title")
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	if page := getBody(t, srv.URL+"/admin/pin_rows"); strings.Contains(page, "data-steward-pin") {
		t.Error("a grid with nothing pinned should not pin the selector")
	}
}

func TestPinnedColumnsVerify(t *testing.T) {
	_, err := newPinPanel(t, func(g *steward.Grid[pinRow]) {
		g.Column("ID")
		g.Column("Title").Pin()
	})
	if err == nil || !strings.Contains(err.Error(), "must be declared first") {
		t.Errorf("a pinned column after an unpinned one should fail Verify, got %v", err)
	}

	_, err = newPinPanel(t, func(g *steward.Grid[pinRow]) {
		g.Column("ID").Pin()
		g.Column("Title").Pin()
		g.Column("Code")
		g.GroupColumns("Naming", "Title", "Code")
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be grouped") {
		t.Errorf("a pinned column in a header group should fail Verify, got %v", err)
	}
}
