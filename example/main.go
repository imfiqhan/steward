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

	"github.com/imfiqhan/steward/example/migrations"
	"github.com/imfiqhan/steward/example/models"
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

	slog.Info("steward example listening", "addr", addr, "panel", "http://localhost"+addr+app.Prefix()+"/")
	return r.Run(addr)
}

func registerResources(app *steward.Admin) {
	posts := steward.Register[models.Post](app).
		Title("Posts").
		Icon("news").
		Group("Content").
		Grid(func(g *steward.Grid[models.Post]) {
			g.Column("ID").Sortable().Width(60)
			g.Column("Cover").Image(56, 40)
			g.Column("Title").Sortable().Editable()
			g.Column("Status").Badge(map[any]steward.BadgeColor{"draft": steward.BadgeSecondary, "published": steward.BadgeGreen})
			g.Column("Featured").Switch().Width(80)
			g.Column("Author.Name", "Author")
			g.Column("PublishedAt", "Published").Sortable()
			g.Column("CreatedAt", "Created").Sortable()
			g.QuickSearch("Title", "Body")
			g.DefaultSort("ID", true)
			g.GroupColumns("Publishing", "Status", "Featured")
			g.Filter(func(f *steward.Filters[models.Post]) {
				f.Equal("Status").Select(steward.Options{"draft": "Draft", "published": "Published"})
				f.Like("Title")
				f.DateRange("CreatedAt", "Created")
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
			steward.HasMany(f, "Comments", "PostID", func(cf *steward.Form[models.Comment]) {
				cf.Text("Name").Rules("required|max:120")
				cf.Textarea("Body").Rules("required|max:500")
			})
			f.Datetime("PublishedAt", "Published at").
				Min(time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)).
				Max(time.Now().AddDate(1, 0, 0)).
				Help("Set automatically when publishing.")
			f.BelongsTo("AuthorID", "Author", "Name", "Author").Rules("required")
			// Virtual: nothing on the model backs it. It is here so the example
			// exercises the multi-select combobox, which the browser harness
			// then drives.
			f.MultiSelect("Topics", "Topics").
				OptionsFunc(func(*steward.Context) steward.Options {
					// Deliberately larger than one page, so the example
					// exercises the fetch rather than the baked-in list.
					opts := steward.Options{"go": "Go", "css": "CSS", "sql": "SQL"}
					for i := 1; i <= 300; i++ {
						id := fmt.Sprintf("topic-%d", i)
						opts[id] = fmt.Sprintf("Topic %d", i)
					}
					return opts
				}).
				Help("Filter by typing; pick as many as apply.")
			f.Image("Cover").Dir("posts").MaxSize(2 << 20)
			// A JSON array of storage paths in one column: files the panel
			// hands over and never queries. Anything needing a caption or an
			// order wants HasMany instead.
			f.Files("Attachments").Dir("posts/docs").
				Accept(".pdf,.txt").MaxSize(2 << 20).MaxFiles(4).
				Help("Up to four documents.")
			f.Saving(func(c *steward.Context, p *models.Post) error {
				if p.Status == "published" && p.PublishedAt == nil {
					now := time.Now()
					p.PublishedAt = &now
				}
				return nil
			})
		})

	// A page composed in Go rather than in a template: rows, columns, and the
	// widgets that sit in them.
	posts.Page("GET", "report", func(c *steward.Context) error {
		gdb := c.Admin.DB().WithContext(c.Ctx())
		var published, drafts int64
		gdb.Model(&models.Post{}).Where("status = ?", "published").Count(&published)
		gdb.Model(&models.Post{}).Where("status = ?", "draft").Count(&drafts)

		var recent []models.Post
		gdb.Order("id desc").Limit(5).Find(&recent)
		rows := make([][]any, 0, len(recent))
		for _, p := range recent {
			rows = append(rows, []any{p.Title, p.Status})
		}

		return c.Layout("Post report",
			steward.Row(
				steward.Col(8, steward.Card("Published over time", steward.Chart(&steward.ChartData{
					Type:   steward.ChartBar,
					Labels: []string{"Published", "Drafts"},
					Series: []steward.ChartSeries{{
						Label:  "Posts",
						Values: []float64{float64(published), float64(drafts)},
					}},
				}))),
				steward.Col(4,
					steward.Metric("Published", published, "live on the site").
						Icon("newspaper").Color(steward.BadgeGreen),
					steward.Metric("Drafts", drafts).
						Icon("file-text").Color(steward.BadgeOrange),
				),
			),
			steward.Row(
				steward.Col(12, steward.Card("Most recent",
					steward.Table([]string{"Title", "Status"}, rows))),
			),
		)
	})

	posts.Detail(func(d *steward.Detail[models.Post]) {
		d.Field("ID")
		d.Field("Title")
		d.Field("Cover").Image(480, 0)
		d.Field("Status").Badge(map[any]steward.BadgeColor{"draft": steward.BadgeSecondary, "published": steward.BadgeGreen})
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
		Grid(func(g *steward.Grid[models.Author]) {
			g.Column("ID").Sortable()
			g.Column("Name").Sortable()
			g.Column("Email")
			// Posts uses buttons, so this grid covers the other presentation.
			g.ActionStyle(steward.GridActionsMenu)
			// And the other filter layout, so both are exercised by the visual
			// checks: a control open inside the drawer has to take Escape
			// without the drawer going with it.
			g.FilterLayout(steward.FiltersDrawer)
			g.Filter(func(f *steward.Filters[models.Author]) {
				f.Like("Name")
				f.Equal("Email").Select(steward.Options{
					"ada@example.com":   "Ada",
					"grace@example.com": "Grace",
				})
				f.DateRange("CreatedAt", "Joined")
			})
		}).
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
