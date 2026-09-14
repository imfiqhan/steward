package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/example/models"
	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "20260914120000_widen_post_cover",
		Up: func(tx *gorm.DB) error {
			// SQLite does not enforce a column's declared width, so there is
			// nothing there to widen and its AlterColumn rebuilds the table to
			// achieve nothing.
			if tx.Name() == "sqlite" {
				return nil
			}
			return tx.Migrator().AlterColumn(&models.Post{}, "Cover")
		},
		Down: func(tx *gorm.DB) error { return nil },
	})
}
