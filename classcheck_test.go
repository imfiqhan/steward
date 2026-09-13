package steward

import (
	"strings"
	"testing"
	"testing/fstest"
)

// The stylesheet a panel serves, in the shape Tailwind writes it.
const testCSS = `.btn{display:inline-flex}.card{border-radius:1rem}
.steward-form-grid{display:grid}.sm\:grid-cols-2{grid-template-columns:repeat(2,1fr)}
.p-2\.5{padding:.625rem}.w-1\/2{width:50%}.hidden{display:none}`

func TestUnknownTemplateClasses(t *testing.T) {
	overlay := fstest.MapFS{
		"layout/base.html": &fstest.MapFile{Data: []byte(
			`<div class="card p-2.5"><span class="btn">x</span></div>`)},
		"pages/report.html": &fstest.MapFile{Data: []byte(
			`<div class="grid justify-items-center mx-auto h-16">y</div>`)},
		"pages/ok.html": &fstest.MapFile{Data: []byte(
			`<div class="sm:grid-cols-2 w-1/2 steward-form-grid">z</div>`)},
		// A template action inside the attribute is not a class name, and the
		// names inside its branches are.
		"pages/conditional.html": &fstest.MapFile{Data: []byte(
			`<div class="btn {{if .X}}hidden{{else}}gap-5{{end}}">w</div>`)},
		"pages/notes.txt": &fstest.MapFile{Data: []byte(`class="not-html"`)},
		// An action that quotes a string of its own, which is how the
		// framework's own layout opens.
		"layout/quoted.html": &fstest.MapFile{Data: []byte(
			`<html class="{{if eq .Page.Theme "dark"}}hidden{{end}}"><b class="btn gap-7">q</b></html>`)},
	}

	got := strings.Join(unknownTemplateClasses(overlay, testCSS), "\n")

	for _, want := range []string{
		"pages/report.html: grid, h-16, justify-items-center, mx-auto",
		"pages/conditional.html: gap-5",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "layout/quoted.html: gap-7") {
		t.Errorf("a quoted string inside an action confused the reader:\n%s", got)
	}
	for _, unwanted := range []string{
		"base.html", "ok.html", "notes.txt", "hidden", "not-html",
		".Page.Theme", "eq", "dark",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("reported %q, which is either defined or not a template:\n%s", unwanted, got)
		}
	}
}

// Escaped selectors are the normal case for a utility framework, and reading
// them wrongly would report every responsive class in every overlay.
func TestDefinedClassesUnescapes(t *testing.T) {
	defined := definedClasses(testCSS)
	for _, name := range []string{"sm:grid-cols-2", "p-2.5", "w-1/2", "btn", "steward-form-grid"} {
		if !defined[name] {
			t.Errorf("%q is in the stylesheet but was not read as defined", name)
		}
	}
	if defined["justify-items-center"] {
		t.Error("a class with no rule was read as defined")
	}
}

// An arbitrary value is written as given and cannot be looked up, so it is not
// worth a line of output every time.
func TestArbitraryValuesAreNotReported(t *testing.T) {
	overlay := fstest.MapFS{"p.html": &fstest.MapFile{
		Data: []byte(`<div class="max-w-[calc(100vw-2rem)] grid-cols-[1fr_auto]">x</div>`)}}
	if got := unknownTemplateClasses(overlay, testCSS); len(got) != 0 {
		t.Errorf("arbitrary values were reported: %v", got)
	}
}

// Without a stylesheet to compare against there is no answer, and guessing one
// would report every class a panel uses.
func TestNoStylesheetNoWarnings(t *testing.T) {
	overlay := fstest.MapFS{"p.html": &fstest.MapFile{Data: []byte(`<div class="anything">x</div>`)}}
	if got := unknownTemplateClasses(overlay, ""); len(got) != 0 {
		t.Errorf("reported %v with no stylesheet to check against", got)
	}
}
