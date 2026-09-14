package main

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/example/migrations"
)

// probeRow is migrated without Nickname, the way a model gains a field and the
// migration to match it does not get written.
type probeRow struct {
	ID       uint `gorm:"primaryKey"`
	Title    string
	Nickname string
}

type probeRowMigrated struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func (probeRowMigrated) TableName() string { return "probe_rows" }

func probeVerify(t *testing.T, migrate func(*gorm.DB), configure func(*steward.Resource[probeRow])) string {
	t.Helper()
	db := testDB(t)
	migrate(db)
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("probe-test-secret-key-0000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	configure(steward.Register[probeRow](app))
	if err := app.Verify(); err != nil {
		return err.Error()
	}
	return ""
}

// The column resolves against the struct, so every check that reads the struct
// passes. Only the query knows it is not there.
func TestVerifyCatchesAColumnNoMigrationAdded(t *testing.T) {
	got := probeVerify(t,
		func(db *gorm.DB) {
			if err := db.AutoMigrate(&probeRowMigrated{}); err != nil {
				t.Fatal(err)
			}
		},
		func(r *steward.Resource[probeRow]) {
			r.Grid(func(g *steward.Grid[probeRow]) {
				g.Column("Title")
				g.Column("Nickname")
			})
		})
	if got == "" {
		t.Fatal("a column that exists on the model and not in the database passed Verify")
	}
	for _, want := range []string{"probe_rows", "the database refused this query", "nickname"} {
		if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Errorf("the report is missing %q: %s", want, got)
		}
	}
}

// The whole schema present is the case that has to stay quiet, or the probe is
// noise nobody will leave switched on.
func TestVerifyIsQuietWhenTheSchemaMatches(t *testing.T) {
	got := probeVerify(t,
		func(db *gorm.DB) {
			if err := db.AutoMigrate(&probeRow{}); err != nil {
				t.Fatal(err)
			}
		},
		func(r *steward.Resource[probeRow]) {
			r.Grid(func(g *steward.Grid[probeRow]) {
				g.Column("Title").Sortable()
				g.Column("Nickname")
				g.QuickSearch("Title", "Nickname")
				g.Filter(func(f *steward.Filters[probeRow]) {
					f.Like("Title")
					f.Equal("Nickname")
				})
			})
		})
	if got != "" {
		t.Errorf("a panel whose schema matches was reported: %s", got)
	}
}

// A table that is not there yet is a migration that has not run, which is not
// the panel's mistake to report.
func TestVerifySaysNothingAboutAMissingTable(t *testing.T) {
	got := probeVerify(t, func(*gorm.DB) {}, func(r *steward.Resource[probeRow]) {
		r.Grid(func(g *steward.Grid[probeRow]) { g.Column("Title") })
	})
	if got != "" {
		t.Errorf("an unmigrated table was reported as a configuration error: %s", got)
	}
}

// The probe costs a round trip per declaration, so it has to be possible to
// turn off.
func TestQueryProbeCanBeSwitchedOff(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&probeRowMigrated{}); err != nil {
		t.Fatal(err)
	}
	app, err := steward.New(steward.Config{
		Prefix: "/admin", DB: db, SecretKey: []byte("probe-test-secret-key-0000"),
		DisableQueryProbe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	steward.Register[probeRow](app).Grid(func(g *steward.Grid[probeRow]) {
		g.Column("Title")
		g.Column("Nickname")
	})
	if err := app.Verify(); err != nil {
		t.Errorf("the probe ran with DisableQueryProbe set: %v", err)
	}
}

// The example panel is the one with real surface — relations, a virtual
// multi-select, a repeater, filters across a join, a tree — so it is the one
// worth running the probe against. Nothing verified it before, which is how a
// declaration that the database refuses could have reached a release.
func TestTheExamplePanelVerifies(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{
		DB: db, SecretKey: []byte("example-verify-secret-0000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	registerResources(app)
	if _, err := app.MigrationRunner(migrations.All).Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Verify(); err != nil {
		t.Errorf("the example panel does not verify:\n%v", err)
	}
}
