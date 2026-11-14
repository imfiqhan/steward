package steward

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	gormlogger "gorm.io/gorm/logger"

	"github.com/imfiqhan/steward/internal/suggest"
)

// The app CLI's command-catalog commands: `tools`, `mcp`, and one data command
// per catalog entry (`posts:list`, `posts:create`, …). They are known only once
// the panel is built, because the catalog comes from the registered resources.

// principalFlags adds -user and -tenant, defaulting to STEWARD_MCP_USER and
// STEWARD_MCP_TENANT.
func principalFlags(fs *flag.FlagSet) (user, tenant *string) {
	user = fs.String("user", os.Getenv("STEWARD_MCP_USER"),
		"account to act as (default $STEWARD_MCP_USER)")
	tenant = fs.String("tenant", os.Getenv("STEWARD_MCP_TENANT"),
		"workspace slug, under tenancy (default $STEWARD_MCP_TENANT)")
	return user, tenant
}

// runToolsCommand answers `tools`: the catalog, one command's arguments, or
// the whole catalog as MCP tool JSON with -json.
func runToolsCommand(a *Panel, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the catalog as MCP tool definitions")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *asJSON {
		tools := make([]map[string]any, 0, len(a.commands))
		for _, c := range a.commands {
			tools = append(tools, c.toolJSON())
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"tools": tools})
	}
	if name := fs.Arg(0); name != "" {
		c, ok := a.lookupCommand(name)
		if !ok {
			return fmt.Errorf("unknown command %q%s", name, suggest.Block(name, a.commandNames()))
		}
		writeCommandHelp(out, c)
		return nil
	}
	width := 0
	for _, c := range a.commands {
		width = max(width, len(c.Name))
	}
	for _, c := range a.commands {
		_, _ = fmt.Fprintf(out, "%-*s  %s\n", width, c.Name, c.Title)
	}
	return nil
}

// writeCommandHelp describes one command and its arguments: what both
// `tools <command>` and `<command> -h` print.
func writeCommandHelp(out io.Writer, c *command) {
	_, _ = fmt.Fprintf(out, "%s — %s\n\n", c.Name, c.Description)
	_, _ = fmt.Fprintln(out, "  -user string\n      account to act as (default $STEWARD_MCP_USER)")
	_, _ = fmt.Fprintln(out, "  -tenant string\n      workspace slug, under tenancy (default $STEWARD_MCP_TENANT)")
	for _, p := range c.Params {
		req := ""
		if p.Required {
			req = " (required)"
		}
		typ := p.Type
		if len(p.Enum) > 0 {
			typ = strings.Join(p.Enum, "|")
		}
		_, _ = fmt.Fprintf(out, "  --%s %s%s\n      %s\n", p.Name, typ, req, p.Description)
	}
}

// helpFlags are the spellings the flag package treats as a request for help.
var helpFlags = []string{"-h", "-help", "--h", "--help"}

// runDataCommand answers one catalog command from the command line: its
// arguments as flags, its answer as JSON on out. A refusal or a validation
// failure is returned as the error, which CLI prints and exits 1 on.
func runDataCommand(ctx context.Context, a *Panel, c *command, args []string, out io.Writer) error {
	// The flag package's own usage would list every argument as a string, with
	// no required marks or choices; the catalog knows better.
	for _, arg := range args {
		if slices.Contains(helpFlags, arg) {
			writeCommandHelp(out, c)
			return nil
		}
	}
	fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
	user, tenant := principalFlags(fs)
	byName := map[string]param{}
	values := map[string]*string{}
	for _, p := range c.Params {
		byName[p.Name] = p
		values[p.Name] = fs.String(p.Name, "", p.Description)
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%s takes flags only; unexpected %q", c.Name, fs.Arg(0))
	}
	callArgs := map[string]any{}
	var problems []string
	fs.Visit(func(f *flag.Flag) {
		p, ok := byName[f.Name]
		if !ok {
			return // -user, -tenant
		}
		v, err := p.parseArg(*values[f.Name])
		if err != nil {
			problems = append(problems, err.Error())
			return
		}
		callArgs[f.Name] = v
	})
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	// Arguments are checked before an account is looked up, so a mistyped
	// call says so without needing a database round trip to say it.
	if err := c.checkArgs(callArgs); err != nil {
		return err
	}
	p, err := a.mcpPrincipal(ctx, *user, *tenant)
	if err != nil {
		return err
	}
	res, err := a.runCommand(context.Background(), p, c, callArgs)
	if err != nil {
		return err
	}
	if res.IsError {
		return errors.New(res.errorText())
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res.Body)
}

// runMCPCommand answers `mcp`: the catalog over stdio for the account named.
// stdout carries protocol messages only, so the database's logger and anything
// else that prints are moved to stderr first.
func runMCPCommand(ctx context.Context, a *Panel, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	user, tenant := principalFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	p, err := a.mcpPrincipal(ctx, *user, *tenant)
	if err != nil {
		return err
	}
	out := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = out }()
	// GORM's default logger captured stdout when it was created.
	a.db.Logger = gormlogger.New(log.New(os.Stderr, "\r\n", log.LstdFlags), gormlogger.Config{
		SlowThreshold: 200 * time.Millisecond, LogLevel: gormlogger.Warn,
	})
	a.log.Info("steward: mcp serving on stdio", "user", p.User.Username, "tools", len(a.commands))
	return a.serveMCP(ctx, p, os.Stdin, out)
}

// isDataCommand reports whether a CLI word could name a catalog command, which
// is only known once the panel is built.
func isDataCommand(name string) bool {
	return strings.Contains(name, ":") && !strings.HasPrefix(name, "migrate:")
}
