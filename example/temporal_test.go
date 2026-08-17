package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// A datetime input works to the minute unless asked otherwise, and sends back
// what it was shown. Rendering to the minute therefore meant every save
// rewrote a record's seconds to zero — opening an article and saving it
// untouched moved its timestamp.

type timeRow struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	At        time.Time
	StartsAt  time.Time
	Note      string
	Untouched string
}

func newTimeServer(t *testing.T, saving func(*steward.Context, *timeRow) error) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&timeRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&timeRow{
		Title:     "first",
		At:        time.Date(2026, 7, 31, 19, 44, 15, 0, time.Local),
		StartsAt:  time.Date(2026, 1, 1, 8, 30, 45, 0, time.Local),
		Note:      "kept",
		Untouched: "untouched",
	}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("temporal-test-secret-key"),
		AuthExcept: []string{"/time_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[timeRow](app).Form(func(f *steward.Form[timeRow]) {
		f.Text("Title")
		f.Datetime("At")
		f.Time("StartsAt")
		f.Text("Note")
		if saving != nil {
			f.Saving(saving)
		}
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv, db
}

// resubmit loads the edit form and posts every value back unchanged, except
// those overridden — the closest thing to a person opening a record and
// pressing save.
func resubmit(t *testing.T, srv *httptest.Server, override map[string]string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + "/admin/time_rows/1/edit")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	page := string(raw)

	m := comboCSRFRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token")
	}
	form := url.Values{"_token": {m[1]}}
	for _, name := range []string{"Title", "At", "StartsAt", "Note"} {
		form.Set(name, attrValue(page, name))
	}
	for k, v := range override {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/admin/time_rows/1",
		strings.NewReader(form.Encode()))
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
	if out.StatusCode >= 400 {
		b, _ := io.ReadAll(out.Body)
		t.Fatalf("PUT = %d: %s", out.StatusCode, b)
	}
}

// attrValue reads a rendered input's value.
func attrValue(page, name string) string {
	i := strings.Index(page, `name="`+name+`"`)
	if i < 0 {
		return ""
	}
	rest := page[i:]
	j := strings.Index(rest, `value="`)
	if j < 0 {
		return ""
	}
	rest = rest[j+7:]
	k := strings.Index(rest, `"`)
	if k < 0 {
		return ""
	}
	return rest[:k]
}

func TestDatetimeKeepsItsSeconds(t *testing.T) {
	srv, db := newTimeServer(t, nil)

	page := getBody(t, srv.URL+"/admin/time_rows/1/edit")
	if got := attrValue(page, "At"); got != "2026-07-31T19:44:15" {
		t.Errorf("the form rendered %q; without the seconds they cannot come back", got)
	}
	if !strings.Contains(page, `step="1"`) {
		t.Error("the input has no step, so the control works to the minute")
	}

	resubmit(t, srv, nil)

	var row timeRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.At.Second() != 15 {
		t.Errorf("saving untouched moved the time to %s", row.At.Format("15:04:05"))
	}
	// A Time field on a time.Time column used to render and then fail to save.
	if row.StartsAt.Format("15:04:05") != "08:30:45" {
		t.Errorf("StartsAt = %s, want 08:30:45", row.StartsAt.Format("15:04:05"))
	}
}

func TestTimeFieldWritesToATimeColumn(t *testing.T) {
	srv, db := newTimeServer(t, nil)
	resubmit(t, srv, map[string]string{"StartsAt": "09:15:30"})

	var row timeRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if got := row.StartsAt.Format("15:04:05"); got != "09:15:30" {
		t.Errorf("StartsAt = %s, want 09:15:30", got)
	}
}

// TestSavingHookReachesTheDatabase covers what the column list used to be built
// from. It came from the form, so a hook that set a field the form never
// submitted wrote nothing at all, silently.
func TestSavingHookReachesTheDatabase(t *testing.T) {
	srv, db := newTimeServer(t, func(_ *steward.Context, r *timeRow) error {
		r.Untouched = "written by the hook"
		return nil
	})
	resubmit(t, srv, map[string]string{"Title": "second"})

	var row timeRow
	if err := db.First(&row, 1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Untouched != "written by the hook" {
		t.Errorf("the hook's field holds %q; its change never reached the database", row.Untouched)
	}
	if row.Title != "second" {
		t.Errorf("Title = %q", row.Title)
	}
}

// TestUnchangedSaveWritesNothing is the claim the documentation already made.
func TestUnchangedSaveWritesNothing(t *testing.T) {
	srv, db := newTimeServer(t, nil)

	var before timeRow
	if err := db.First(&before, 1).Error; err != nil {
		t.Fatal(err)
	}
	resubmit(t, srv, nil)

	var after timeRow
	if err := db.First(&after, 1).Error; err != nil {
		t.Fatal(err)
	}
	if !after.At.Equal(before.At) || after.Title != before.Title || after.Note != before.Note {
		t.Errorf("an untouched save changed the row: %+v -> %+v", before, after)
	}
}

// TestDateBoundsAreEnforced covers Min and Max. They reach the control as its
// own min and max, which is a hint to whoever is using the page and no obstacle
// at all to anyone who is not — so the same range is checked on save.
func TestDateBoundsAreEnforced(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&timeRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&timeRow{
		Title: "x", At: time.Date(2026, 6, 1, 12, 0, 0, 0, time.Local),
	}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("bounds-test-secret-key"),
		AuthExcept: []string{"/time_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[timeRow](app).Form(func(f *steward.Form[timeRow]) {
		f.Text("Title")
		f.Datetime("At").
			Min(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)).
			Max(time.Date(2026, 12, 31, 23, 59, 59, 0, time.Local))
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	defer srv.Close()

	page := getBody(t, srv.URL+"/admin/time_rows/1/edit")
	for _, want := range []string{`min="2026-01-01T00:00:00"`, `max="2026-12-31T23:59:59"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the control is missing %s", want)
		}
	}

	// Posting past the range directly, as anything not using the page would.
	for _, bad := range []string{"2025-12-31T23:00:00", "2027-01-01T00:00:00"} {
		code, body := putRow(t, srv, map[string]string{"Title": "x", "At": bad})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s was accepted: %d %s", bad, code, body)
		}
	}
	// And a date inside it still saves.
	if code, body := putRow(t, srv, map[string]string{"Title": "x", "At": "2026-06-02T08:00:00"}); code >= 400 {
		t.Errorf("an in-range date was refused: %d %s", code, body)
	}
}

// putRow submits the edit form and reports the status.
func putRow(t *testing.T, srv *httptest.Server, values map[string]string) (int, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + "/admin/time_rows/1/edit")
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
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/admin/time_rows/1",
		strings.NewReader(form.Encode()))
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
