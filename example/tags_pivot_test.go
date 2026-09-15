package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	steward "github.com/imfiqhan/steward"
)

type pivotTagRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// newPivotTagServer mounts a Tags field whose values live somewhere other than a
// column of the row: ValuesFunc supplies them, a Saved hook takes them back.
func newPivotTagServer(t *testing.T, seen *seenTags, held []string) *httptest.Server {
	t.Helper()
	db := testDB(t)
	if err := db.AutoMigrate(&pivotTagRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&pivotTagRow{Name: "one"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("tags-pivot-test-secret-key"),
		AuthExcept: []string{"/pivot_tag_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[pivotTagRow](app)
	res.Form(func(f *steward.Form[pivotTagRow]) {
		f.Text("Name")
		f.Tags("Tags", "Tags").Virtual().
			ValuesFunc(func(_ *steward.Context, m any) []string {
				if r, ok := m.(*pivotTagRow); ok && r.ID == 1 {
					return held
				}
				return nil
			})
		if seen != nil {
			f.Saved(func(c *steward.Context, _ *pivotTagRow, _ bool) error {
				seen.set(c.R.Form["Tags"])
				return nil
			})
		}
	})
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, app)
	t.Cleanup(srv.Close)
	return srv
}

// A virtual Tags field has no column behind it, so the chips a record opens
// with can only come from ValuesFunc. Without it the editor sees an empty field
// and saving wipes the rows it was standing for.
func TestVirtualTagsOpenOnWhatTheRowHolds(t *testing.T) {
	srv := newPivotTagServer(t, nil, []string{"jatim", "kominfo"})
	page := getBody(t, srv.URL+"/admin/pivot_tag_rows/1/edit")

	got := hiddenValue(t, page, "Tags")
	var vals []string
	if err := json.Unmarshal([]byte(got), &vals); err != nil {
		t.Fatalf("the value is not a JSON array: %s", got)
	}
	if len(vals) != 2 || vals[0] != "jatim" || vals[1] != "kominfo" {
		t.Fatalf("value = %s, want [jatim kominfo]", got)
	}
}

// Creating a record has no row to read values from, so the field opens empty
// rather than on the last row's chips.
func TestVirtualTagsOpenEmptyOnCreate(t *testing.T) {
	srv := newPivotTagServer(t, nil, []string{"jatim"})
	page := getBody(t, srv.URL+"/admin/pivot_tag_rows/create")
	if got := hiddenValue(t, page, "Tags"); got != "" {
		t.Fatalf("value = %q, want empty", got)
	}
}

// A hook fills a second table from these values, one row per value, so what it
// reads has to be a plain normalised list whatever the client posted.
func TestVirtualTagsSubmission(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want []string
	}{
		{
			name: "the widget posts a JSON array",
			form: url.Values{"Name": {"x"}, "Tags": {`["jatim","kominfo"]`}},
			want: []string{"jatim", "kominfo"},
		},
		{
			name: "repeated values still work",
			form: url.Values{"Name": {"x"}, "Tags": {"jatim", "kominfo"}},
			want: []string{"jatim", "kominfo"},
		},
		{
			name: "an empty list clears",
			form: url.Values{"Name": {"x"}, "Tags": {`[]`}},
			want: []string{},
		},
		{
			name: "a bare value is one tag",
			form: url.Values{"Name": {"x"}, "Tags": {"jatim"}},
			want: []string{"jatim"},
		},
		{
			name: "whitespace is collapsed and blanks dropped",
			form: url.Values{"Name": {"x"}, "Tags": {`["  jawa   timur ","","   ","kominfo"]`}},
			want: []string{"jawa timur", "kominfo"},
		},
		{
			name: "repeats are dropped",
			form: url.Values{"Name": {"x"}, "Tags": {`["jatim","kominfo","jatim"]`}},
			want: []string{"jatim", "kominfo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := &seenTags{}
			srv := newPivotTagServer(t, seen, nil)
			postPivotTagForm(t, srv, tc.form)

			got := seen.get()
			if len(got) != len(tc.want) {
				t.Fatalf("hook saw %#v, want %#v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("hook saw %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

// The editor adds a value at a time. A list this long is a client posting
// whatever it likes, and the hook behind it writes a row per value.
func TestVirtualTagsSubmissionIsBounded(t *testing.T) {
	values := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		values = append(values, "tag-"+strconv.Itoa(i))
	}
	payload, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}

	seen := &seenTags{}
	srv := newPivotTagServer(t, seen, nil)
	postPivotTagForm(t, srv, url.Values{"Name": {"x"}, "Tags": {string(payload)}})

	if got := len(seen.get()); got != 200 {
		t.Fatalf("hook saw %d values, want 200", got)
	}
}

func postPivotTagForm(t *testing.T, srv *httptest.Server, form url.Values) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	page, err := client.Get(srv.URL + "/admin/pivot_tag_rows/create")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	m := comboCSRFRe.FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no CSRF token on the create page")
	}
	form.Set("_token", m[1])

	resp, err := client.PostForm(srv.URL+"/admin/pivot_tag_rows", form)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		t.Fatalf("POST = %d: %s", resp.StatusCode, body)
	}
}

// TagList draws the same chips a Tags column does, for values that are not one.
func TestTagList(t *testing.T) {
	got := string(steward.TagList([]string{"jatim", "", "  ", "kominfo"}))
	if strings.Count(got, `class="steward-tag"`) != 2 {
		t.Fatalf("TagList = %q, want two chips", got)
	}
	if !strings.Contains(got, `<span class="steward-tag-list">`) {
		t.Fatalf("TagList = %q, want the list wrapper", got)
	}
	if esc := string(steward.TagList([]string{`<img src=x onerror=alert(1)>`})); strings.Contains(esc, "<img") {
		t.Fatalf("TagList = %q, want the value escaped", esc)
	}
	if empty := string(steward.TagList(nil)); empty != "" {
		t.Fatalf("TagList(nil) = %q, want empty", empty)
	}
}
