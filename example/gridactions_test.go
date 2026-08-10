package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type actionRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// newActionServer builds a panel with the given panel-wide default, optionally
// overriding the style on the grid itself.
func newActionServer(t *testing.T, global steward.GridActionStyle, override steward.GridActionStyle) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/act.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&actionRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&actionRow{Name: "row one"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("grid-actions-test-secret-key"),
		AuthExcept:  []string{"/action_rows*"},
		GridActions: global,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[actionRow](app)
	res.Grid(func(g *steward.Grid[actionRow]) {
		g.Column("Name")
		if override != "" {
			g.ActionStyle(override)
		}
		g.RowAction(steward.NewAction("publish", "Publish",
			func(c *steward.Context, ids []string) (*steward.Envelope, error) {
				return steward.Success("done"), nil
			}))
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/admin/action_rows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET grid = %d", resp.StatusCode)
	}
	return string(raw)
}

func TestGridActionStyles(t *testing.T) {
	cases := []struct {
		name             string
		global, override steward.GridActionStyle
		wantMenu         bool
	}{
		{"defaults to buttons", "", "", false},
		{"global menu", steward.GridActionsMenu, "", true},
		{"global buttons", steward.GridActionsButtons, "", false},
		{"grid overrides global buttons", steward.GridActionsButtons, steward.GridActionsMenu, true},
		{"grid overrides global menu", steward.GridActionsMenu, steward.GridActionsButtons, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := newActionServer(t, tc.global, tc.override)
			isMenu := strings.Contains(html, "data-steward-menu")
			if isMenu != tc.wantMenu {
				t.Fatalf("menu style = %v, want %v", isMenu, tc.wantMenu)
			}
			if tc.wantMenu {
				// The trigger and the menu must be wired to each other.
				if !strings.Contains(html, `aria-haspopup="menu"`) {
					t.Error("the menu trigger is not marked")
				}
				if !strings.Contains(html, `role="menu"`) {
					t.Error("no menu role")
				}
				// Ids are per row, or Basecoat wires every dropdown to the first.
				if !strings.Contains(html, "rowmenu-action_rows-1-trigger") {
					t.Error("the menu id is not per row")
				}
			} else {
				if !strings.Contains(html, `aria-label="Edit"`) {
					t.Error("the edit button is missing from the button style")
				}
			}
			// Whatever the presentation, every action has to be present.
			for _, want := range []string{"Publish", "/action_rows/1/edit", "/action_rows/1"} {
				if !strings.Contains(html, want) {
					t.Errorf("%q is missing in %s style", want, map[bool]string{true: "menu", false: "button"}[tc.wantMenu])
				}
			}
			// And the column stays pinned either way.
			if !strings.Contains(html, "steward-col-actions") {
				t.Error("the actions column should stay pinned in both styles")
			}
		})
	}
}

// TestRowMenuIdsAreUnique guards the thing that breaks silently: Basecoat wires
// each dropdown by id, so duplicated ids would point every row's trigger at the
// first row's menu.
func TestRowMenuIdsAreUnique(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/ids.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&actionRow{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.Create(&actionRow{Name: "row"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("grid-actions-ids-secret-key"),
		AuthExcept: []string{"/action_rows*"}, GridActions: steward.GridActionsMenu,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[actionRow](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/admin/action_rows")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	html := string(raw)

	seen := map[string]bool{}
	for i := 1; i <= 5; i++ {
		id := "rowmenu-action_rows-" + itoa(uint(i)) + "-trigger"
		if !strings.Contains(html, id) {
			t.Errorf("row %d has no menu trigger (%s)", i, id)
		}
		if seen[id] {
			t.Errorf("duplicate id %s", id)
		}
		seen[id] = true
	}
}
