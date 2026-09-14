package steward

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"github.com/imfiqhan/steward/internal/suggest"
	"github.com/imfiqhan/steward/migrate"
)

// App wires an application into the standard runtime commands. Because
// migrations are Go code living in the app, runtime operations execute in
// the app binary — `go run . migrate up` — while the `steward` CLI handles
// code generation only.
type App struct {
	// Build constructs the configured Admin (required). It runs for every
	// command; keep it side-effect free beyond wiring.
	Build func() (*Admin, error)

	// Serve starts the HTTP server (optional). The default serves the
	// admin on Addr with net/http.
	Serve func(a *Admin) error

	// Addr is the default listen address for the built-in server (":8080").
	Addr string

	// Migrations are the app's own migrations, registered under "app".
	Migrations []migrate.Migration

	// Jobs registers recurring jobs on the scheduler. They run only in the
	// `worker` command — a separate process from `serve` — so the panel and
	// background work deploy, restart, and scale independently. Use a.DB()
	// for database access.
	Jobs func(a *Admin, s Scheduler) error
}

// CLI parses os.Args and runs one command:
//
//	serve                     start the admin (default)
//	worker                    run App.Jobs on the scheduler (no HTTP)
//	migrate up                apply pending migrations
//	migrate down [-steps N]   roll back (default: last batch)
//	migrate status            list migrations
//	menu:sync                 re-sync menu entries from registered resources
//	search:reindex [-batch]   rebuild the search index from the database
//	admin:create-user         create a panel account interactively
func CLI(app App) {
	if err := runCLI(app, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runCLI(app App, args []string) error {
	if app.Build == nil {
		return fmt.Errorf("steward.CLI: App.Build is required")
	}
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	// Accept both "migrate up" and "migrate:up".
	if strings.HasPrefix(cmd, "migrate:") {
		args = append([]string{strings.TrimPrefix(cmd, "migrate:")}, args...)
		cmd = "migrate"
	}

	// The command name is checked before the panel is built. Built first, a
	// mistyped command reported whatever the database had to say about being
	// unreachable, and `help` needed a working database to print a paragraph.
	switch cmd {
	case "help", "-h", "--help":
		fmt.Println("commands: " + strings.Join(cliCommands, ", "))
		return nil
	}
	if !slices.Contains(cliCommands, cmd) {
		return fmt.Errorf("unknown command %q%s", cmd, suggest.Block(cmd, cliCommands))
	}

	a, err := app.Build()
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		addr := fs.String("addr", defaultAddr(app), "listen address")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if err := a.Build(); err != nil {
			return err
		}
		if app.Serve != nil {
			return app.Serve(a)
		}
		a.log.Info("steward: serving", "addr", *addr, "panel", a.url("/"))
		return http.ListenAndServe(*addr, ServeMux(a))

	case "worker":
		if app.Jobs == nil {
			return fmt.Errorf("no jobs registered — set App.Jobs to use the worker")
		}
		s := NewIntervalScheduler()
		if err := app.Jobs(a, s); err != nil {
			return err
		}
		wctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		s.Start(wctx)
		a.log.Info("steward: worker running", "jobs", len(s.Jobs()))
		<-wctx.Done()
		a.log.Info("steward: worker stopping")
		return nil

	case "migrate":
		sub := "up"
		if len(args) > 0 {
			sub = args[0]
			args = args[1:]
		}
		runner := a.MigrationRunner(app.Migrations)
		switch sub {
		case "up":
			applied, err := runner.Up(ctx)
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				fmt.Println("nothing to migrate")
				return nil
			}
			for _, name := range applied {
				fmt.Println("applied:", name)
			}
			return nil
		case "down":
			fs := flag.NewFlagSet("migrate down", flag.ContinueOnError)
			steps := fs.Int("steps", 0, "how many migrations to roll back (0 = last batch)")
			force := fs.Bool("force", false, "roll back even when that is every migration applied")
			yes := fs.Bool("yes", false, "same as -force")
			if err := parseFlags(fs, args); err != nil {
				return err
			}
			sts, err := runner.Status(ctx)
			if err != nil {
				return err
			}
			n, everything := downPlan(sts, *steps)
			if n == 0 {
				fmt.Println("nothing to roll back")
				return nil
			}
			// A database migrated in one go holds every migration in batch 1,
			// so "the last batch" is all of them — the panel's own tables
			// included. On a development machine the command reads as "undo
			// the last change" and would empty the database instead.
			if everything && !*force && !*yes {
				return fmt.Errorf(
					"migrate down would roll back all %d applied migrations, including the panel's own tables: "+
						"they were applied as one batch, so there is no earlier state to return to.\n"+
						"Roll back fewer with -steps N, or say -force if emptying the database is what you want",
					n)
			}
			return runner.Down(ctx, *steps)
		case "status":
			statuses, err := runner.Status(ctx)
			if err != nil {
				return err
			}
			for _, s := range statuses {
				state := "pending"
				if s.Applied {
					state = fmt.Sprintf("applied (batch %d, %s)", s.Batch, s.AppliedAt.Format("2006-01-02 15:04"))
				}
				fmt.Printf("%-12s %-50s %s\n", s.Source, s.Name, state)
			}
			return nil
		default:
			return fmt.Errorf("unknown migrate subcommand %q%s",
				sub, suggest.Block(sub, migrateSubcommands))
		}

	case "menu:sync":
		if err := a.Build(); err != nil {
			return err
		}
		fmt.Println("menu synced")
		return nil

	case "search:reindex":
		// Indexing on write only ever covers what is written afterwards, so a
		// table that already has rows needs this once before search is honest.
		fs := flag.NewFlagSet("search:reindex", flag.ContinueOnError)
		batch := fs.Int("batch", 500, "rows read and sent per round trip")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		counts, err := a.Reindex(context.Background(), *batch)
		for slug, n := range counts {
			fmt.Printf("%s: %d indexed\n", slug, n)
		}
		if err != nil {
			return err
		}
		if len(counts) == 0 {
			fmt.Println("nothing to index — no resource called Searchable")
		}
		return nil

	case "admin:create-user":
		fs := flag.NewFlagSet("admin:create-user", flag.ContinueOnError)
		username := fs.String("username", "", "login username")
		name := fs.String("name", "", "display name")
		password := fs.String("password", "", "password (prompted when omitted)")
		if err := parseFlags(fs, args); err != nil {
			return err
		}
		if *username == "" {
			return fmt.Errorf("admin:create-user needs a login name: pass -username")
		}
		if *name == "" {
			*name = *username
		}
		// The panel's own tables are created by its build, and this is the one
		// command that writes to them without serving. Without this, creating
		// the first account on a fresh database failed with the driver's own
		// "no such table: admin_users" — a true sentence about the wrong
		// thing. It runs before the password is asked for, so a database that
		// cannot be prepared is reported before anyone types.
		if err := a.Build(); err != nil {
			return err
		}
		pw := *password
		if pw == "" {
			// Reading from a pipe or a CI log here blocks forever, or worse
			// takes the next line of a script as the password.
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return fmt.Errorf("no password given and stdin is not a terminal: pass -password")
			}
			fmt.Print("Password: ")
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			pw = string(raw)
		}
		if len(pw) < 5 {
			return fmt.Errorf("password must be at least 5 characters, got %d", len(pw))
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		u := AdminUser{Username: *username, Name: *name, Password: string(hash)}
		if err := a.db.Create(&u).Error; err != nil {
			return err
		}
		fmt.Printf("created user %s (id %d)\n", u.Username, u.ID)
		return nil

	default:
		// Unreachable: the name was checked before the panel was built.
		return fmt.Errorf("unknown command %q%s", cmd, suggest.Block(cmd, cliCommands))
	}
}

