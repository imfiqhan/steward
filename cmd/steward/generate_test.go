package main

import (
	"strings"
	"testing"
)

// A type outside the set used to fall through goType's lookup and become a
// string column: the spec was wrong, the generated model compiled, and nothing
// said so. This is the whole reason the set is checked where the spec is read.
func TestParseFieldsRefusesATypeThatDoesNotExist(t *testing.T) {
	_, err := parseFields("title:strnig")
	if err == nil {
		t.Fatal("an unknown type was accepted")
	}
	for _, want := range []string{
		`field "title": unknown type "strnig"`,
		"did you mean: string",
		"available: bool, color, date",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message is missing %q: %v", want, err)
		}
	}
}

// Every type the generator can emit has to parse, or the check above turns a
// working spec into an error.
func TestParseFieldsAcceptsEveryTypeItCanEmit(t *testing.T) {
	for name := range fieldTypes {
		spec := "col:" + name
		if name == "enum" {
			spec = "col:enum(a,b)"
		}
		if name == "fk" {
			spec = "col:fk(others)"
		}
		if _, err := parseFields(spec); err != nil {
			t.Errorf("%q is a type the generator emits but the parser refused it: %v", name, err)
		}
	}
}

func TestParseFieldsRefusesAnUnknownModifier(t *testing.T) {
	_, err := parseFields("title:string:nulable")
	if err == nil {
		t.Fatal("an unknown modifier was accepted")
	}
	if !strings.Contains(err.Error(), "did you mean: nullable") {
		t.Errorf("the message does not name the modifier meant: %v", err)
	}
}

func TestParseFieldsKeepsWhatItParses(t *testing.T) {
	specs, err := parseFields("title:string,due_at:datetime:nullable,author_id:fk(authors):index")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("parsed %d fields, want 3", len(specs))
	}
	if specs[1].Type != "datetime" || !specs[1].Nullable {
		t.Errorf("due_at parsed as %+v", specs[1])
	}
	if specs[2].Args != "authors" || !specs[2].Index {
		t.Errorf("author_id parsed as %+v", specs[2])
	}
	// A nullable non-bool is a pointer; this is what the model file writes.
	if got := specs[1].goType(); got != "*time.Time" {
		t.Errorf("goType = %q, want *time.Time", got)
	}
}

// takeVerbose has to find the flag wherever it was put, since a reader who
// guesses the position guesses wrong half the time.
func TestVerboseIsFoundInEitherPosition(t *testing.T) {
	for _, args := range [][]string{
		{"--verbose", "make:resource", "Post"},
		{"make:resource", "Post", "--verbose"},
	} {
		verbose = false
		rest := takeVerbose(args)
		if !verbose {
			t.Errorf("--verbose was not seen in %v", args)
		}
		for _, a := range rest {
			if a == "--verbose" {
				t.Errorf("--verbose was left in the arguments: %v", rest)
			}
		}
	}
	verbose = false
	if rest := takeVerbose([]string{"publish", "views"}); len(rest) != 2 || verbose {
		t.Errorf("takeVerbose changed arguments that had no flag: %v", rest)
	}
}
