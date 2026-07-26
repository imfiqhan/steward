package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdNew(args []string) error {
	var dir, module, db string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--module":
			i++
			module = args[i]
		case "--db":
			i++
			db = args[i]
		default:
			if dir == "" && !strings.HasPrefix(args[i], "-") {
				dir = args[i]
			}
		}
	}
	if dir == "" {
		return fmt.Errorf("usage: steward new <dir> --module <module-path> [--db sqlite|mysql|postgres]")
	}
	if module == "" {
		module = filepath.Base(dir)
	}
	if db == "" {
		db = "sqlite"
	}

	var driverImport, driverOpen string
	switch db {
	case "sqlite":
		driverImport = `"github.com/glebarez/sqlite"`
		driverOpen = `sqlite.Open(dsnOr("app.db"))`
	case "mysql":
		driverImport = `"gorm.io/driver/mysql"`
		driverOpen = `mysql.Open(dsnOr("user:pass@tcp(127.0.0.1:3306)/app?parseTime=true"))`
	case "postgres":
		driverImport = `"gorm.io/driver/postgres"`
		driverOpen = `postgres.Open(dsnOr("host=127.0.0.1 user=postgres dbname=app sslmode=disable"))`
	default:
		return fmt.Errorf("unknown --db %q (want sqlite, mysql, postgres)", db)
	}

	files := map[string]string{
		// Dependencies resolve on the first `go mod tidy`.
		"go.mod": fmt.Sprintf(`module %s

go 1.26

// Until steward is published, point the require at your checkout:
// replace github.com/imfiqhan/steward => /path/to/steward
`, module),

		"main.go": fmt.Sprintf(`package main

import (
	steward "github.com/imfiqhan/steward"

	"%s/app"
	"%s/migrations"
)

func main() {
	steward.CLI(steward.App{
		Build:      app.Build,
		Migrations: migrations.All,
	})
}
`, module, module),

		"app/app.go": fmt.Sprintf(`// Package app wires the Steward admin panel.
package app

import (
	"fmt"
	"os"

	steward "github.com/imfiqhan/steward"
	%s
	"gorm.io/gorm"

	"%s/resources"
)

func dsnOr(def string) string {
	if dsn := os.Getenv("DATABASE_DSN"); dsn != "" {
		return dsn
	}
	return def
}

// Build constructs the configured admin. steward.CLI calls it for every
// command (serve, migrate, ...).
func Build() (*steward.Admin, error) {
	db, err := gorm.Open(%s, &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("opening database: %%w", err)
	}

	secret := os.Getenv("STEWARD_SECRET")
	if secret == "" {
		// Dev fallback — set STEWARD_SECRET in production; changing it
		// invalidates all sessions.
		secret = "dev-secret-change-me-please"
	}

	a, err := steward.New(steward.Config{
		DB:        db,
		Brand:     "%s",
		SecretKey: []byte(secret),
		Dev:       os.Getenv("STEWARD_DEV") != "",
	})
	if err != nil {
		return nil, err
	}
	resources.RegisterAll(a)
	return a, nil
}
`, driverImport, module, driverOpen, filepath.Base(module)),

		"resources/registry.go": `// Package resources declares the app's admin resources.
package resources

import steward "github.com/imfiqhan/steward"

// RegisterAll wires every resource; steward make:resource appends here.
func RegisterAll(a *steward.Admin) {
	_ = a
	// steward:register
}
`,

		"migrations/migrations.go": `// Package migrations lists the app's schema migrations; generated files
// append to All from their init functions.
package migrations

import "github.com/imfiqhan/steward/migrate"

// All is registered with the runner under the "app" source.
var All []migrate.Migration
`,

		".gitignore": `*.db
uploads/
`,

		"README.md": fmt.Sprintf("# %s\n\nA [Steward](https://github.com/imfiqhan/steward) admin panel.\n\n```sh\ngo mod tidy\ngo run . migrate up     # creates framework + app tables (seeds admin/admin)\ngo run . serve          # http://localhost:8080/admin\n```\n\nGenerate resources:\n\n```sh\nsteward make:resource Post --fields \"title:string,body:markdown,status:enum(draft,published)\"\ngo run . migrate up\n```\n", filepath.Base(module)),
	}

	if _, err := os.Stat(dir); err == nil {
		entries, _ := os.ReadDir(dir)
		if len(entries) > 0 {
			return fmt.Errorf("%s already exists and is not empty", dir)
		}
	}
	for rel, content := range files {
		if err := writeFile(filepath.Join(dir, rel), []byte(content), false); err != nil {
			return err
		}
		fmt.Println("created:", filepath.Join(dir, rel))
	}
	fmt.Printf("\nproject ready — next:\n  cd %s\n  go mod tidy\n  go run . migrate up\n  go run . serve\n", dir)
	return nil
}
