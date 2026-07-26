// The Steward example app: a small blog admin. Run it, then sign in at
// http://localhost:8080/admin with admin / admin.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/ginsteward"

	"github.com/imfiqhan/steward-example/migrations"
	"github.com/imfiqhan/steward-example/models"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "example.db", "SQLite database file")
	flag.Parse()

	if err := run(*addr, *dbPath); err != nil {
		log.Fatal(err)
	}
}

func run(addr, dbPath string) error {
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("opening %s: %w", dbPath, err)
	}

	app, err := steward.New(steward.Config{
		DB:    db,
		Brand: "Steward Blog",
		// Dev-only key: hardcoded so example sessions survive restarts.
		// Real apps must load a secret from the environment.
		SecretKey: []byte("example-dev-secret-do-not-reuse"),
		Dev:       os.Getenv("STEWARD_DEV") != "",
		Logger:    slog.Default(),
	})
	if err != nil {
		return err
	}

	registerResources(app)

	// Apply the app's own migrations (framework migrations run at Build).
	if applied, err := app.MigrationRunner(migrations.All).Up(context.Background()); err != nil {
		return fmt.Errorf("migrations: %w", err)
	} else if len(applied) > 0 {
		slog.Info("migrations applied", "names", applied)
	}

	r := gin.Default()
	if err := ginsteward.Mount(r, app); err != nil {
		return err
	}

	slog.Info("steward example listening", "addr", addr, "panel", "http://localhost"+addr+"/admin")
	return r.Run(addr)
}

func registerResources(app *steward.Admin) {
	posts := steward.Register[models.Post](app).
		Title("Posts").
		Icon("news").
		Group("Content").
		Grid(func(g *steward.Grid[models.Post]) {
			g.Column("ID").Sortable().Width(60)
			g.Column("Title").Sortable().Editable()
			g.Column("Status").Badge(map[any]string{"draft": "secondary", "published": "green"})
			g.Column("Featured").Switch().Width(80)
			g.Column("Author.Name", "Author")
			g.Column("PublishedAt", "Published").Sortable()
			g.Column("CreatedAt", "Created").Sortable()
			g.QuickSearch("Title", "Body")
			g.DefaultSort("ID", true)
			g.Filter(func(f *steward.Filters[models.Post]) {
				f.Equal("Status").Select(steward.Options{"draft": "Draft", "published": "Published"})
				f.Like("Title")
				f.Between("CreatedAt", "Created").Datetime()
			})
			publish := func(c *steward.Context, ids []string) (*steward.Envelope, error) {
				if len(ids) == 0 {
					return steward.Error("Nothing selected."), nil
				}
				err := c.Admin.DB().WithContext(c.Ctx()).
					Model(&models.Post{}).Where("id IN ?", ids).
					Updates(map[string]any{"status": "published", "published_at": time.Now()}).Error
				if err != nil {
					return nil, err
				}
				return steward.Success("Published.").Refresh(), nil
			}
			g.RowAction(steward.NewAction("publish", "Publish", publish).Icon("upload"))
			g.BatchAction(steward.NewAction("publish-selected", "Publish selected", publish).
				Icon("upload").Confirm("Publish all selected posts?"))
		}).
		Form(func(f *steward.Form[models.Post]) {
			f.Text("Title").Rules("required|max:255").Placeholder("Post title")
			f.Markdown("Body").Rules("required")
			f.Radio("Status").Options(steward.Options{"draft": "Draft", "published": "Published"}).Default("draft")
			f.Switch("Featured")
			f.Datetime("PublishedAt", "Published at").Help("Set automatically when publishing.")
			f.BelongsTo("AuthorID", "Author", "Name", "Author").Rules("required")
			f.Image("Cover").Dir("posts").MaxSize(2 << 20)
			f.Saving(func(c *steward.Context, p *models.Post) error {
				if p.Status == "published" && p.PublishedAt == nil {
					now := time.Now()
					p.PublishedAt = &now
				}
				return nil
			})
		})

	posts.Detail(func(d *steward.Detail[models.Post]) {
		d.Field("ID")
		d.Field("Title")
		d.Field("Status").Badge(map[any]string{"draft": "secondary", "published": "green"})
		d.Field("Author.Name", "Author")
		d.Field("Body").Markdown()
		d.Field("PublishedAt", "Published")
		d.Field("CreatedAt", "Created")
	})

	// Authors stay zero-config for grid/form; detail embeds their posts.
	steward.Register[models.Author](app).
		Title("Authors").
		Icon("users").
		Group("Content").
		Detail(func(d *steward.Detail[models.Author]) {
			d.Field("ID")
			d.Field("Name")
			d.Field("Email").Link()
			steward.RelationGrid[models.Author, models.Post](d, "Posts by this author",
				func(q *steward.ListQuery, a *models.Author) {
					q.Conds = append(q.Conds, steward.Cond{Path: "AuthorID", Op: steward.OpEq, Val: a.ID})
				})
		})
}
