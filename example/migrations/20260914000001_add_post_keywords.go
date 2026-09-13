package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "20260914000001_add_post_keywords",
		Up: func(tx *gorm.DB) error {
			if tx.Migrator().HasColumn("posts", "keywords") {
				return nil
			}
			if err := tx.Exec("ALTER TABLE posts ADD COLUMN keywords text").Error; err != nil {
				return err
			}
			return tx.Exec(`UPDATE posts SET keywords = ? WHERE status = ?`,
				`["launch","steward"]`, "published").Error
		},
		Down: func(tx *gorm.DB) error {
			return tx.Exec("ALTER TABLE posts DROP COLUMN keywords").Error
		},
	})
}
