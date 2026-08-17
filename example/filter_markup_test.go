package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"

	steward "github.com/imfiqhan/steward"
)

type Ticket struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status int16
}

// Both filter layouts render the same body template, so a defect in it shows
// up in a panel that never opens a drawer.
func newMarkupServer(t *testing.T, layout steward.GridFilterLayout) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&Ticket{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Ticket{Title: "first", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:           db,
		SecretKey:    []byte("filter-markup-test-secret-key"),
		Prefix:       "/admin",
		FilterLayout: layout,
		AuthExcept:   []string{"/tickets*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[Ticket](app).Grid(func(g *steward.Grid[Ticket]) {
		g.Column("Title")
		g.Filter(func(f *steward.Filters[Ticket]) {
			f.Like("Title")
			f.Equal("Status").Select(steward.Options{"1": "Open", "2": "Closed"})
		})
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

// attrLike matches text that reads as tag attributes rather than prose, which
// is what a template holding part of an opening tag produces.
var attrLike = regexp.MustCompile(`\b(hx-[a-z-]+|href|class|method|action)="`)

// A template split so that one half kept the tail of an opening tag renders
// that tail as visible text: the panel showed
// `hx-get="/admin/berita" hx-target="#page-content" hx-push-url="true">`
// above its filter fields.
func TestFilterPanelLeaksNoMarkupAsText(t *testing.T) {
	for name, layout := range map[string]steward.GridFilterLayout{
		"above":  steward.FiltersAbove,
		"drawer": steward.FiltersDrawer,
	} {
		t.Run(name, func(t *testing.T) {
			srv := newMarkupServer(t, layout)
			resp, err := http.Get(srv.URL + "/admin/tickets")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /admin/tickets = %d, want 200", resp.StatusCode)
			}

			z := html.NewTokenizer(resp.Body)
			for {
				switch z.Next() {
				case html.ErrorToken:
					return // end of document
				case html.TextToken:
					text := strings.TrimSpace(string(z.Text()))
					if text == "" {
						continue
					}
					if m := attrLike.FindString(text); m != "" {
						t.Fatalf("markup rendered as text (%s…): %.120q", m, text)
					}
				}
			}
		})
	}
}
