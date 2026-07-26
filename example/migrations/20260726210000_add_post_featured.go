package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "20260726210000_add_post_featured",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn("posts", "featured") {
				return nil
			}
			return tx.Exec("ALTER TABLE posts ADD COLUMN featured boolean NOT NULL DEFAULT false").Error
		},
		Down: func(tx *gorm.DB) error {
			return tx.Exec("ALTER TABLE posts DROP COLUMN featured").Error
		},
	})
}
