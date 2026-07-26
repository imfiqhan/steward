package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward-example/models"
	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "20260727010000_create_comments",
		Up: func(tx *gorm.DB) error {
			return tx.Migrator().AutoMigrate(&models.Comment{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.Comment{})
		},
	})
}
