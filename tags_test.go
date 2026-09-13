package steward

import (
	"strings"
	"testing"
)

func TestEncodeTagsNormalisesWhatWasPosted(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty stays empty", "", ""},
		{"an array survives", `["go","sql"]`, `["go","sql"]`},
		{"whitespace is collapsed", `["  go  lang ","sql"]`, `["go lang","sql"]`},
		{"blanks are dropped", `["go","","   ","sql"]`, `["go","sql"]`},
		{"repeats are dropped", `["go","sql","go"]`, `["go","sql"]`},
		{"nothing left is empty", `["","  "]`, ""},
		{"an empty array is empty", `[]`, ""},
		// A column promoted from a plain text field, or a client posting the
		// field the ordinary way.
		{"a bare value becomes one tag", "go", `["go"]`},
		{"broken json becomes one tag", `["go"`, `["[\"go\""]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeTags(tc.raw); got != tc.want {
				t.Errorf("encodeTags(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// A client can post any length; the editor adds one value at a time.
func TestEncodeTagsIsBounded(t *testing.T) {
	var parts []string
	for i := 0; i < maxTagValues*2; i++ {
		parts = append(parts, `"v`+string(rune('a'+i%26))+strings.Repeat("x", i%7)+`"`)
	}
	got := decodeStringList(encodeTags("[" + strings.Join(parts, ",") + "]"))
	if len(got) > maxTagValues {
		t.Errorf("kept %d values, the cap is %d", len(got), maxTagValues)
	}
}

func TestTagsHTMLRendersChips(t *testing.T) {
	got := string(tagsHTML(`["go","sql"]`))
	for _, want := range []string{
		`<span class="steward-tag-list">`,
		`<span class="steward-tag">go</span>`,
		`<span class="steward-tag">sql</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("chips are missing %s: %s", want, got)
		}
	}
	if s := string(tagsHTML("")); s != "" {
		t.Errorf("an empty column rendered %q", s)
	}
}

// The values are free text, so they reach the page as text.
func TestTagsHTMLEscapesValues(t *testing.T) {
	got := string(tagsHTML(`["<img src=x onerror=alert(1)>"]`))
	if strings.Contains(got, "<img") {
		t.Fatalf("a stored value reached the page as markup: %s", got)
	}
	if !strings.Contains(got, "&lt;img") {
		t.Errorf("the value was not rendered at all: %s", got)
	}
}
