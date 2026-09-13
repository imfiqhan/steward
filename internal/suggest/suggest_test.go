package suggest

import (
	"strings"
	"testing"
)

func TestNearestOffersOnlyACloseName(t *testing.T) {
	fields := []string{"Title", "Body", "AuthorID", "CreatedAt"}
	cases := []struct {
		input string
		want  string
	}{
		{"Titel", "Title"},  // one transposition
		{"Titl", "Title"},   // one deletion
		{"titles", "Title"}, // case plus one insertion
		{"title", "Title"},  // right name, wrong case
		{"Athr", ""},        // three edits away, past the cut
		{"Slug", ""},        // nothing close
		{"", ""},
	}
	for _, tc := range cases {
		if got := Nearest(tc.input, fields); got != tc.want {
			t.Errorf("Nearest(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// The same mistake has to produce the same message every run, or a reader
// comparing two failures sees a difference that is not one.
func TestNearestAndListAreDeterministic(t *testing.T) {
	// "aa" and "ba" are both one edit from "ca"; the smaller wins.
	if got := Nearest("ca", []string{"ba", "aa"}); got != "aa" {
		t.Errorf("a tie went to %q, want the lexicographically smaller", got)
	}
	if got := List([]string{"zebra", "apple", "mango"}); got != "apple, mango, zebra" {
		t.Errorf("List did not sort: %q", got)
	}
}

func TestListIsCapped(t *testing.T) {
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, "name"+string(rune('a'+i%26))+itoa(i))
	}
	got := List(many)
	if n := strings.Count(got, ","); n != maxList-1 {
		t.Errorf("listed %d names, the cap is %d: %s", n+1, maxList, got)
	}
	if !strings.HasSuffix(got, "... and 25 more") {
		t.Errorf("the remainder was not counted: %q", got)
	}
}

func TestBlockSaysNothingWhenThereIsNothingToSay(t *testing.T) {
	if b := Block("anything", nil); b != "" {
		t.Errorf("an empty candidate set produced %q", b)
	}
	b := Block("Titel", []string{"Title", "Body"})
	if !strings.HasPrefix(b, "\n  did you mean: Title") {
		t.Errorf("the suggestion is missing or misplaced: %q", b)
	}
	if !strings.Contains(b, "\n  available: Body, Title") {
		t.Errorf("the available line is missing: %q", b)
	}
	// A name nothing is close to still gets the set to choose from.
	far := Block("zzzzzz", []string{"Title", "Body"})
	if strings.Contains(far, "did you mean") {
		t.Errorf("a distant name was given a suggestion: %q", far)
	}
	if !strings.Contains(far, "available:") {
		t.Errorf("a distant name lost its candidate list: %q", far)
	}
}
