package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type filterRow struct {
	ID     uint `gorm:"primaryKey"`
	Name   string
	Status string
	TagID  uint
}

// newFilterServer builds a grid with a short filter list and a long one. calls
// counts how often the long list is resolved, which is how "loaded once at
// registration" is told apart from "resolved per request".
func newFilterServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/fl.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&filterRow{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if err := db.Create(&filterRow{Name: fmt.Sprintf("row %d", i), Status: "on", TagID: uint(i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("filter-test-secret-key-00"),
		AuthExcept: []string{"/filter_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[filterRow](app).Grid(func(g *steward.Grid[filterRow]) {
		g.Column("Name")
		g.Filter(func(f *steward.Filters[filterRow]) {
			f.Equal("Status").Select(steward.Options{"on": "On", "off": "Off"})
			f.Equal("TagID", "Tag").SelectFunc(func(_ *steward.Context) steward.Options {
				if calls != nil {
					calls.Add(1)
				}
				opts := steward.Options{}
				for i := 1; i <= 300; i++ {
					opts[fmt.Sprint(i)] = fmt.Sprintf("tag-%03d", i)
				}
				opts["999"] = "wisata bahari"
				return opts
			})
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

// TestFilterShipsOnlyOnePageOfALongList is the defect: a <select> has to carry
// every option in the page, so a tag filter over 7,776 rows put all of them
// into every page of the grid — 649 KB of HTML per load.
func TestFilterShipsOnlyOnePageOfALongList(t *testing.T) {
	srv := newFilterServer(t, nil)
	page := getBody(t, srv.URL+"/admin/filter_rows")

	if strings.Contains(page, `<select id="filter-`) {
		t.Error("a filter still renders a native select")
	}
	if n := strings.Count(page, `data-value="`); n > 120 {
		t.Errorf("the page carries %d options; a long list should ship one page", n)
	}
	// The long one fetches, the short one does not.
	if !strings.Contains(page, `data-steward-options="/admin/filter_rows/_options?filter=f_TagID"`) {
		t.Error("the long filter has no options endpoint")
	}
	if strings.Contains(page, `data-steward-options="/admin/filter_rows/_options?filter=f_Status"`) {
		t.Error("a two-option filter should ship whole rather than fetch")
	}
	// Both still submit under their own parameter.
	for _, want := range []string{`name="f_Status"`, `name="f_TagID"`} {
		if !strings.Contains(page, want) {
			t.Errorf("missing submitted input %s", want)
		}
	}
}

// TestFilterOptionsEndpoint covers the search the control leans on.
func TestFilterOptionsEndpoint(t *testing.T) {
	srv := newFilterServer(t, nil)

	code, body := getStatus(t, srv.URL+"/admin/filter_rows/_options?filter=f_TagID&q=wisata")
	if code != http.StatusOK {
		t.Fatalf("search = %d: %s", code, body)
	}
	var out struct {
		Options []struct{ Value, Label string } `json:"options"`
		More    bool                            `json:"more"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, body)
	}
	if len(out.Options) != 1 || out.Options[0].Label != "wisata bahari" {
		t.Errorf("search returned %+v", out.Options)
	}

	// An unfiltered page is capped and says so.
	_, body = getStatus(t, srv.URL+"/admin/filter_rows/_options?filter=f_TagID")
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Options) > 50 {
		t.Errorf("an unfiltered page returned %d options", len(out.Options))
	}
	if !out.More {
		t.Error("the reply does not say anything was left out")
	}

	// And it answers only for filters it has.
	if code, _ := getStatus(t, srv.URL+"/admin/filter_rows/_options?filter=f_Nope"); code != http.StatusNotFound {
		t.Errorf("unknown filter = %d, want 404", code)
	}
}

// TestSelectFuncResolvesPerRequest: Select takes its map when the resource is
// registered, so a list read from the database there is loaded at boot and
// never refreshed. SelectFunc is the way to say "ask me each time".
func TestSelectFuncResolvesPerRequest(t *testing.T) {
	var calls atomic.Int32
	srv := newFilterServer(t, &calls)
	if n := calls.Load(); n != 0 {
		t.Errorf("registering the resource resolved the options %d times", n)
	}
	getBody(t, srv.URL+"/admin/filter_rows")
	first := calls.Load()
	if first == 0 {
		t.Fatal("rendering the grid never resolved the options")
	}
	getBody(t, srv.URL+"/admin/filter_rows")
	if calls.Load() <= first {
		t.Error("the second render reused the first render's options")
	}
}

// getStatus fetches a URL and returns its status and body.
func getStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}
