package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"

	steward "github.com/imfiqhan/steward"
	"gorm.io/gorm"
)

// The docs show one example of every field kind and every detail renderer. A
// snippet nobody compiles goes stale without anyone noticing, so this calls
// each of them the way the pages spell it and boots the panel that results.
//
// Adding a kind means adding it here and on the page it belongs to:
// steward-site/content/docs/form.md and detail.md.

type docsCategory struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type docsRow struct {
	ID          uint `gorm:"primaryKey"`
	Title       string
	Summary     string
	Email       string
	Password    string
	Website     string
	Order       int
	Rating      float64
	Price       float64
	Status      int16
	Visibility  string
	Featured    bool
	PublishedOn time.Time
	RunsFrom    time.Time
	RunsTo      time.Time
	PostDate    time.Time
	OpensAt     string
	Cover       string
	Attachment  string
	Photos      string
	Attachments string
	Body        string
	Content     string
	Payload     string
	Notes       string
	Size        int64
	Icon        string
	Brand       string
	Slug        string
	CategoryID  *uint
	Category    *docsCategory
	CreatedAt   time.Time
}

func TestDocumentedFieldKindsAllWork(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&docsCategory{}, &docsRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&docsCategory{Name: "Politics"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&docsRow{Title: "A headline", Size: 2411724, Status: 1}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("docs-field-kinds-test-secret"),
		AuthExcept: []string{"/docs_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := steward.Register[docsRow](app)

	res.Form(func(f *steward.Form[docsRow]) {
		// Text
		f.Text("Title").Rules("required|max:255").Placeholder("Headline")
		f.Textarea("Summary").Help("Shown on the listing page.")
		f.Email("Email").Rules("required|email")
		f.Password("Password").Rules("min:12").OnlyOnCreate()
		f.URL("Website").Placeholder("https://example.com")

		// Numbers
		f.Number("Order").Min(0).Max(999).Default(0)
		f.Decimal("Rating").Rules("numeric|gte:0|lte:5")
		f.Currency("Price").Symbol("Rp").Rules("required|numeric")

		// Choice
		f.Select("Status").Options(steward.Options{"0": "Draft", "1": "Published"})
		f.Radio("Visibility").Options(steward.Options{"public": "Public", "private": "Private"})
		f.Switch("Featured").Help("Pinned to the top of the list.")

		// Dates and times
		f.Date("PublishedOn")
		f.Datetime("PostDate").Rules("required")
		f.Time("OpensAt")
		f.DateRange("RunsFrom", "RunsTo", "Runs").Datetime()

		// Uploads
		f.Image("Cover").Dir("covers").MaxSize(2 << 20).Accept("image/*")
		f.File("Attachment").Dir("docs").Accept("application/pdf")
		f.Images("Photos").Dir("galleries").MaxFiles(20)
		f.Files("Attachments").Dir("docs")

		// Long text
		f.Markdown("Body")
		f.Richtext("Content")

		// Relations
		f.BelongsTo("CategoryID", "Category", "Name")

		// Pickers and colour
		f.Icon("Icon")
		f.Color("Brand").Default("#2563eb")

		// Not an input
		f.Hidden("Slug")
		f.Display("CreatedAt", "Created")
		f.Divider()
		f.Fieldset("Publishing", func(f *steward.Form[docsRow]) {
			f.Switch("Featured").Span(6)
			f.Datetime("PostDate").Span(6)
		})
	})

	res.Detail(func(d *steward.Detail[docsRow]) {
		d.Field("Status").Using(map[any]string{0: "Draft", 1: "Published"})
		d.Field("Status").Badge(map[any]steward.BadgeColor{
			0: steward.BadgeSecondary, 1: steward.BadgeGreen,
		})
		d.Field("Featured").Bool()
		d.Field("Featured").Bool("Ya", "Tidak")
		d.Field("Size").Filesize()

		d.Field("Body").Markdown()
		d.Field("Content").HTML()
		d.Field("Payload").JSON()
		d.Field("Notes").Preformatted()

		d.Field("Cover").Image(480, 0)
		d.Field("Attachment").Link()
		d.Field("Icon").Image(96, 96).Disk("local")

		d.FieldFunc("tags", "Tags", func(r *docsRow) template.HTML {
			return template.HTML(strings.Join([]string{"one", "two"}, ", "))
		})
		d.Field("PostDate").As(func(v any, r *docsRow) template.HTML {
			return template.HTML(r.PostDate.Format("2 January 2006"))
		})
		d.Field("Title").Copyable()
		d.Field("Notes").Block()
	})

	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	// Verify resolves every path and checks colours, disks and label counts, so
	// a snippet that names something that no longer exists fails here.
	if err := app.Verify(); err != nil {
		t.Fatalf("the documented calls should verify: %v", err)
	}
}

// The layout page shows one example of every widget and both of the metric's
// settings. Same reasoning as above: a snippet nobody compiles goes stale, and
// this one already caught a chart type that does not exist.
func TestDocumentedWidgetsAllWork(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&docsRow{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("docs-widgets-test-secret"),
		AuthExcept: []string{"/docs_rows*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	headers := []string{"Title", "Author"}
	rows := [][]any{{"A headline", "Ada"}}

	steward.Register[docsRow](app).Page("GET", "widgets", func(c *steward.Context) error {
		return c.Layout("Widgets",
			steward.Row(
				steward.Col(6, steward.Card("Latest posts", steward.Table(headers, rows))),
				steward.Col(6, steward.Card("", steward.Text("A card with no heading."))),
			),
			steward.Card("This week",
				steward.Metric("Published", 12),
				steward.Divider(),
				steward.Table(headers, rows),
			),
			steward.Metric("Published", 1752),
			steward.Metric("Published", 1752, "live on the site"),
			steward.Metric("Published", 1752, "live on the site").
				Icon("newspaper").
				Color(steward.BadgeGreen),
			steward.Chart(&steward.ChartData{
				Type:   steward.ChartLine,
				Labels: []string{"Jan", "Feb", "Mar"},
				Series: []steward.ChartSeries{{Label: "Posts", Values: []float64{12, 19, 7}}},
			}),
			steward.Table([]string{"Title", "Status"}, [][]any{
				{"A headline", template.HTML(`<span class="badge">Published</span>`)},
			}),
			steward.Heading("How this is counted"),
			steward.Text("Drafts are excluded, and so is anything in the bin."),
			steward.Divider(),
			steward.Markup(template.HTML(`<ol class="grid gap-3"><li class="text-sm">an event</li></ol>`)),
		)
	})

	// And the dashboard spellings the page shows.
	app.Dashboard(func(d *steward.Dashboard) {
		d.Metric("Users", func(c *steward.Context) (any, error) { return 3, nil }).
			Icon("users").Color(steward.BadgeBlue).Lazy()
		d.Row(
			steward.Col(8, d.Chart("Trend", func(c *steward.Context) (*steward.ChartData, error) {
				return &steward.ChartData{
					Type: steward.ChartBar, Labels: []string{"a"},
					Series: []steward.ChartSeries{{Label: "n", Values: []float64{1}}},
				}, nil
			}).Lazy()),
			steward.Col(4, d.Metric("This month", func(c *steward.Context) (any, error) { return 9, nil })),
		)
	})

	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatalf("the documented calls should verify: %v", err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)

	html := fetchOK(t, srv.URL+"/admin/docs_rows/widgets")
	for _, want := range []string{
		"Latest posts", "A card with no heading.", "1752", "live on the site",
		`data-tone="green"`, "steward-metric-icon", "data-steward-chart",
		"How this is counted", "an event",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page should contain %q", want)
		}
	}
}

// Every Config field belongs in the configuration reference. Reflection over
// the struct is what keeps that true: a field added to Config fails here until
// it is listed, and a field listed here that no longer exists fails too.
//
// The page is steward-site/content/docs/configuration.md.
func TestEveryConfigFieldIsDocumented(t *testing.T) {
	documented := map[string]bool{
		"DB": true, "SecretKey": true,
		"Prefix": true, "Brand": true, "CurrencySymbol": true, "GridActions": true,
		"FilterLayout": true,
		"UploadDir":    true, "Storage": true, "Disks": true, "DefaultDisk": true,
		"ExportDisk":    true,
		"PublicUploads": true, "SignedURLTTL": true,
		"TablePrefix": true, "DisableAutoMigrate": true,
		"DisableNotifications": true,
		"BackgroundExportRows": true, "DisableExportWorker": true,
		"Require2FA": true, "LoginCheck": true, "AuthExcept": true,
		"EnableTokenAuth": true, "TokenTTL": true,
		"TokenRateLimit": true, "TokenRateWindow": true,
		"Cache": true, "Searcher": true, "Mailer": true, "Logger": true,
		"Dev": true, "TemplatesFS": true, "AssetsFS": true,
	}

	typ := reflect.TypeOf(steward.Config{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported: not part of the surface
		}
		seen[f.Name] = true
		if !documented[f.Name] {
			t.Errorf("Config.%s is not in the configuration reference", f.Name)
		}
	}
	for name := range documented {
		if !seen[name] {
			t.Errorf("the reference documents Config.%s, which no longer exists", name)
		}
	}
}

// The configuration page shows where a panel's values can come from. The
// point is that Config is an ordinary struct, so this compiles the shapes:
// environment variables, and a fetch that can fail.
func TestDocumentedConfigSourcesCompile(t *testing.T) {
	// Environment, as a generated project starts.
	fromEnv := func(db *gorm.DB) (*steward.Admin, error) {
		return steward.New(steward.Config{
			// These exercise a prefixed mount; the default is the root.
			Prefix:    "/admin",
			DB:        db,
			SecretKey: []byte(envOr("STEWARD_SECRET", "docs-test-secret-key-0000")),
			Dev:       os.Getenv("STEWARD_DEV") != "",
		})
	}

	// A secrets manager: Build returns an error, so it is a fine place to fetch.
	fromSecrets := func(ctx context.Context, db *gorm.DB) (*steward.Admin, error) {
		key, err := fakeSecret(ctx, "prod/panel/session-key")
		if err != nil {
			return nil, fmt.Errorf("reading the session key: %w", err)
		}
		return steward.New(steward.Config{DB: db, SecretKey: []byte(key)})
	}

	db := testDB(t)
	for name, build := range map[string]func() (*steward.Admin, error){
		"environment": func() (*steward.Admin, error) { return fromEnv(db) },
		"secrets":     func() (*steward.Admin, error) { return fromSecrets(context.Background(), db) },
	} {
		app, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := app.Build(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// fakeSecret stands in for whatever store the reader uses.
func fakeSecret(_ context.Context, _ string) (string, error) {
	return "docs-test-secret-key-0000", nil
}

// The settings store is what the configuration page offers for values that
// change without a deploy: a slug reads as "" when absent, a write is visible
// to the next read, and the panel's own page edits the same rows.
func TestDocumentedSettingsStore(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{
		// These exercise a prefixed mount; the default is the root.
		Prefix: "/admin",
		DB:     db, SecretKey: []byte("docs-settings-test-secret-key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	got, err := app.Setting(ctx, "login-notice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("an unset slug should read as empty, got %q", got)
	}

	if err := app.SetSetting(ctx, "login-notice", "Maintenance on Sunday."); err != nil {
		t.Fatal(err)
	}
	got, err = app.Setting(ctx, "login-notice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Maintenance on Sunday." {
		t.Errorf("a written setting should read back, got %q", got)
	}

	// A typo is not an error, which is the trap the page warns about.
	if v, err := app.Setting(ctx, "login-notce"); err != nil || v != "" {
		t.Errorf("a misspelled slug should read as empty, got %q, %v", v, err)
	}
}

// The notifications page shows every call it documents. Compiling them here
// keeps the page from drifting past the API, which is how a snippet goes wrong
// without anyone noticing.
//
// steward-site/content/docs/notifications.md
func TestDocumentedNotificationCalls(t *testing.T) {
	app, _ := newNotifyApp(t)
	ctx := context.Background()

	type submitted struct {
		PostID uint   `json:"post_id"`
		Author string `json:"author"`
	}

	n := steward.Notification{
		Title: "Article awaiting review",
		Body:  `"Pemprov Jatim gelar sosialisasi" was submitted by Editor.`,
		URL:   "/admin/posts/4182",
		Icon:  "file-text",
		Type:  "post.submitted",
	}.WithPayload(submitted{PostID: 4182, Author: "editor"})

	if err := app.Notify(ctx, 1, n); err != nil {
		t.Fatal(err)
	}
	if err := app.NotifyUsers(ctx, []uint{1}, n); err != nil {
		t.Fatal(err)
	}
	if err := app.NotifyRole(ctx, n, "administrator"); err != nil {
		t.Fatal(err)
	}

	items, err := app.Notifications(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("nothing to read back")
	}
	var got submitted
	if err := items[0].Payload(&got); err != nil {
		t.Fatal(err)
	}
	if got.PostID != 4182 {
		t.Fatalf("payload came back as %+v", got)
	}

	if _, err := app.UnreadNotifications(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := app.MarkNotificationRead(ctx, 1, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := app.MarkNotificationsRead(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := app.DeleteNotification(ctx, 1, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.PruneNotifications(ctx, 90*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	// An icon the page names but the sprite lacks renders as a gap in the row.
	have := map[string]bool{}
	for _, name := range app.Icons() {
		have[name] = true
	}
	for _, name := range []string{"file-text", "bell", "check", "calendar", "database"} {
		if !have[name] {
			t.Errorf("the page names icon %q, which is not in the sprite", name)
		}
	}
}

// The page sends from a Saved hook, which lives on the form rather than the
// resource. Registering it here is what proves the shape.
//
// steward-site/content/docs/notifications.md
func TestDocumentedNotificationHook(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:notifyhook?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&hookPost{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("notification-hook-test-secret"),
	})
	if err != nil {
		t.Fatal(err)
	}

	const statusPending = 1
	steward.Register[hookPost](app).
		Form(func(f *steward.Form[hookPost]) {
			f.Text("Title")
			f.Saved(func(c *steward.Context, p *hookPost, created bool) error {
				if p.Status != statusPending {
					return nil
				}
				return c.Admin.NotifyRole(c.Ctx(), steward.Notification{
					Title: "Article awaiting review",
					Body:  p.Title,
					URL:   c.URL("hook_posts", fmt.Sprint(p.ID)),
					Icon:  "file-text",
					Type:  "post.submitted",
				}, "editor")
			})
		})

	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
}

type hookPost struct {
	ID     uint `gorm:"primaryKey"`
	Title  string
	Status int16
}

// The grid page shows how to move queued exports into a worker and how to trim
// old ones. Both are compiled here so the page cannot drift past the API.
//
// steward-site/content/docs/grid.md#background-export
func TestDocumentedExportJobCalls(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:docsexport?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&hookPost{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		DB:        db,
		SecretKey: []byte("documented-export-test-secret"),
		// The two knobs the page names, spelled the way it spells them.
		BackgroundExportRows: 50000,
		DisableExportWorker:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[hookPost](app)
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}

	// The worker snippet.
	jobs := func(a *steward.Admin, s steward.Scheduler) error {
		return s.Add("@every 30s", "exports", func(ctx context.Context) error {
			_, err := a.RunPendingExports(ctx)
			return err
		})
	}
	if err := jobs(app, steward.NewIntervalScheduler()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := app.RunPendingExports(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.PruneExports(ctx, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exports(ctx, 1, 0); err != nil {
		t.Fatal(err)
	}
}

// The grid page shows a two-disk arrangement that keeps exports out of the
// directory a web server reads. Compiled here so the spelling stays real.
//
// steward-site/content/docs/grid.md#background-export
func TestDocumentedExportDiskArrangement(t *testing.T) {
	app, err := steward.New(steward.Config{
		DB:        testDB(t),
		SecretKey: []byte("documented-export-disk-secret"),
		Disks: map[string]steward.Disk{
			"media":   {Storage: &steward.LocalStorage{Dir: t.TempDir()}},
			"exports": {Storage: &steward.LocalStorage{Dir: t.TempDir()}},
		},
		DefaultDisk: "media",
		ExportDisk:  "exports",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Fatal(err)
	}
}
