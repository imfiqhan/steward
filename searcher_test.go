package steward

import (
	"context"
	"testing"
)

func TestMemorySearcher(t *testing.T) {
	ctx := context.Background()
	s := NewMemorySearcher()

	docs := []SearchDoc{
		{ID: "1", Type: "post", Fields: map[string]string{"title": "Hello Steward", "body": "admin panels in Go"}},
		{ID: "2", Type: "post", Fields: map[string]string{"title": "Drafting drafts", "body": "hello hello world"}},
		{ID: "3", Type: "page", Fields: map[string]string{"title": "Contact"}},
	}
	for _, d := range docs {
		if err := s.Index(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	hits, _ := s.Query(ctx, "", "hello", 10)
	if len(hits) != 2 || hits[0].ID != "2" {
		t.Fatalf("hello: want doc 2 first (freq 2), got %+v", hits)
	}
	hits, _ = s.Query(ctx, "", "hello steward", 10)
	if len(hits) != 1 || hits[0].ID != "1" {
		t.Fatalf("AND semantics: want only doc 1, got %+v", hits)
	}
	if hits, _ = s.Query(ctx, "", "missing", 10); len(hits) != 0 {
		t.Fatalf("missing term should return nothing, got %+v", hits)
	}
	if hits, _ = s.Query(ctx, "", "", 10); hits != nil {
		t.Fatalf("empty query returns nil, got %+v", hits)
	}

	// Reindex replaces postings.
	_ = s.Index(ctx, SearchDoc{ID: "2", Type: "post", Fields: map[string]string{"title": "Renamed"}})
	if hits, _ = s.Query(ctx, "", "hello", 10); len(hits) != 1 || hits[0].ID != "1" {
		t.Fatalf("after reindex want only doc 1, got %+v", hits)
	}

	// Limit applies after ranking.
	hits, _ = s.Query(ctx, "", "steward", 1)
	if len(hits) != 1 {
		t.Fatalf("limit: got %+v", hits)
	}
}
