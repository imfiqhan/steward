package steward

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

// buildNotReached fails the test if the panel is built. A command that cannot
// possibly run should be refused before anything touches a database.
func buildNotReached(t *testing.T) App {
	t.Helper()
	return App{Build: func() (*Panel, error) {
		t.Error("the panel was built for a command that was never going to run")
		return nil, errors.New("unreachable")
	}}
}

func TestUnknownCommandIsRefusedBeforeTheDatabase(t *testing.T) {
	err := runCLI(buildNotReached(t), []string{"srve"})
	if err == nil {
		t.Fatal("an unknown command was accepted")
	}
	for _, want := range []string{`unknown command "srve"`, "did you mean: serve", "available: "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message is missing %q: %q", want, err)
		}
	}
}

// help is a paragraph of text; needing a reachable database to print it meant
// the one command that works when nothing else does did not.
func TestHelpNeedsNothing(t *testing.T) {
	if err := runCLI(buildNotReached(t), []string{"help"}); err != nil {
		t.Errorf("help failed: %v", err)
	}
}

func TestMissingBuildIsNamed(t *testing.T) {
	err := runCLI(App{}, []string{"serve"})
	if err == nil || !strings.Contains(err.Error(), "App.Build is required") {
		t.Errorf("a CLI with no Build said %v", err)
	}
}

// A bad flag used to be reported by flag itself — its message, then its usage,
// then the same failure again from this package's own printer.
func TestUnknownFlagReadsLikeEveryOtherError(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.String("addr", ":8080", "listen address")

	err := parseFlags(fs, []string{"-addrr", ":9000"})
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	got := err.Error()
	for _, want := range []string{`serve: unknown flag -addrr`, "did you mean: -addr", "available: -addr"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q: %q", want, got)
		}
	}
	if strings.Count(got, "unknown flag") != 1 {
		t.Errorf("the failure is reported more than once: %q", got)
	}

	// The flags that exist still parse, including after the offending run.
	if err := parseFlags(fs, []string{"-addr", ":9000"}); err != nil {
		t.Fatalf("a valid flag was refused: %v", err)
	}
	if got := fs.Lookup("addr").Value.String(); got != ":9000" {
		t.Errorf("addr = %q, want :9000", got)
	}
}

// -h is an answer, not a failure.
func TestHelpFlagIsNotAnError(t *testing.T) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.String("addr", ":8080", "listen address")
	if err := parseFlags(fs, []string{"-h"}); err != nil {
		t.Errorf("-h was reported as a failure: %v", err)
	}
}

// Every name the switch answers to is in the list the errors and help read
// from, or a real command is reported as unknown.
func TestTheCommandListMatchesTheSwitch(t *testing.T) {
	for _, cmd := range cliCommands {
		if err := runCLI(App{Build: func() (*Panel, error) {
			return nil, errors.New("stop here")
		}}, []string{cmd}); err != nil && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%q is in cliCommands but the switch does not know it", cmd)
		}
	}
}
