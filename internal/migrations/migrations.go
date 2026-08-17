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
	// TokenModel is created by 0003 rather than listed in Models, so that
	// databases already past 0001 pick the table up too.
	TokenModel any
	// NotificationModel is created by 0005, apart from Models for the same
	// reason as TokenModel.
	NotificationModel any
	// UserModel is the account table, altered by 0004 to add the two-factor
	// columns. Same reasoning as TokenModel: installations already past 0001
	// need the change applied, not just present in the model.
	UserModel any
	// AddTwoFactorColumns applies 0004 against UserModel. The root package owns
	// the column list because this package must not import it.
	AddTwoFactorColumns func(tx *gorm.DB, model any) error
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
		{
			Name: "0003_create_admin_tokens",
			Up: func(tx *gorm.DB) error {
				return tx.Migrator().AutoMigrate(t.TokenModel)
			},
			Down: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(t.TokenModel)
			},
		},
		{
			Name: "0004_add_two_factor_columns",
			Up: func(tx *gorm.DB) error {
				return t.AddTwoFactorColumns(tx, t.UserModel)
			},
			// Rolling back drops enrolments, which would lock out anyone
			// relying on them; the columns are inert when the feature is
			// unused, so leaving them is the safer no-op.
			Down: func(tx *gorm.DB) error { return nil },
		},
		{
			Name: "0005_create_admin_notifications",
			Up: func(tx *gorm.DB) error {
				return tx.Migrator().AutoMigrate(t.NotificationModel)
			},
			Down: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(t.NotificationModel)
			},
		},
	}
}
