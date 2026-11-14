package main

import (
	"net/http"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type i18nRow struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Active bool
}

func newI18nPanel(t *testing.T, cfg steward.Config) (*steward.Panel, error) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&i18nRow{}); err != nil {
		t.Fatal(err)
	}
	cfg.DB = db
	cfg.SecretKey = []byte("i18n-test-secret-key-0000000")
	cfg.Prefix = "/admin"
	cfg.AuthExcept = []string{"/i18n_rows*"}
	app, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[i18nRow](app).Title("Rows").
		Grid(func(g *steward.Grid[i18nRow]) {
			g.Column("Title")
			g.Column("Active").Bool()
		}).
		Form(func(f *steward.Form[i18nRow]) {
			f.Text("Title").Rules("required")
			f.Switch("Active")
		})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	return app, app.Verify()
}

func TestIndonesianPanel(t *testing.T) {
	app, err := newI18nPanel(t, steward.Config{Locale: "id"})
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)

	page := getBody(t, srv.URL+"/admin/i18n_rows")
	for _, want := range []string{
		`lang="id"`,
		"Belum ada apa-apa di sini",
		`id="steward-i18n"`,
		"Perbaiki isian yang ditandai.",
		"Rows baru",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Indonesian grid lacks %q", want)
		}
	}
	if strings.Contains(page, "Nothing here yet") {
		t.Error("the empty state is still English")
	}

	c := newNuClient(t, srv.URL+"/admin/i18n_rows")
	m := comboCSRFRe.FindStringSubmatch(c.get("/create"))
	if m == nil {
		t.Fatal("no CSRF token")
	}
	c.csrf = m[1]
	code, body := c.send("POST", "", "application/x-www-form-urlencoded", strings.NewReader("Title="))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Title wajib diisi.") {
		t.Errorf("a required rule should answer in Indonesian: %d %s", code, body)
	}
	code, body = c.send("POST", "", "application/x-www-form-urlencoded", strings.NewReader("Title=Satu&Active=on&_present_Active=1"))
	if code != http.StatusOK || !strings.Contains(body, "Rows berhasil dibuat.") {
		t.Errorf("the save message should be Indonesian: %d %s", code, body)
	}
	if page := c.get(""); !strings.Contains(page, ">Ya<") {
		t.Error("a Bool column should say Ya")
	}
	code, body = c.send("DELETE", "/1", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "Data dihapus.") {
		t.Errorf("the delete message should be Indonesian: %d %s", code, body)
	}

	login := getBody(t, srv.URL+"/admin/auth/login")
	if !strings.Contains(login, "Masuk ke akun Anda") || !strings.Contains(login, `lang="id"`) {
		t.Error("the sign-in page should be Indonesian")
	}
}

func TestTranslationsOverrideAndEnglishDefault(t *testing.T) {
	app, err := newI18nPanel(t, steward.Config{Locale: "id", Translations: map[string]map[string]string{
		"id": {"Nothing here yet": "Masih kosong", "Rows": "Baris data"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	page := getBody(t, srv.URL+"/admin/i18n_rows")
	if !strings.Contains(page, "Masih kosong") || !strings.Contains(page, "Baris data") {
		t.Error("Config.Translations should override the catalog and translate an app's own labels")
	}

	en, err := newI18nPanel(t, steward.Config{})
	if err != nil {
		t.Fatal(err)
	}
	srvEn := serve(t, en)
	t.Cleanup(srvEn.Close)
	page = getBody(t, srvEn.URL+"/admin/i18n_rows")
	if !strings.Contains(page, `lang="en"`) || strings.Contains(page, "steward-i18n") || !strings.Contains(page, "Nothing here yet") {
		t.Error("an English panel should be unchanged and embed no catalog")
	}

	if _, err := newI18nPanel(t, steward.Config{Locale: "fr"}); err == nil || !strings.Contains(err.Error(), `Config.Locale "fr"`) {
		t.Errorf("an unknown locale should fail Verify, got %v", err)
	}
}