func defaultAddr(app App) string {
	if app.Addr != "" {
		return app.Addr
	}
	return ":8080"
}

// ServeMux is the routing `serve` puts in front of a panel: the panel itself,
// and — when it is mounted under a prefix — a redirect from the root to it.
//
// At the root the panel is the whole mux. The bare-prefix and catch-all
// patterns needed otherwise are then either a second registration of "/",
// which panics, or, built from an empty prefix, not valid patterns at all.
func ServeMux(a *Admin) *http.ServeMux {
	mux := http.NewServeMux()
	if p := a.Prefix(); p != "" {
		mux.Handle(p+"/", a)
		mux.Handle(p, a)
		mux.Handle("/", http.RedirectHandler(p+"/", http.StatusFound))
		return mux
	}
	mux.Handle("/", a)
	return mux
}

// downPlan reports how many migrations "migrate down" would roll back, and
// whether that is every migration applied — which is the state a database
// migrated from nothing is in, since all of it went in as one batch.
func downPlan(sts []migrate.Status, steps int) (n int, everything bool) {
	batches := map[int]int{}
	applied := 0
	maxBatch := 0
	for _, st := range sts {
		if !st.Applied {
			continue
		}
		applied++
		batches[st.Batch]++
		if st.Batch > maxBatch {
			maxBatch = st.Batch
		}
	}
	if applied == 0 {
		return 0, false
	}
	n = batches[maxBatch]
	if steps > 0 {
		n = min(steps, applied)
	}
	return n, n == applied
}

// The command sets the CLI answers with when it is given a name it does not
// have. Kept beside the switch that reads them so the two cannot drift.
var (
	cliCommands = []string{
		"serve", "worker", "migrate", "menu:sync", "search:reindex",
		"admin:create-user", "help",
	}
	migrateSubcommands = []string{"up", "down", "status"}
)

// parseFlags reads a command's flags and reports a bad one the way every other
// error here reads: what is wrong, the nearest flag that exists, and the set.
//
// flag's own ContinueOnError reporting is discarded rather than used. It writes
// the message and the full usage itself and then returns the error, so letting
// it through prints the failure twice in two different shapes.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if err == nil {
		return nil
	}
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(os.Stdout)
		fs.Usage()
		return nil
	}
	var defined []string
	fs.VisitAll(func(f *flag.Flag) { defined = append(defined, "-"+f.Name) })
	// Read the offending name from the arguments rather than from the error's
	// wording, which is flag's to change.
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" || a == "--" {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if name != "" && fs.Lookup(name) == nil {
			return fmt.Errorf("%s: unknown flag -%s%s", fs.Name(), name,
				suggest.Block("-"+name, defined))
		}
	}
	return fmt.Errorf("%s: %w", fs.Name(), err)
}
