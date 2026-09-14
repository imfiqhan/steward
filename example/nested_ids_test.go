package main

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// Two repeaters on one parent, so a form has two relations and each has to
// answer for its own rows.
type disc struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Cuts   []cut  `gorm:"foreignKey:DiscID"`
	Sleeve []note `gorm:"foreignKey:DiscID"`
}

type cut struct {
	ID     uint `gorm:"primaryKey"`
	DiscID uint `gorm:"index"`
	Title  string
}

type note struct {
	ID     uint `gorm:"primaryKey"`
	DiscID uint `gorm:"index"`
	Body   string
}

type savedNested struct {
	tracks map[string]string
	notes  map[string]string
}

func newNestedIDServer(t *testing.T, seen *savedNested) (*httptest.Server, *gorm.DB) {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&disc{}, &cut{}, &note{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("nested-ids-test-secret-key"), Prefix: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[disc](app).Form(func(f *steward.Form[disc]) {
		f.Text("Title")
		steward.HasMany(f, "Cuts", "DiscID", func(cf *steward.Form[cut]) {
			cf.Text("Title")
		})
		steward.HasMany(f, "Sleeve", "DiscID", func(cf *steward.Form[note]) {
			cf.Text("Body")
		})
		f.Saved(func(c *steward.Context, _ *disc, _ bool) error {
			seen.tracks = c.NestedIDs("Cuts")
			seen.notes = c.NestedIDs("Sleeve")
			return nil
		})
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	return serve(t, app), db
}

var nestedTokenRe = regexp.MustCompile(`name="_token" value="([^"]+)"`)

// postAlbum submits a create form with one new row in each repeater, using the
// made-up keys the browser would have generated.
func postDisc(t *testing.T, srv *httptest.Server, values url.Values) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	resp, err := client.Get(srv.URL + "/admin/discs/create")
	if err != nil {
		t.Fatal(err)
	}
	m := nestedTokenRe.FindStringSubmatch(readBody(t, resp))
	if m == nil {
		t.Fatal("no CSRF token on the create form")
	}
	values.Set("_token", m[1])
	resp, err = client.PostForm(srv.URL+"/admin/discs", values)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode >= 400 {
		t.Fatalf("POST = %d %s", resp.StatusCode, body)
	}
}

// The id a new row is saved as exists for one moment, between Create filling it
// and the row going out of scope. Without this it was discarded, and a record
// referring to a sibling row had nothing to resolve against.
func TestNestedIDsMapFormKeysToSavedRows(t *testing.T) {
	var seen savedNested
	srv, db := newNestedIDServer(t, &seen)

	postDisc(t, srv, url.Values{
		"Title":                   {"Kind of Blue"},
		"Cuts[new_1_aaa][Title]":  {"So What"},
		"Cuts[new_2_bbb][Title]":  {"Blue in Green"},
		"Sleeve[new_3_ccc][Body]": {"Recorded in two sessions"},
	})

	if len(seen.tracks) != 2 {
		t.Fatalf("NestedIDs(\"Cuts\") held %d rows, want 2: %v", len(seen.tracks), seen.tracks)
	}
	for _, key := range []string{"new_1_aaa", "new_2_bbb"} {
		id, ok := seen.tracks[key]
		if !ok {
			t.Errorf("%s is missing from the map: %v", key, seen.tracks)
			continue
		}
		// It has to be the row's real primary key, not the key echoed back.
		if strings.HasPrefix(id, "new_") || id == "" {
			t.Errorf("%s mapped to %q, which is not a saved id", key, id)
			continue
		}
		var row cut
		if err := db.First(&row, id).Error; err != nil {
			t.Errorf("%s mapped to id %q, which no row has: %v", key, id, err)
			continue
		}
		if row.Title == "" {
			t.Errorf("the row at id %q is not the one that was submitted: %+v", id, row)
		}
	}

	// Each relation answers for its own rows and no one else's.
	if len(seen.notes) != 1 {
		t.Errorf("NestedIDs(\"Sleeve\") held %d rows, want 1: %v", len(seen.notes), seen.notes)
	}
	for key := range seen.notes {
		if _, clash := seen.tracks[key]; clash {
			t.Errorf("%s appears under both relations", key)
		}
	}

	// A relation that was not part of this request is empty, not a panic.
	if got := len(seen.tracks) + len(seen.notes); got != 3 {
		t.Errorf("the two relations held %d rows between them, want 3", got)
	}
}

// A row that was already there is keyed by its own id, and is in the map too —
// a caller reads one map rather than deciding per row which kind it is.
func TestNestedIDsIncludeRowsThatAlreadyExisted(t *testing.T) {
	var seen savedNested
	srv, db := newNestedIDServer(t, &seen)

	postDisc(t, srv, url.Values{
		"Title":                  {"First"},
		"Cuts[new_1_aaa][Title]": {"One"},
	})
	var existing cut
	if err := db.First(&existing).Error; err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatUint(uint64(existing.ID), 10)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	signIn(t, client, srv.URL)
	resp, err := client.Get(srv.URL + "/admin/discs/" + strconv.FormatUint(uint64(existing.DiscID), 10) + "/edit")
	if err != nil {
		t.Fatal(err)
	}
	m := nestedTokenRe.FindStringSubmatch(readBody(t, resp))
	if m == nil {
		t.Fatal("no CSRF token on the edit form")
	}
	form := url.Values{
		"_token":                  {m[1]},
		"Title":                   {"First again"},
		"Cuts[" + id + "][Title]": {"One, renamed"},
		"Cuts[new_9_zzz][Title]":  {"Two"},
	}
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/admin/discs/"+strconv.FormatUint(uint64(existing.DiscID), 10), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := readBody(t, resp); resp.StatusCode >= 400 {
		t.Fatalf("PUT = %d %s", resp.StatusCode, body)
	}

	if got := seen.tracks[id]; got != id {
		t.Errorf("an existing row mapped %q to %q, want its own id", id, got)
	}
	if got := seen.tracks["new_9_zzz"]; got == "" || strings.HasPrefix(got, "new_") {
		t.Errorf("the added row mapped to %q, which is not a saved id", got)
	}
}

// Every relation is written before Saved runs, so a hook can resolve a
// reference from one repeater's rows to another's. A hook that ran between the
// two would see half a form.
func TestEveryRelationIsCompleteBySaved(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&disc{}, &cut{}, &note{}); err != nil {
		t.Fatal(err)
	}
	// Recorded from inside persist, in the order the rows actually land.
	var written []string
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("nested-ids-test-secret-key"), Prefix: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[disc](app).Form(func(f *steward.Form[disc]) {
		f.Text("Title")
		steward.HasMany(f, "Cuts", "DiscID", func(cf *steward.Form[cut]) { cf.Text("Title") })
		steward.HasMany(f, "Sleeve", "DiscID", func(cf *steward.Form[note]) { cf.Text("Body") })
		f.Saved(func(c *steward.Context, _ *disc, _ bool) error {
			for name, ids := range map[string]map[string]string{
				"Cuts": c.NestedIDs("Cuts"), "Sleeve": c.NestedIDs("Sleeve"),
			} {
				if len(ids) > 0 {
					written = append(written, name)
				}
			}
			return nil
		})
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)

	postDisc(t, srv, url.Values{
		"Title":                   {"Two repeaters"},
		"Cuts[new_1_aaa][Title]":  {"A"},
		"Sleeve[new_2_bbb][Body]": {"B"},
	})

	if len(written) != 2 {
		t.Fatalf("the hook saw %d relations, want both: %v", len(written), written)
	}
}
