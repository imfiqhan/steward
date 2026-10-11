package main

import (
	"strings"
	"testing"

	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

// frameworkTables lists the tables whose names start with one of the given
// prefixes.
func frameworkTables(t *testing.T, db *gorm.DB, prefixes ...string) []string {
	t.Helper()
	all, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range all {
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				out = append(out, name)
			}
		}
	}
	return out
}

// The table prefix is process-global, so each test puts the default back for
// the tests that run after it.
func restorePrefix(t *testing.T, db *gorm.DB) {
	t.Cleanup(func() { _ = steward.SetupModels(db, steward.DefaultTablePrefix) })
}

func TestDefaultTablePrefixIsSteward(t *testing.T) {
	db := testDB(t)
	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("prefix-test-secret-key")})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}

	if got := frameworkTables(t, db, "admin_"); len(got) > 0 {
		t.Fatalf("fresh install created admin_ tables: %v", got)
	}
	for _, want := range []string{"steward_users", "steward_role_users", "steward_role_permissions", "steward_migrations"} {
		if !db.Migrator().HasTable(want) {
			t.Errorf("missing table %s", want)
		}
	}

	var user steward.User
	if err := db.Preload("Roles").Take(&user, 1).Error; err != nil {
		t.Fatalf("loading seeded user with roles: %v", err)
	}
	if !user.IsAdministrator() {
		t.Fatal("seeded user lost the administrator role")
	}
}

func TestLegacyAdminPrefixKeepsExistingInstall(t *testing.T) {
	db := testDB(t)
	restorePrefix(t, db)

	cfg := steward.Config{DB: db, SecretKey: []byte("prefix-test-secret-key"), TablePrefix: "admin_"}
	first, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, first); err != nil {
		t.Fatal(err)
	}
	if got := frameworkTables(t, db, "steward_"); len(got) > 0 {
		t.Fatalf("admin_ install created steward_ tables: %v", got)
	}

	// A second boot finds every framework migration already recorded.
	second, err := steward.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := second.MigrationRunner(nil).Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pending {
		if !s.Applied {
			t.Errorf("migration %s would run again", s.Name)
		}
	}

	var user steward.AdminUser //nolint:staticcheck // the deprecated alias must keep compiling
	if err := db.Preload("Roles").Take(&user, 1).Error; err != nil {
		t.Fatalf("loading seeded user with roles: %v", err)
	}
	if !user.IsAdministrator() {
		t.Fatal("seeded user lost the administrator role")
	}
}

func TestSetupModelsWithoutPanel(t *testing.T) {
	db := testDB(t)
	restorePrefix(t, db)

	app, err := steward.New(steward.Config{DB: db, SecretKey: []byte("prefix-test-secret-key"), TablePrefix: "admin_"})
	if err != nil {
		t.Fatal(err)
	}
	if err := buildPanel(t, app); err != nil {
		t.Fatal(err)
	}

	// A separate process sharing the database, with no Panel of its own.
	other, err := gorm.Open(db.Dialector, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := steward.SetupModels(other, "admin_"); err != nil {
		t.Fatal(err)
	}
	var user steward.User
	if err := other.Preload("Roles").Take(&user, 1).Error; err != nil {
		t.Fatalf("loading user with roles through SetupModels: %v", err)
	}
	if !user.IsAdministrator() {
		t.Fatal("roles did not resolve through the prefixed join table")
	}
}
