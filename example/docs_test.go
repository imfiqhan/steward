package main

import (
	"html/template"
	"strings"
	"testing"
	"time"

	steward "github.com/imfiqhan/steward"
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
		DB: db, SecretKey: []byte("docs-field-kinds-test-secret"),
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
