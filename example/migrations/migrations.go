// Package migrations lists the example app's schema migrations, applied by
// the runner under the "app" source.
package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/example/models"
	"github.com/imfiqhan/steward/migrate"
)

// All is the ordered migration list registered with the runner.
var All = []migrate.Migration{
	{
		Name: "20260726000001_create_blog_tables",
		Up: func(tx *gorm.DB) error {
			return tx.Migrator().AutoMigrate(&models.Author{}, &models.Post{})
		},
		Down: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable(&models.Post{}); err != nil {
				return err
			}
			return tx.Migrator().DropTable(&models.Author{})
		},
	},
	{
		Name: "20260726000002_seed_blog",
		Up: func(tx *gorm.DB) error {
			author := models.Author{Name: "Ada Lovelace", Email: "ada@example.com"}
			if err := tx.Create(&author).Error; err != nil {
				return err
			}
			posts := []models.Post{
				{Title: "Hello, Steward", Body: "First post managed by the Steward admin panel.", Status: "published", AuthorID: author.ID},
				{Title: "Drafting in the open", Body: "This one is still a draft.", Status: "draft", AuthorID: author.ID},
			}
			return tx.Create(&posts).Error
		},
		Down: func(tx *gorm.DB) error { return nil },
	},
	{
		// A second author, so the Author picker has something to choose
		// between. With one row a combobox cannot be told from a label.
		Name: "20260814000001_seed_second_author",
		Up: func(tx *gorm.DB) error {
			return tx.Create(&models.Author{
				Name: "Grace Hopper", Email: "grace@example.com",
			}).Error
		},
		Down: func(tx *gorm.DB) error { return nil },
	},
}
