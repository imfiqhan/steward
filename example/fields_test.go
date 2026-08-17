package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type fieldRow struct {
	ID     uint `gorm:"primaryKey"`
	Name   string
	Colour string
	Price  float64
	Slug   string
}

func newFieldServer(t *testing.T, symbol string, cfgSymbol string) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&fieldRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fieldRow{Name: "a", Colour: "", Price: 25000, Slug: "a-slug"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("fields-test-secret-key-000"),
		AuthExcept: []string{"/field_rows*"}, CurrencySymbol: cfgSymbol,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[fieldRow](app).Form(func(f *steward.Form[fieldRow]) {
		f.Text("Name")
		f.Color("Colour")
		price := f.Currency("Price")
		if symbol != "" {
			price.Symbol(symbol)
		}
		f.Display("Slug")
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db
}

// TestColorLeavesAnUnsetColumnUnset is the defect this control was rebuilt for.
// A native colour input has no empty state: handed "", it reports and submits
// #000000, so opening a record and saving it wrote black over a column nobody
// had set.
func TestColorLeavesAnUnsetColumnUnset(t *testing.T) {
	srv, db := newFieldServer(t, "", "")

	page := getBody(t, srv.URL+"/admin/field_rows/1/edit")
	if !strings.Contains(page, `data-steward-color`) {
		t.Fatal("the colour field did not render its own control")
	}
	// The submitted field is the text input, and it carries nothing.
	if !strings.Contains(page, `name="Colour" value=""`) {
		t.Error("the empty colour reached the form as something other than empty")
	}

	code, body := putField(t, srv, map[string]string{"Name": "a", "Colour": "", "Price": "25000"})
	if code >= 400 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	var row fieldRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Colour != "" {
		t.Errorf("an untouched save wrote %q over an unset colour", row.Colour)
	}
}

// TestColorValidatesItsFormat: the control that submits is a text input now, so
// the format is the server's to check.
func TestColorValidatesItsFormat(t *testing.T) {
	srv, db := newFieldServer(t, "", "")

	for _, bad := range []string{"red", "#ff", "#gggggg", "3366ff", "#3366ff aa"} {
		if code, _ := putField(t, srv, map[string]string{"Name": "a", "Colour": bad}); code != http.StatusUnprocessableEntity {
			t.Errorf("%q was accepted: %d", bad, code)
		}
	}
	// Accepted, and stored in one case.
	if code, body := putField(t, srv, map[string]string{"Name": "a", "Colour": "#3366FF"}); code >= 400 {
		t.Fatalf("a valid colour was refused: %d %s", code, body)
	}
	var row fieldRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Colour != "#3366ff" {
		t.Errorf("Colour = %q, want the lowercase form", row.Colour)
	}
}

// TestCurrencySymbol covers both places it can come from. The template used to
// hardcode "$", which is wrong everywhere it is wrong.
func TestCurrencySymbol(t *testing.T) {
	srv, _ := newFieldServer(t, "", "")
	if page := getBody(t, srv.URL+"/admin/field_rows/1/edit"); !strings.Contains(page, `>$</span>`) {
		t.Error("the default symbol is not $")
	}

	srv, _ = newFieldServer(t, "", "€")
	if page := getBody(t, srv.URL+"/admin/field_rows/1/edit"); !strings.Contains(page, `>€</span>`) {
		t.Error("Config.CurrencySymbol did not reach the field")
	}

	srv, _ = newFieldServer(t, "Rp", "€")
	page := getBody(t, srv.URL+"/admin/field_rows/1/edit")
	if !strings.Contains(page, `>Rp</span>`) {
		t.Error("Field.Symbol did not override the app default")
	}
	if strings.Contains(page, `>€</span>`) {
		t.Error("both symbols rendered")
	}
}

// TestDisplayIsReadOnlyNotDisabled: either way the value never submits, but a
// disabled input cannot be focused or selected, and a value shown read-only is
// usually one someone wants to copy.
func TestDisplayIsReadOnlyNotDisabled(t *testing.T) {
	srv, db := newFieldServer(t, "", "")
	page := getBody(t, srv.URL+"/admin/field_rows/1/edit")

	i := strings.Index(page, `id="field-Slug"`)
	if i < 0 {
		t.Fatal("the Display field did not render")
	}
	tag := page[i:]
	tag = tag[:strings.Index(tag, ">")]
	if strings.Contains(tag, "disabled") {
		t.Errorf("still disabled, so its value cannot be selected: %s", tag)
	}
	if !strings.Contains(tag, "readonly") {
		t.Errorf("not read-only, so it can be edited: %s", tag)
	}
	if strings.Contains(tag, `name=`) {
		t.Errorf("a Display field must not submit: %s", tag)
	}

	// And a forged submission still does not write it.
	if code, body := putField(t, srv, map[string]string{"Name": "a", "Slug": "forged"}); code >= 400 {
		t.Fatalf("PUT = %d %s", code, body)
	}
	var row fieldRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Slug != "a-slug" {
		t.Errorf("a Display field was written from a forged submission: %q", row.Slug)
	}
}

func putField(t *testing.T, srv *httptest.Server, values map[string]string) (int, string) {
	t.Helper()
	return putRow2(t, srv, "/admin/field_rows/1/edit", "/admin/field_rows/1", values)
}

// putRow2 loads a form for its CSRF token and submits the given values.
func putRow2(t *testing.T, srv *httptest.Server, editPath, putPath string, values map[string]string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + editPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	form := url.Values{"_token": {m[1]}}
	for k, v := range values {
		form.Set(k, v)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+putPath, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", m[1])
	out, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	b, _ := io.ReadAll(out.Body)
	return out.StatusCode, string(b)
}
