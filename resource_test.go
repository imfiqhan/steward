package steward

import (
	"reflect"
	"testing"
	"time"
)

func TestSplitCamel(t *testing.T) {
	cases := map[string]string{
		"BlogPost":    "Blog Post",
		"ID":          "ID",
		"AuthorID":    "Author ID",
		"HTTPServer":  "HTTP Server",
		"Title":       "Title",
		"PublishedAt": "Published At",
		"a":           "a",
		"":            "",
	}
	for in, want := range cases {
		if got := splitCamel(in); got != want {
			t.Errorf("splitCamel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToSnake(t *testing.T) {
	cases := map[string]string{
		"BlogPost": "blog_post",
		"Post":     "post",
		"Author":   "author",
	}
	for in, want := range cases {
		if got := toSnake(in); got != want {
			t.Errorf("toSnake(%q) = %q, want %q", in, got, want)
		}
	}
}

type ftAuthor struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type ftPost struct {
	ID          uint `gorm:"primaryKey"`
	Title       string
	PublishedAt *time.Time
	AuthorID    uint
	Author      ftAuthor
}

func TestFieldTable(t *testing.T) {
	ft, err := newFieldTable(reflect.TypeFor[ftPost](), nil)
	if err != nil {
		t.Fatal(err)
	}
	if ft.pk == nil || ft.pk.Path != "ID" {
		t.Fatalf("pk = %+v, want ID", ft.pk)
	}
	if _, err := ft.lookup("Title"); err != nil {
		t.Errorf("lookup(Title): %v", err)
	}
	rel, err := ft.lookup("Author.Name")
	if err != nil {
		t.Fatalf("lookup(Author.Name): %v", err)
	}
	if rel.Relation != "Author" {
		t.Errorf("relation = %q, want Author", rel.Relation)
	}
	if _, err := ft.lookup("Nope"); err == nil {
		t.Error("lookup(Nope) should fail")
	}

	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := ftPost{ID: 7, Title: "hi", PublishedAt: &when, Author: ftAuthor{Name: "Ada"}}
	if v, ok := ft.byPath["Title"].value(reflect.ValueOf(&p)); !ok || v != "hi" {
		t.Errorf("value(Title) = %v %v", v, ok)
	}
	if v, ok := rel.value(reflect.ValueOf(&p)); !ok || v != "Ada" {
		t.Errorf("value(Author.Name) = %v %v", v, ok)
	}
	if v, ok := ft.byPath["PublishedAt"].value(reflect.ValueOf(&p)); !ok || !v.(time.Time).Equal(when) {
		t.Errorf("value(PublishedAt) = %v %v", v, ok)
	}
	var empty ftPost
	if _, ok := ft.byPath["PublishedAt"].value(reflect.ValueOf(&empty)); ok {
		t.Error("nil *time.Time should report !ok")
	}
}
