// Command assets builds Steward's frontend bundle without Node: JavaScript
// through the esbuild Go API, CSS through the Tailwind standalone binary
// (a native executable — see `make tailwind-bin`). Output lands in
// assets/dist and ships inside the Go binary via go:embed.
//
//	go run ./tools/assets           # one-shot production build
//	go run ./tools/assets -watch    # rebuild on change (dev)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

func main() {
	watch := flag.Bool("watch", false, "rebuild on change")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}

	js := api.BuildOptions{
		EntryPoints:       []string{filepath.Join(root, "frontend/src/app.js")},
		Outfile:           filepath.Join(root, "assets/dist/app.js"),
		Bundle:            true,
		Write:             true,
		MinifyWhitespace:  !*watch,
		MinifyIdentifiers: !*watch,
		MinifySyntax:      !*watch,
		Target:            api.ES2020,
		LogLevel:          api.LogLevelInfo,
		// htmx uses direct eval on purpose (hx-on:, event filters, js:
		// prefixes); esbuild already disables identifier renaming in that
		// one scope, so its warning is noise for a vendored file we won't
		// change. Anything louder than a warning still gets through.
		LogOverride: map[string]api.LogLevel{"direct-eval": api.LogLevelSilent},
	}

	tw := tailwindCmd(root, *watch)

	if !*watch {
		if result := api.Build(js); len(result.Errors) > 0 {
			os.Exit(1)
		}
		tw.Stdout, tw.Stderr = os.Stdout, os.Stderr
		if err := tw.Run(); err != nil {
			fatal(fmt.Errorf("tailwind: %w (run `make tailwind-bin` to install the standalone binary)", err))
		}
		fmt.Println("assets built into assets/dist")
		return
	}

	ctx, ctxErr := api.Context(js)
	if ctxErr != nil {
		fatal(ctxErr)
	}
	if err := ctx.Watch(api.WatchOptions{}); err != nil {
		fatal(err)
	}
	tw.Stdout, tw.Stderr = os.Stdout, os.Stderr
	if err := tw.Start(); err != nil {
		fatal(fmt.Errorf("tailwind: %w (run `make tailwind-bin`)", err))
	}
	fmt.Println("watching frontend/ — Ctrl-C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	ctx.Dispose()
	_ = tw.Process.Kill()
}

// tailwindCmd builds the CSS compile command. The binary is looked up at
// frontend/.bin/tailwindcss (installed by `make tailwind-bin`), then $PATH.
func tailwindCmd(root string, watch bool) *exec.Cmd {
	bin := filepath.Join(root, "frontend/.bin/tailwindcss")
	if _, err := os.Stat(bin); err != nil {
		bin = "tailwindcss"
	}
	args := []string{
		"--input", filepath.Join(root, "frontend/src/app.css"),
		"--output", filepath.Join(root, "assets/dist/app.css"),
	}
	if watch {
		args = append(args, "--watch")
	} else {
		args = append(args, "--minify")
	}
	return exec.Command(bin, args...)
}

// repoRoot walks up from the working directory to the module root (go.work).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("run from inside the steward repository (go.work not found)")
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
