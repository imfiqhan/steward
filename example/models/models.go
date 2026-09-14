// Package models holds the example blog's domain types.
package models

import "time"

// Author writes posts.
type Author struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:120"`
	Email     string `gorm:"size:255;uniqueIndex"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Post is the example resource showcasing grid, form, and detail features.
type Post struct {
	ID       uint   `gorm:"primaryKey"`
	Title    string `gorm:"size:255"`
	Body     string `gorm:"type:text"`
	Status   string `gorm:"size:20;default:draft"` // draft | published
	Featured bool   `gorm:"default:false"`
	// A cover is a stored path or a URL, and a data URI is a URL. 255
	// characters is enough for the first and not for the last, which SQLite
	// does not mind and PostgreSQL refuses outright.
	Cover string `gorm:"type:text"`
	// A Files field's column: a JSON array of storage paths, so text rather
	// than a sized string.
	Attachments string `gorm:"type:text"`
	// A Tags field's column, holding the same shape: a JSON array of values.
	Keywords    string `gorm:"type:text"`
	PublishedAt *time.Time
	AuthorID    uint
	Author      Author
	Comments    []Comment
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Comment demonstrates hasMany nested forms on Post.
type Comment struct {
	ID        uint   `gorm:"primaryKey"`
	PostID    uint   `gorm:"index"`
	Name      string `gorm:"size:120"`
	Body      string `gorm:"size:500"`
	Kind      string `gorm:"size:20"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
