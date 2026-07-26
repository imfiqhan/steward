// Package migrations holds Steward's embedded framework migrations, applied
// under the "core" source by Config.AutoMigrate or `migrate up`.
package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/migrate"
	"golang.org/x/crypto/bcrypt"
)

// Tables abstracts the concrete model types so this package never imports the
// root package (which imports us). The root package passes its models in.
type Tables struct {
	// Models carries one zero-value pointer per framework table, in
	// creation order; DropTable runs in reverse.
	Models []any
	// SeedFn inserts default rows (admin user, administrator role, menu).
	SeedFn func(tx *gorm.DB, passwordHash string) error
}

// Core builds the embedded migration list for the given table set.
func Core(t Tables) []migrate.Migration {
	return []migrate.Migration{
		{
			Name: "0001_create_admin_tables",
			Up: func(tx *gorm.DB) error {
				return tx.Migrator().AutoMigrate(t.Models...)
			},
			Down: func(tx *gorm.DB) error {
				for i := len(t.Models) - 1; i >= 0; i-- {
					if err := tx.Migrator().DropTable(t.Models[i]); err != nil {
						return err
					}
				}
				return nil
			},
		},
		{
			Name: "0002_seed_defaults",
			Up: func(tx *gorm.DB) error {
				hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
				if err != nil {
					return err
				}
				return t.SeedFn(tx, string(hash))
			},
			// Seeded rows are user data once created; rolling back the seed
			// deliberately does nothing destructive.
			Down: func(tx *gorm.DB) error { return nil },
		},
	}
}
