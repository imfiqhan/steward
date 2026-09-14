package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type album struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Tracks []track `gorm:"foreignKey:AlbumID"`
}

type track struct {
	ID      uint `gorm:"primaryKey"`
	AlbumID uint `gorm:"index"`
	Title   string
	Seconds int
	Price   float64
	Note    string
}

func newAlbumServer(t *testing.T, child func(*steward.Form[track])) (*steward.Admin, *httptest.Server) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&album{}, &track{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("nested-field-test-secret-key"),
		Prefix:    "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[album](app).Slug("albums").Form(func(f *steward.Form[album]) {
		f.Text("Title")
		steward.HasMany(f, "Tracks", "AlbumID", child)
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return app, srv
}

// What a row's template already knows how to render, a child field can now ask
// for — the same settings mean the same thing on both sides of a repeater.
func TestNestedFieldCarriesItsSettings(t *testing.T) {
	app, srv := newAlbumServer(t, func(cf *steward.Form[track]) {
		cf.Text("Title").Span(6)
		cf.Number("Seconds").Min(0).Max(600).Span(3)
		cf.Currency("Price").Symbol("Rp").Span(3)
		cf.Text("Note").ReadOnly()
	})
	if err := app.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	seedUser(t, app, "root", "correct-horse")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	code, body := getPath(t, client, srv.URL+"/admin/albums/create")
	if code != http.StatusOK {
		t.Fatalf("create form = %d, want 200", code)
	}
	// The template row is what a row added in the browser is cloned from, so
	// checking the page covers both.
	for _, want := range []string{
		`min="0"`, `max="600"`, "Rp", "readonly",
		"steward-span-6", "steward-span-3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("a nested row did not carry %q", want)
		}
	}
}

// Fieldset groups fields inside a row, as it does outside one.
func TestNestedFieldsetGroupsARow(t *testing.T) {
	app, srv := newAlbumServer(t, func(cf *steward.Form[track]) {
		cf.Text("Title")
		cf.Fieldset("Shown when", func(g *steward.Form[track]) {
			g.Text("Note")
			g.Number("Seconds")
		})
	})
	if err := app.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	seedUser(t, app, "root", "correct-horse")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)

	code, body := getPath(t, client, srv.URL+"/admin/albums/create")
	if code != http.StatusOK {
		t.Fatalf("create form = %d, want 200", code)
	}
	row := body[strings.Index(body, "data-steward-nested-row"):]
	if !strings.Contains(row, "<legend>Shown when</legend>") {
		t.Error("a fieldset inside a row rendered no legend")
	}
	// The grouped pair is together, and the ungrouped field is not in with them.
	fs := row[strings.Index(row, "<legend>Shown when</legend>"):]
	end := strings.Index(fs, "</fieldset>")
	if end < 0 {
		t.Fatal("the fieldset never closes")
	}
	inside := fs[:end]
	if !strings.Contains(inside, "[Note]") || !strings.Contains(inside, "[Seconds]") {
		t.Error("the grouped fields are not inside the fieldset")
	}
	if strings.Contains(inside, "[Title]") {
		t.Error("an ungrouped field was swept into the fieldset")
	}
}

// A setting a row cannot honour is refused at boot rather than dropped at
// render: accepted-then-ignored is the failure that reads as working code.
func TestNestedFieldRefusesWhatItCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(*steward.Form[track])
		want  string
	}{
		{"per-mode rules", func(cf *steward.Form[track]) {
			cf.Text("Title").CreationRules("required")
		}, "CreationRules/UpdateRules"},
		{"per-mode visibility", func(cf *steward.Form[track]) {
			cf.Text("Title").OnlyOnCreate()
		}, "OnlyOnCreate/OnlyOnUpdate"},
		{"a predicate that cannot run", func(cf *steward.Form[track]) {
			cf.Text("Title").Show(func(*steward.Context) bool { return true })
		}, "Show"},
		{"a transform the repeater does not call", func(cf *steward.Form[track]) {
			cf.Text("Title").SavingValue(func(*steward.Context, string) (any, error) { return nil, nil })
		}, "SavingValue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := newAlbumServer(t, tc.build)
			err := app.Verify()
			if err == nil {
				t.Fatalf("%s was accepted inside a nested row", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Verify said %q, which does not name %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), "Tracks") {
				t.Errorf("Verify did not say which repeater: %v", err)
			}
		})
	}
}
