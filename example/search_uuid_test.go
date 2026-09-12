package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

// The arrangement documented under "Any column type, not only text" in
// steward-site/content/docs/databases.md.
//
// A column whose Go type is string and whose database type is uuid. The
// database type comes from the tag, so reflection over the model reports
// string and says nothing about what the engine will accept.
type mediaFile struct {
	ID           string `gorm:"type:uuid;primaryKey"`
	SubmissionID string `gorm:"type:uuid;index;not null"`
	FieldKey     string `gorm:"size:64;not null"`

	Submission *mediaSubmission `gorm:"foreignKey:SubmissionID"`
}

type mediaSubmission struct {
	ID    string `gorm:"type:uuid;primaryKey"`
	Label string `gorm:"size:64"`
}

const (
	mediaSubmissionID = "6f1e7c7e-4f3a-4f0e-9a4d-2b7c1e5d8a91"
	mediaOtherSubID   = "0b2c9d44-1f8e-4a63-8f21-ab55c0d3e7f2"
)

// A panel whose quick search and palette both read a uuid column, which is the
// arrangement that has to survive: quick search searches every declared path at
// once, so one column the engine will not pattern-match takes the rest with it.
func newUUIDSearchServer(t *testing.T) (*steward.Admin, *tfaClient) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&mediaSubmission{}, &mediaFile{}); err != nil {
		t.Fatal(err)
	}
	subs := []mediaSubmission{
		{ID: mediaSubmissionID, Label: "first"},
		{ID: mediaOtherSubID, Label: "second"},
	}
	if err := db.Create(&subs).Error; err != nil {
		t.Fatal(err)
	}
	rows := []mediaFile{
		{ID: "1f8b9c2e-7d45-4a11-9c3e-5a6b7c8d9e0f", SubmissionID: mediaSubmissionID, FieldKey: "foto_tempat"},
		{ID: "2a9cad3f-8e56-4b22-ad4f-6b7c8d9e0f1a", SubmissionID: mediaOtherSubID, FieldKey: "tanda_tangan"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("search-uuid-test-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[mediaFile](app).
		Slug("media-files").
		Command("ID", "FieldKey").
		Grid(func(g *steward.Grid[mediaFile]) {
			g.Column("FieldKey")
			g.Column("SubmissionID")
			g.QuickSearch("SubmissionID", "FieldKey")
		})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")
	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatalf("login failed: %d", code)
	}
	return app, c
}

// Quick search sends every declared path into one OR, so a term that matches a
// plain varchar column still has to travel beside the uuid one.
func TestQuickSearchReadsAUUIDColumn(t *testing.T) {
	_, c := newUUIDSearchServer(t)

	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"a bare term", "foto_tempat", "foto_tempat"},
		{"a term matching nothing", "zzzznothing", ""},
		{"the uuid itself", mediaSubmissionID, "foto_tempat"},
		{"part of the uuid", mediaSubmissionID[:8], "foto_tempat"},
		{"field:value contains", "FieldKey:%foto%", "foto_tempat"},
		{"field:value prefix", "FieldKey:foto%", "foto_tempat"},
		{"prefix on the uuid column", "SubmissionID:6f1e%", "foto_tempat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := c.get("/media-files?q=" + urlQuery(tc.query))
			if code != http.StatusOK {
				t.Fatalf("grid = %d, want 200 (body: %.200s)", code, body)
			}
			if tc.want == "" {
				if strings.Contains(body, "foto_tempat") {
					t.Errorf("a term matching nothing returned a row")
				}
				return
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("grid did not return the matching row for %q", tc.query)
			}
		})
	}
}

// Equality against a uuid column resolves without a cast, because the literal
// is coerced to the column's type. Casting the column would compare text to
// text and lose the index.
func TestFieldEqualityOnAUUIDColumnStillWorks(t *testing.T) {
	_, c := newUUIDSearchServer(t)

	code, body := c.get("/media-files?q=" + urlQuery("SubmissionID:"+mediaSubmissionID))
	if code != http.StatusOK {
		t.Fatalf("grid = %d, want 200 (body: %.200s)", code, body)
	}
	if !strings.Contains(body, "foto_tempat") {
		t.Error("equality on the uuid column returned no row")
	}
	if strings.Contains(body, "tanda_tangan") {
		t.Error("equality on the uuid column returned a row it does not match")
	}
}

// The palette answers 200 whatever happens, so a broken predicate shows up as
// an empty list rather than an error: the assertion has to be that the row is
// there.
func TestCommandPaletteReadsAUUIDColumn(t *testing.T) {
	_, c := newUUIDSearchServer(t)

	for _, q := range []string{"foto_tempat", "1f8b9c2e"} {
		code, body := c.get("/_command?q=" + urlQuery(q))
		if code != http.StatusOK {
			t.Fatalf("palette = %d, want 200", code)
		}
		if !strings.Contains(body, "foto_tempat") {
			t.Errorf("palette found nothing for %q: %s", q, body)
		}
	}
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", ":", "%3A", " ", "+").Replace(s)
}

// The other caller of the predicate: a relation path searches through a
// subquery, which has the related table's column name and nothing about its
// type.
func TestQuickSearchReadsAUUIDColumnThroughARelation(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&mediaSubmission{}, &mediaFile{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mediaSubmission{ID: mediaSubmissionID, Label: "first"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mediaFile{
		ID: "3c7d0e11-2f34-4a55-8b66-9c0d1e2f3a4b", SubmissionID: mediaSubmissionID, FieldKey: "foto_tempat",
	}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("search-uuid-relation-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only the relation path is uuid here, so the subquery is the one branch
	// that has to carry the cast.
	steward.Register[mediaSubmission](app).Slug("media-submissions")
	steward.Register[mediaFile](app).
		Slug("media-files").
		Grid(func(g *steward.Grid[mediaFile]) {
			g.Column("FieldKey")
			g.QuickSearch("FieldKey", "Submission.ID")
		})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	seedUser(t, app, "root", "correct-horse")
	c := new2FAClient(t, srv)
	if code, _ := c.login("root", "correct-horse"); code >= 400 {
		t.Fatal("login failed")
	}

	code, body := c.get("/media-files?q=" + urlQuery(mediaSubmissionID[:8]))
	if code != http.StatusOK {
		t.Fatalf("grid = %d, want 200 (body: %.160s)", code, body)
	}
	if !strings.Contains(body, "foto_tempat") {
		t.Error("searching the related uuid returned no row")
	}
}
