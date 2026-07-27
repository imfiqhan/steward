package steward

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

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
}

// CLI parses os.Args and runs one command:
//
//	serve                     start the admin (default)
//	migrate up                apply pending migrations
//	migrate down [-steps N]   roll back (default: last batch)
//	migrate status            list migrations
//	menu:sync                 re-sync menu entries from registered resources
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

	a, err := app.Build()
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch cmd {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", defaultAddr(app), "listen address")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if err := a.Build(); err != nil {
			return err
		}
		if app.Serve != nil {
			return app.Serve(a)
		}
		mux := http.NewServeMux()
		mux.Handle(a.Prefix()+"/", a)
		mux.Handle(a.Prefix(), a)
		mux.Handle("/", http.RedirectHandler(a.Prefix()+"/", http.StatusFound))
		a.log.Info("steward: serving", "addr", *addr, "panel", a.Prefix())
		return http.ListenAndServe(*addr, mux)

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
			fs := flag.NewFlagSet("migrate down", flag.ExitOnError)
			steps := fs.Int("steps", 0, "how many migrations to roll back (0 = last batch)")
			if err := fs.Parse(args); err != nil {
				return err
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
			return fmt.Errorf("unknown migrate subcommand %q (want up, down, status)", sub)
		}

	case "menu:sync":
		if err := a.Build(); err != nil {
			return err
		}
		fmt.Println("menu synced")
		return nil

	case "admin:create-user":
		fs := flag.NewFlagSet("admin:create-user", flag.ExitOnError)
		username := fs.String("username", "", "login username")
		name := fs.String("name", "", "display name")
		password := fs.String("password", "", "password (prompted when omitted)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *username == "" {
			return fmt.Errorf("-username is required")
		}
		if *name == "" {
			*name = *username
		}
		pw := *password
		if pw == "" {
			fmt.Print("Password: ")
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			pw = string(raw)
		}
		if len(pw) < 5 {
			return fmt.Errorf("password must be at least 5 characters")
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

	case "help", "-h", "--help":
		fmt.Println("commands: serve [-addr], migrate up|down|status, menu:sync, admin:create-user")
		return nil

	default:
		return fmt.Errorf("unknown command %q — try: serve, migrate up|down|status, menu:sync, admin:create-user", cmd)
	}
}

func defaultAddr(app App) string {
	if app.Addr != "" {
		return app.Addr
	}
	return ":8080"
}
