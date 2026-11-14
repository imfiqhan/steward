package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type emAddress struct {
	Street string
	City   string
	Zip    string // stored, never on the form
}

type emLink struct {
	Label string
	URL   string
}

type emVenue struct {
	ID      uint `gorm:"primaryKey"`
	Name    string
	Address emAddress `gorm:"serializer:json;type:text"`
	Links   []emLink  `gorm:"serializer:json;type:text"`
}

type emBadVenue struct {
	ID      uint      `gorm:"primaryKey"`
	Address emAddress `gorm:"serializer:json;type:text"`
	Plain   emLink    `gorm:"type:text"`
}

func emForm(f *steward.Form[emVenue]) {
	f.Text("Name")
	steward.Embed(f, "Address", func(ef *steward.Form[emAddress]) {
		ef.Text("Street").Span(8)
		ef.Text("City").Rules("required").Span(4)
	})
	steward.EmbedMany(f, "Links", func(ef *steward.Form[emLink]) {
		ef.Text("Label").Span(4)
		ef.URL("URL").Span(8)
	})
}

func newEmbedServer(t *testing.T) (*nuClient, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&emVenue{}); err != nil {
		t.Fatal(err)
	}
	v := emVenue{
		Name:    "Hall",
		Address: emAddress{Street: "1 Main", City: "Surabaya", Zip: "60111"},
		Links:   []emLink{{Label: "Site", URL: "https://a.example"}, {Label: "Map", URL: "https://b.example"}},
	}
	if err := db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("embed-test-secret-key-0000"),
		AuthExcept: []string{"/em_venues*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[emVenue](app).Form(emForm)
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return embedClient(t, srv), db
}

func embedClient(t *testing.T, srv *httptest.Server) *nuClient {
	t.Helper()
	c := newNuClient(t, srv.URL+"/admin/em_venues")
	m := comboCSRFRe.FindStringSubmatch(c.get("/1/edit"))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	c.csrf = m[1]
	return c
}

func TestEmbedRendersStoredValues(t *testing.T) {
	c, _ := newEmbedServer(t)
	page := c.get("/1/edit")
	for _, want := range []string{
		`<legend>Address</legend>`,
		`name="Address[City]"`, `value="Surabaya"`,
		`name="Links[0][URL]"`, `value="https://a.example"`,
		`name="Links[1][Label]"`, `value="Map"`,
		`name="Links[__KEY__][URL]"`,
		`name="_present_Links"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("edit page lacks %s", want)
		}
	}
	if strings.Contains(page, `Address[Zip]`) {
		t.Error("a struct field the embedded form does not declare reached the page")
	}
}

func TestEmbedSaves(t *testing.T) {
	c, db := newEmbedServer(t)
	form := url.Values{
		"Name":                   {"Hall"},
		"Address[Street]":        {"2 Side"},
		"Address[City]":          {"Malang"},
		"_present_Links":         {"1"},
		"Links[0][Label]":        {"Site"},
		"Links[0][URL]":          {"https://a.example"},
		"Links[1][_remove]":      {"1"},
		"Links[1][Label]":        {"Map"},
		"Links[1][URL]":          {"https://b.example"},
		"Links[new_2_ab][Label]": {"Second new"},
		"Links[new_2_ab][URL]":   {"https://d.example"},
		"Links[new_1_cd][Label]": {"First new"},
		"Links[new_1_cd][URL]":   {"https://c.example"},
	}
	code, body := c.send("PUT", "/1", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if code != http.StatusOK {
		t.Fatalf("save = %d %s", code, body)
	}
	var v emVenue
	if err := db.First(&v, 1).Error; err != nil {
		t.Fatal(err)
	}
	if v.Address != (emAddress{Street: "2 Side", City: "Malang", Zip: "60111"}) {
		t.Errorf("address = %+v; the undeclared Zip should be kept", v.Address)
	}
	var labels []string
	for _, l := range v.Links {
		labels = append(labels, l.Label)
	}
	if strings.Join(labels, ",") != "Site,First new,Second new" {
		t.Errorf("links = %v, want kept row, then added rows in the order added", labels)
	}
}

func TestEmbedValidatesAndEmpties(t *testing.T) {
	c, db := newEmbedServer(t)
	form := url.Values{"Address[Street]": {"x"}, "Address[City]": {""}}
	code, body := c.send("PUT", "/1", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"Address[City]"`) {
		t.Errorf("a required embedded field should fail by its input name: %d %s", code, body)
	}
	bad := url.Values{"_present_Links": {"1"}, "Links[0][URL]": {"not a url"}}
	code, body = c.send("PUT", "/1", "application/x-www-form-urlencoded", strings.NewReader(bad.Encode()))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"Links[0][URL]"`) {
		t.Errorf("a row's rule should fail by its input name: %d %s", code, body)
	}

	empty := url.Values{"_present_Links": {"1"}, "Links[0][_remove]": {"1"}, "Links[1][_remove]": {"1"}}
	if code, body := c.send("PUT", "/1", "application/x-www-form-urlencoded", strings.NewReader(empty.Encode())); code != http.StatusOK {
		t.Fatalf("emptying = %d %s", code, body)
	}
	var v emVenue
	db.First(&v, 1)
	if len(v.Links) != 0 || v.Address.City != "Surabaya" {
		t.Errorf("after removing every row: links %v, address %+v (untouched expected)", v.Links, v.Address)
	}
}

func TestEmbedVerify(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&emBadVenue{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("embed-verify-secret-key-00")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[emBadVenue](app).Form(func(f *steward.Form[emBadVenue]) {
		steward.Embed(f, "Address", func(ef *steward.Form[emAddress]) {
			ef.Image("Street")
		})
		steward.Embed(f, "Plain", func(ef *steward.Form[emLink]) { ef.Text("URL") })
		steward.EmbedMany(f, "Address", func(ef *steward.Form[emAddress]) { ef.Text("City") })
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	err = app.Verify()
	for _, want := range []string{"an embedded form cannot hold", "serializer:json", "not a []"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Verify should report %q, got %v", want, err)
		}
	}
}

// The form page's Embed and EmbedMany example, as written there.
//
// steward-site/content/docs/form.md#embeds
func TestDocumentedEmbedCalls(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&emVenue{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("documented-embed-secret-00")})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[emVenue](app).Form(func(f *steward.Form[emVenue]) {
		f.Text("Name")
		steward.Embed(f, "Address", func(ef *steward.Form[emAddress]) {
			ef.Text("Street").Span(8)
			ef.Text("City").Rules("required").Span(4)
		})
		steward.EmbedMany(f, "Links", func(ef *steward.Form[emLink]) {
			ef.Text("Label").Span(4)
			ef.URL("URL").Span(8)
		}).Label("Links elsewhere")
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
}
