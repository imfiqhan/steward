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
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
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
		return fmt.Errorf("publish what? views or assets")
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
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("published %d files to %s\n", count, target)
	fmt.Println("wire them in with Config.TemplatesFS / Config.AssetsFS (os.DirFS)")
	return nil
}
