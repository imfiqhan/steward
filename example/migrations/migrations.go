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
				// A cover as a data URI: an Image column resolves a stored path
				// through a disk, and this example ships no files, but the
				// column and the preview it opens are the same either way.
				{Title: "Hello, Steward", Body: "First post managed by the Steward admin panel.", Status: "published", AuthorID: author.ID, Cover: sampleCover},
				{Title: "Drafting in the open", Body: "This one is still a draft.", Status: "draft", AuthorID: author.ID, Cover: sampleCover},
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

// sampleCover is a 4x3 SVG, small enough to inline and large enough to see
// scaled up in the preview.
const sampleCover = "data:image/svg+xml;utf8," +
	"%3Csvg%20xmlns%3D'http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg'%20viewBox%3D'0%200%20400%20300'%3E" +
	"%3Crect%20width%3D'400'%20height%3D'300'%20fill%3D'%23dbeafe'%2F%3E" +
	"%3Ccircle%20cx%3D'200'%20cy%3D'150'%20r%3D'90'%20fill%3D'%233b82f6'%2F%3E%3C%2Fsvg%3E"
