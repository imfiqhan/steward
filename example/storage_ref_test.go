package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// A File or Image form field stores a storage-relative path, not a URL. Any
// helper that puts that value straight into a src or an href therefore has to
// resolve it, or the browser reads it relative to the current page.

type mediaRow struct {
	ID    uint `gorm:"primaryKey"`
	Cover string
	Doc   string
}

func newMediaServer(t *testing.T, rows ...mediaRow) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&mediaRow{}); err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("storage-ref-test-secret-key"),
		AuthExcept: []string{"/media_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[mediaRow](app)
	res.Grid(func(g *steward.Grid[mediaRow]) {
		g.Column("Cover").Image(60, 40)
		g.Column("Doc").Copyable()
	})
	res.Detail(func(d *steward.Detail[mediaRow]) {
		d.Field("Cover").Image(480, 0)
		d.Field("Doc").Link()
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

func fetchOK(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", url, resp.StatusCode)
	}
	return string(b)
}

func TestStorageRefResolvesStoredPaths(t *testing.T) {
	cases := []struct {
		name     string
		stored   string
		wantSrc  string
		wantText string // what Detail.Link shows, when it differs from the href
	}{
		{
			name:    "a storage path resolves",
			stored:  "images/galleries/cover.jpg",
			wantSrc: "/admin/_uploads/local/images/galleries/cover.jpg",
		},
		{
			// A real filename from the imported data: unescaped, a space in a
			// src or href is invalid.
			name:     "a space is escaped",
			stored:   "files/Majalah Potensi Januari.pdf",
			wantSrc:  "/admin/_uploads/local/files/Majalah%20Potensi%20Januari.pdf",
			wantText: "files/Majalah Potensi Januari.pdf",
		},
		{
			name:    "an absolute URL is left alone",
			stored:  "https://cdn.example.com/a.jpg",
			wantSrc: "https://cdn.example.com/a.jpg",
		},
		{
			name:    "a rooted path is left alone",
			stored:  "/static/a.jpg",
			wantSrc: "/static/a.jpg",
		},
		{
			name:    "a data URI is left alone",
			stored:  "data:image/gif;base64,R0lGOD",
			wantSrc: "data:image/gif;base64,R0lGOD",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMediaServer(t, mediaRow{Cover: tc.stored, Doc: tc.stored})

			// A stored path resolves to a signed URL on its disk, so the src is
			// the prefix plus an expiry and a signature; anything already
			// absolute is passed through untouched and must match exactly.
			hasRef := func(html, attr string) bool {
				want := attr + `="` + tc.wantSrc
				if !strings.Contains(html, want) {
					return false
				}
				if !strings.HasPrefix(tc.wantSrc, "/admin/_uploads/") {
					return strings.Contains(html, want+`"`)
				}
				i := strings.Index(html, want)
				return strings.Contains(html[i:min(len(html), i+300)], "sig=")
			}

			grid := fetchOK(t, srv.URL+"/admin/media_rows")
			if !hasRef(grid, "src") {
				t.Errorf("grid Image src is not %q", tc.wantSrc)
			}

			detail := fetchOK(t, srv.URL+"/admin/media_rows/1")
			if !hasRef(detail, "src") {
				t.Errorf("detail Image src is not %q", tc.wantSrc)
			}
			if !hasRef(detail, "href") {
				t.Errorf("detail Link href is not %q", tc.wantSrc)
			}
			// The link shows the stored value; only the href is rewritten, so a
			// file path stays readable.
			text := tc.wantText
			if text == "" {
				text = tc.stored
			}
			// The value sits in a span beside the glyph marking what the link
			// opens, so the anchor is matched rather than its closing tag.
			if !strings.Contains(detail, `>`+text+`</span></a>`) {
				t.Errorf("detail Link should show %q", text)
			}
		})
	}
}

// TestStorageRefLeavesTheStoredValueAlone covers the two places that must keep
// seeing the path rather than the URL: the CSV export and the copy button.
func TestStorageRefLeavesTheStoredValueAlone(t *testing.T) {
	const path = "images/galleries/cover.jpg"
	srv := newMediaServer(t, mediaRow{Cover: path, Doc: path})

	csv := fetchOK(t, srv.URL+"/admin/media_rows?export=all")
	if !strings.Contains(csv, path) {
		t.Errorf("the export should carry the stored path, got: %s", csv)
	}
	if strings.Contains(csv, "_uploads") {
		t.Error("the export should not carry a resolved URL")
	}

	grid := fetchOK(t, srv.URL+"/admin/media_rows")
	if !strings.Contains(grid, `data-steward-copy="`+path+`"`) {
		t.Error("Copyable should copy the stored path")
	}
}

// TestStorageRefEmptyValue covers an unset upload. An empty src asks the browser
// for the current page again, and resolving "" would point at the uploads root.
func TestStorageRefEmptyValue(t *testing.T) {
	srv := newMediaServer(t, mediaRow{})

	grid := fetchOK(t, srv.URL+"/admin/media_rows")
	if strings.Contains(grid, `<img src=""`) || strings.Contains(grid, `src="/admin/_uploads/"`) {
		t.Error("an empty value should render no image at all")
	}
	detail := fetchOK(t, srv.URL+"/admin/media_rows/1")
	if strings.Contains(detail, `src="/admin/_uploads/"`) || strings.Contains(detail, `href="/admin/_uploads/"`) {
		t.Error("an empty value should not resolve to the uploads root")
	}
}
