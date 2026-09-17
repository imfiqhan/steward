package steward

import (
	"strings"
	"testing"
)

func TestResolveStyleDefaults(t *testing.T) {
	got, err := resolveStyle("")
	if err != nil {
		t.Fatalf("unset style: %v", err)
	}
	if got != DefaultStyle {
		t.Errorf("unset style = %q, want %q", got, DefaultStyle)
	}
}

// An unknown pack is refused rather than defaulted, and the message lists what
// is available: a panel silently drawn in a style it did not ask for is a bug
// that only shows up as "it looks wrong".
func TestResolveStyleRejectsUnknown(t *testing.T) {
	_, err := resolveStyle("maya")
	if err == nil {
		t.Fatal("an unknown style was accepted")
	}
	for _, want := range []string{"maya", string(StyleMaia), string(StyleVega)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolveStyleAcceptsEvery(t *testing.T) {
	for _, s := range Styles() {
		got, err := resolveStyle(s)
		if err != nil {
			t.Errorf("style %q: %v", s, err)
			continue
		}
		if got != s {
			t.Errorf("style %q resolved to %q", s, got)
		}
	}
}

// Styles hands out a copy, so a caller sorting or truncating the result cannot
// reach into the list the validator reads.
func TestStylesIsACopy(t *testing.T) {
	got := Styles()
	if len(got) == 0 {
		t.Fatal("no styles")
	}
	got[0] = "clobbered"
	if Styles()[0] == "clobbered" {
		t.Error("Styles exposes the package's own slice")
	}
}
