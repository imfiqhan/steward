package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type layoutRow struct {
	ID              uint `gorm:"primaryKey"`
	Title, Slug     string
	Inside, Outside string
}

func newLayoutServer(t *testing.T, width steward.FormWidth) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&layoutRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&layoutRow{Title: "a"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("layout-test-secret-key-0"),
		AuthExcept: []string{"/layout_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[layoutRow](app).Form(func(f *steward.Form[layoutRow]) {
		if width != "" {
			f.Width(width)
		}
		f.Text("Title").Span(8)
		f.Text("Slug").Span(4)
		f.Fieldset("Publishing", func(f *steward.Form[layoutRow]) {
			f.Text("Inside").Span(6)
		})
		f.Text("Outside").Span(99) // out of range
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

// TestFormSpanReachesTheField covers the width mechanism. The class is fixed
// rather than a Tailwind utility, because the value comes from Go and Tailwind
// only emits classes it can find by scanning the sources.
func TestFormSpanReachesTheField(t *testing.T) {
	page := getBody(t, newLayoutServer(t, "").URL+"/admin/layout_rows/1/edit")

	for _, want := range []string{
		`steward-span-8"`,
		`steward-span-4"`,
		`steward-span-6"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("no field carries %s", want)
		}
	}
	// A span outside 1..12 is ignored rather than emitted.
	if strings.Contains(page, "steward-span-99") {
		t.Error("an out-of-range span reached the page")
	}
	i := strings.Index(page, `data-steward-field="Outside"`)
	if i < 0 {
		t.Fatal("the field is missing")
	}
	if !strings.Contains(page[max(0, i-160):i], "steward-span-12") {
		t.Error("an out-of-range span did not fall back to the full row")
	}
}

// TestFieldsetRendersItsLegend: Fieldset set a value on each field and the
// value reached the view model, where it stopped. No template read it, so a
// declared fieldset drew nothing at all.
func TestFieldsetRendersItsLegend(t *testing.T) {
	page := getBody(t, newLayoutServer(t, "").URL+"/admin/layout_rows/1/edit")

	if !strings.Contains(page, "<legend>Publishing</legend>") {
		t.Error("the fieldset's legend is not on the page")
	}
	// And it holds its own field, not the ones declared outside it.
	start := strings.Index(page, "<legend>Publishing</legend>")
	end := strings.Index(page[start:], "</fieldset>")
	if end < 0 {
		t.Fatal("the fieldset is not closed")
	}
	inside := page[start : start+end]
	if !strings.Contains(inside, `data-steward-field="Inside"`) {
		t.Error("the fieldset does not contain the field declared in it")
	}
	for _, out := range []string{"Title", "Slug", "Outside"} {
		if strings.Contains(inside, `data-steward-field="`+out+`"`) {
			t.Errorf("%s was swept into the fieldset", out)
		}
	}
}

func TestFormWidth(t *testing.T) {
	cases := map[steward.FormWidth]string{
		"":                 "max-w-3xl",
		steward.FormNarrow: "max-w-xl",
		steward.FormNormal: "max-w-3xl",
		steward.FormWide:   "max-w-5xl",
		steward.FormFull:   "max-w-none",
	}
	for w, want := range cases {
		page := getBody(t, newLayoutServer(t, w).URL+"/admin/layout_rows/1/edit")
		if !strings.Contains(page, want) {
			t.Errorf("width %q did not render %s", w, want)
		}
	}
}
