package main

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// csrfRe pulls the token out of the rendered form.
var comboCSRFRe = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

type comboRow struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// seenTags records what a hook read out of the submitted form, which is the
// contract that matters: the widget's own wire shape must not reach handlers.
type seenTags struct {
	mu   sync.Mutex
	vals []string
}

func (s *seenTags) set(v []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vals = append([]string(nil), v...)
}

func (s *seenTags) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vals
}

func newComboServer(t *testing.T, seen *seenTags) *httptest.Server {
	return newComboServerWith(t, steward.Options{
		// Labels deliberately out of alphabetical order relative to their
		// values, so a sorted render cannot be mistaken for map luck.
		"3": "apple",
		"1": "cherry",
		"2": "banana",
	}, seen)
}

func newComboServerWith(t *testing.T, opts steward.Options, seen *seenTags) *httptest.Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.TempDir()+"/combo.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&comboRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&comboRow{Name: "one"}).Error; err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("combobox-test-secret-key"),
		AuthExcept: []string{"/combo_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[comboRow](app)
	res.Form(func(f *steward.Form[comboRow]) {
		f.Text("Name")
		f.MultiSelect("Tags", "Tags").
			Options(opts).
			ValuesFunc(func(_ *steward.Context, m any) []string {
				if r, ok := m.(*comboRow); ok && r.ID == 1 {
					return []string{"2"}
				}
				return nil
			})
		if seen != nil {
			f.Saved(func(c *steward.Context, _ *comboRow, _ bool) error {
				seen.set(c.R.Form["Tags"])
				return nil
			})
		}
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, url string) string {
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

// TestMultiSelectRendersCombobox covers the markup the component needs. Basecoat
// wires a combobox by id and reads its behaviour off the listbox, so the parts
// have to reference each other exactly.
func TestMultiSelectRendersCombobox(t *testing.T) {
	srv := newComboServer(t, nil)
	html := getBody(t, srv.URL+"/admin/combo_rows/create")

	for _, want := range []string{
		`class="combobox w-full" data-auto-highlight="true"`,
		`<input type="text" role="combobox" id="field-Tags"`,
		`aria-controls="field-Tags-listbox"`,
		`id="field-Tags-popover" data-popover aria-hidden="true"`,
		`id="field-Tags-listbox"`,
		// Without this the component renders a single-select, silently.
		`aria-multiselectable="true"`,
		`<div role="option" data-value="1" data-label="cherry">cherry</div>`,
		`<input type="hidden" name="Tags" value="[]"/>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the combobox is missing %s", want)
		}
	}
	// The old widget told the reader to hold a modifier key; nothing should.
	if strings.Contains(html, "Ctrl to select") {
		t.Error("the multiple-select hint outlived the <select multiple> it described")
	}
}

// TestSelectOptionsAreOrdered guards against the option list moving between
// requests. Options are declared as a map, and Go randomises map iteration, so
// this held different orders on consecutive loads.
func TestSelectOptionsAreOrdered(t *testing.T) {
	srv := newComboServer(t, nil)
	want := []string{"apple", "banana", "cherry"}

	for i := 0; i < 5; i++ {
		html := getBody(t, srv.URL+"/admin/combo_rows/create")
		var got []string
		for _, label := range want {
			if idx := strings.Index(html, `data-label="`+label+`"`); idx >= 0 {
				got = append(got, label)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("expected every option to render, got %v", got)
		}
		// Compare positions rather than presence: order is the point.
		var positions []int
		for _, label := range want {
			positions = append(positions, strings.Index(html, `data-label="`+label+`"`))
		}
		for j := 1; j < len(positions); j++ {
			if positions[j] < positions[j-1] {
				t.Fatalf("run %d: options are out of order (%v at %v)", i, want, positions)
			}
		}
	}
}

// hiddenValue pulls a field's submitted value out of the rendered form.
func hiddenValue(t *testing.T, page, name string) string {
	t.Helper()
	re := regexp.MustCompile(`<input type="hidden" name="` + name + `" value="([^"]*)"`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no hidden input for %q", name)
	}
	return html.UnescapeString(m[1])
}

