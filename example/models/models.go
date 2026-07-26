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
	ID          uint   `gorm:"primaryKey"`
	Title       string `gorm:"size:255"`
	Body        string `gorm:"type:text"`
	Status      string `gorm:"size:20;default:draft"` // draft | published
	Featured    bool   `gorm:"default:false"`
	Cover       string `gorm:"size:255"`
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
	CreatedAt time.Time
	UpdatedAt time.Time
}
