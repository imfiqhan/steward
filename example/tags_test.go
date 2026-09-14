package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type tagRow struct {
	ID       uint   `gorm:"primaryKey"`
	Name     string `gorm:"size:120"`
	Keywords string `gorm:"type:text"`
}

func newTagServer(t *testing.T) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&tagRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tagRow{Name: "a", Keywords: `["go","sql"]`}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("tags-test-secret-key-00000"),
		AuthExcept: []string{"/tag_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[tagRow](app).
		Grid(func(g *steward.Grid[tagRow]) {
			g.Column("Name")
			g.Column("Keywords").Tags()
		}).
		Form(func(f *steward.Form[tagRow]) {
			f.Text("Name")
			f.Tags("Keywords")
		}).
		Detail(func(d *steward.Detail[tagRow]) {
			d.Field("Name")
			d.Field("Keywords").Tags()
		})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return srv, db
}

// The editor is progressive enhancement over a hidden input: what the column
// holds has to reach the form as the hidden input's value, or the script has
// nothing to draw and the first save empties the column.
func TestTagsFieldCarriesTheStoredArray(t *testing.T) {
	srv, _ := newTagServer(t)
	page := getBody(t, srv.URL+"/admin/tag_rows/1/edit")

	for _, want := range []string{
		`class="steward-tags" data-steward-tags`,
		`data-steward-tags-chips`,
		`data-steward-tags-input`,
		`<input type="hidden" name="Keywords" value="[&#34;go&#34;,&#34;sql&#34;]" data-steward-tags-value/>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the tags field is missing %s", want)
		}
	}
}

// What posts the field is not always the widget that drew it, so the shape the
// column ends up with is the server's to decide.
func TestTagsFieldStoresACanonicalArray(t *testing.T) {
	cases := []struct {
		posted string
		want   string
	}{
		{`["go","sql"]`, `["go","sql"]`},
		{`["  go  ","sql","go",""]`, `["go","sql"]`},
		{`[]`, ""},
		{"", ""},
		// Not the widget's payload at all.
		{"go", `["go"]`},
	}
	for _, tc := range cases {
		srv, db := newTagServer(t)
		code, body := putRow2(t, srv, "/admin/tag_rows/1/edit", "/admin/tag_rows/1",
			map[string]string{"Name": "a", "Keywords": tc.posted})
		if code >= 400 {
			t.Fatalf("PUT %q = %d %s", tc.posted, code, body)
		}
		var row tagRow
		if err := db.First(&row, 1).Error; err != nil {
			t.Fatal(err)
		}
		if row.Keywords != tc.want {
			t.Errorf("posting %q stored %q, want %q", tc.posted, row.Keywords, tc.want)
		}
	}
}

// Without a presenter the cell is the column: a reader sees ["go","sql"].
func TestTagsReadAsChips(t *testing.T) {
	srv, _ := newTagServer(t)

	grid := getBody(t, srv.URL+"/admin/tag_rows")
	if !strings.Contains(grid, `<span class="steward-tag">go</span>`) {
		t.Error("the grid cell did not render the stored values as chips")
	}
	if strings.Contains(grid, `[&#34;go&#34;,&#34;sql&#34;]`) {
		t.Error("the grid cell still shows the raw array")
	}

	detail := getBody(t, srv.URL+"/admin/tag_rows/1")
	if !strings.Contains(detail, `<span class="steward-tag">sql</span>`) {
		t.Error("the detail row did not render the stored values as chips")
	}
	if strings.Contains(detail, `[&#34;go&#34;,&#34;sql&#34;]`) {
		t.Error("the detail row still shows the raw array")
	}
}

// A column holding a value from before the field became Tags.
func TestTagsKeepAPlainValue(t *testing.T) {
	srv, db := newTagServer(t)
	if err := db.Model(&tagRow{}).Where("id = ?", 1).Update("keywords", "legacy").Error; err != nil {
		t.Fatal(err)
	}
	if grid := getBody(t, srv.URL+"/admin/tag_rows"); !strings.Contains(grid, `<span class="steward-tag">legacy</span>`) {
		t.Error("a plain stored value did not render as one chip")
	}
	code, body := putRow2(t, srv, "/admin/tag_rows/1/edit", "/admin/tag_rows/1",
		map[string]string{"Name": "a", "Keywords": "legacy"})
	if code >= 400 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	var row tagRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Keywords != `["legacy"]` {
		t.Errorf("saving lost the value it was shown: %q", row.Keywords)
	}
}

func TestTagsFieldHonoursReadOnly(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&tagRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tagRow{Name: "a", Keywords: `["go"]`}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("tags-test-secret-key-00000"),
		AuthExcept: []string{"/tag_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[tagRow](app).Form(func(f *steward.Form[tagRow]) {
		f.Text("Name")
		f.Tags("Keywords").ReadOnly()
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)

	page := getBody(t, srv.URL+"/admin/tag_rows/1/edit")
	if !strings.Contains(page, `data-readonly="1"`) {
		t.Error("a read-only tags field did not say so, so the chips keep their remove buttons")
	}
}