// TestMultiSelectSelectionRoundTrips covers the edit form: what is already
// selected has to come back, or reopening a record silently clears its choices.
//
// Objects rather than bare values, because the list is fetched and replaced as
// the reader types — a chip whose option has gone has nowhere else to read its
// label from, and would show its id.
func TestMultiSelectSelectionRoundTrips(t *testing.T) {
	srv := newComboServer(t, nil)
	page := getBody(t, srv.URL+"/admin/combo_rows/1/edit")

	got := hiddenValue(t, page, "Tags")
	var sel []struct{ Value, Label string }
	if err := json.Unmarshal([]byte(got), &sel); err != nil {
		t.Fatalf("the selection is not a JSON array of objects: %s", got)
	}
	if len(sel) != 1 || sel[0].Value != "2" || sel[0].Label != "banana" {
		t.Fatalf("selection = %s, want one entry {2, banana}", got)
	}
}

// TestMultiSelectShipsOnePage guards the reason this field is fetched rather
// than rendered whole: a form holding thousands of options was most of the page.
func TestMultiSelectShipsOnePage(t *testing.T) {
	opts := steward.Options{}
	for i := 0; i < 500; i++ {
		opts[itoa(uint(i))] = "option " + itoa(uint(i))
	}
	srv := newComboServerWith(t, opts, nil)
	page := getBody(t, srv.URL+"/admin/combo_rows/create")

	rendered := strings.Count(page, `role="option"`)
	if rendered > 50 {
		t.Errorf("the form rendered %d options; one page is 50", rendered)
	}
	if rendered == 0 {
		t.Error("the form rendered no options at all, so the first open is empty")
	}
	// And it has to say where the rest live.
	if !strings.Contains(page, `data-filter="manual"`) ||
		!strings.Contains(page, `data-steward-options="/admin/combo_rows/_options?field=Tags"`) {
		t.Error("the field does not point at its options endpoint")
	}
}

// TestOptionsEndpointFilters covers what the typing actually calls.
func TestOptionsEndpointFilters(t *testing.T) {
	opts := steward.Options{}
	for i := 0; i < 500; i++ {
		opts[itoa(uint(i))] = "option " + itoa(uint(i))
	}
	opts["x"] = "banana"
	srv := newComboServerWith(t, opts, nil)

	var body struct {
		Options []struct{ Value, Label string }
		More    bool
	}
	raw := getBody(t, srv.URL+"/admin/combo_rows/_options?field=Tags&q=banana")
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	if len(body.Options) != 1 || body.Options[0].Label != "banana" {
		t.Fatalf("q=banana returned %d options, want just banana", len(body.Options))
	}

	raw = getBody(t, srv.URL+"/admin/combo_rows/_options?field=Tags")
	body.Options = nil
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Options) != 50 {
		t.Errorf("an unfiltered page returned %d options, want 50", len(body.Options))
	}
	if !body.More {
		t.Error("the reply should say more were left out, so the reader is told to keep typing")
	}
}

// TestMultiSelectSubmission is the contract this rests on: a handler reads
// c.R.Form["Tags"] as a plain list, whichever shape the client posted.
func TestMultiSelectSubmission(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
		want []string
	}{
		{
			name: "the combobox posts a JSON array",
			form: url.Values{"Name": {"x"}, "Tags": {`["1","3"]`}},
			want: []string{"1", "3"},
		},
		{
			// A plain <select multiple>, or any client posting the ordinary way.
			name: "repeated values still work",
			form: url.Values{"Name": {"x"}, "Tags": {"1", "3"}},
			want: []string{"1", "3"},
		},
		{
			name: "an empty selection clears",
			form: url.Values{"Name": {"x"}, "Tags": {`[]`}},
			want: []string{},
		},
		{
			// Not an array: left exactly as it arrived rather than guessed at.
			name: "a bare value is untouched",
			form: url.Values{"Name": {"x"}, "Tags": {"7"}},
			want: []string{"7"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := &seenTags{}
			srv := newComboServer(t, seen)

			// The form page hands out the CSRF token and its cookie; posting
			// without them is refused before any of this is reached.
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar}
			page, err := client.Get(srv.URL + "/admin/combo_rows/create")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(page.Body)
			_ = page.Body.Close()
			m := comboCSRFRe.FindStringSubmatch(string(raw))
			if m == nil {
				t.Fatal("no CSRF token on the create page")
			}
			tc.form.Set("_token", m[1])

			resp, err := client.PostForm(srv.URL+"/admin/combo_rows", tc.form)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				t.Fatalf("POST = %d: %s", resp.StatusCode, body)
			}

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
