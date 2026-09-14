// Command steward scaffolds Steward projects and resources. It only
// generates code — runtime operations (serve, migrate) live in the app
// binary via steward.CLI, because migrations are Go code compiled into it.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/internal/suggest"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = `steward — scaffolding for the Steward admin framework

Usage:
  steward new <dir> --module <module-path> [--db sqlite|mysql|postgres]
  steward make:resource <Name> --fields "<spec>" [--dir <project>] [--force]
  steward make:resource <Name> --from-db --dsn <dsn> [--db sqlite|mysql|postgres] [--table <t>]
  steward make:resource <Name> --from-struct [<models-path>]
  steward make:migration <name> [--dir <project>]
  steward publish views|assets [--dir <target>]

Field spec:
  comma-separated name:type[:modifier...] entries, e.g.
    --fields "title:string,body:markdown,status:enum(draft,published),
              price:decimal,active:bool,published_at:datetime:nullable,
              author_id:fk(authors),cover:image"
  types:     string text markdown int uint float decimal bool date datetime
             time json enum(...) fk(table) email url password color image file
  modifiers: nullable unique index

Runtime commands run through your app binary instead:
  go run . serve | migrate up | migrate status | admin:create-user
`

func run(args []string) error {
	args = takeVerbose(args)
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "new":
		return cmdNew(args)
	case "make:resource":
		return cmdMakeResource(args)
	case "make:migration":
		return cmdMakeMigration(args)
	case "publish":
		return cmdPublish(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q%s\n\n%s", cmd, suggest.Block(cmd, commands), usage)
	}
}

// writeFile refuses to clobber existing files unless force is set.
func writeFile(path string, content []byte, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

// cmdPublish copies embedded templates or assets into the project so they
// can be customized; the overlay FS picks them up automatically.
func cmdPublish(args []string) error {
	what := ""
	dir := "."
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		what, rest = rest[0], rest[1:]
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--dir" && i+1 < len(rest) {
			dir = rest[i+1]
			i++
		}
	}
	var src fs.FS
	var target string
	switch what {
	case "views":
		src = steward.BuiltinTemplates()
		target = filepath.Join(dir, "admin-templates")
	case "assets":
		src = steward.BuiltinAssets()
		target = filepath.Join(dir, "admin-assets")
	default:
		return fmt.Errorf("publish what?%s", suggest.Block(pickPublishArg(args), publishTargets))
	}
	count := 0
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, filepath.FromSlash(p))
		if _, err := os.Stat(dst); err == nil {
			return nil // never clobber customized files
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		wrote(dst)
		count++
		return nil
	})
	if err != nil {
		return err
	}
	note("published %d files to %s", count, target)
	note("wire them in with Config.TemplatesFS / Config.AssetsFS (os.DirFS)")
	return nil
}

// commands is what run dispatches on, for an error that says what it could
// have been given. Kept beside that switch so the two cannot drift.
var commands = []string{"new", "make:resource", "make:migration", "publish", "help"}

// publishTargets is what publish copies out.
var publishTargets = []string{"views", "assets"}

// pickPublishArg is the first non-flag argument, which is what the reader
// meant the target to be. It is "" when they gave none.
func pickPublishArg(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// Output is two streams with two audiences. What was written goes to stdout,
// one bare path per line, so whatever runs this next can read the list without
// parsing prose. Everything else — what to do now, what was skipped — is
// commentary, and is silent unless asked for.
var verbose bool

// takeVerbose removes --verbose wherever it appears, so it works before or
// after the subcommand rather than only in the one position a reader guesses.
func takeVerbose(args []string) []string {
	out := args[:0:0]
	for _, a := range args {
		if a == "--verbose" || a == "-verbose" {
			verbose = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// wrote records one written file. This is the command's whole output on a
// successful run.
func wrote(path string) { fmt.Println(path) }

// note is commentary, printed only under --verbose.
func note(format string, args ...any) {
	if verbose {
		fmt.Printf(format+"\n", args...)
	}
}
