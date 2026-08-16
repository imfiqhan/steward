package meilistore_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	steward "github.com/imfiqhan/steward"
	"github.com/imfiqhan/steward/contrib/meilistore"
)

// live returns a Store against a running Meilisearch, or skips. Set
// MEILI_TEST_HOST to point at one:
//
//	podman run --rm -p 7701:7700 -e MEILI_MASTER_KEY=k getmeili/meilisearch:v1.11
//	MEILI_TEST_HOST=http://127.0.0.1:7701 MEILI_TEST_KEY=k go test ./...
//
// Skipped rather than mocked on purpose: what is worth testing here is whether
// the requests this driver builds are ones Meilisearch accepts, and a fake that
// answers them would only prove the fake agrees with itself.
func live(t *testing.T) *meilistore.Store {
	t.Helper()
	host := os.Getenv("MEILI_TEST_HOST")
	if host == "" {
		t.Skip("MEILI_TEST_HOST is not set; skipping the live Meilisearch test")
	}
	s, err := meilistore.New(meilistore.Config{
		Host:   host,
		APIKey: os.Getenv("MEILI_TEST_KEY"),
		// Namespaced per run, so a test never reads another run's leftovers.
		Prefix: fmt.Sprintf("t%d-", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// settle waits for Meilisearch to finish indexing. Writes are asynchronous:
// asking straight after a write is the single most common way a test like this
// passes locally and flakes everywhere else.
func settle(t *testing.T, s *meilistore.Store, typ, query string, want int) []steward.SearchHit {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last []steward.SearchHit
	for time.Now().Before(deadline) {
		hits, err := s.Query(t.Context(), typ, query, 20)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		last = hits
		if len(hits) == want {
			return hits
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("after 15s, %q returned %d hits, want %d", query, len(last), want)
	return nil
}

func TestIndexQueryDelete(t *testing.T) {
	s := live(t)
	ctx := t.Context()

	err := s.Index(ctx,
		steward.SearchDoc{ID: "1", Type: "berita", Fields: map[string]string{
			"Title": "Bank Jatim sabet dua penghargaan digital",
		}},
		steward.SearchDoc{ID: "2", Type: "berita", Fields: map[string]string{
			"Title": "Koni Jatim persiapkan puslatda",
		}},
		// A different resource, same ID: the two must not collide.
		steward.SearchDoc{ID: "1", Type: "auth/users", Fields: map[string]string{
			"Name": "Bank clerk",
		}},
	)
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	hits := settle(t, s, "berita", "bank jatim", 1)
	if hits[0].ID != "1" {
		t.Errorf("hit = %+v, want id 1", hits[0])
	}
	if hits[0].Type != "berita" {
		t.Errorf("type = %q", hits[0].Type)
	}

	// A slug with a slash is not a legal Meilisearch index UID; the driver has
	// to map it, and the mapping has to hold both ways.
	users := settle(t, s, "auth/users", "bank", 1)
	if users[0].ID != "1" {
		t.Errorf("users hit = %+v", users[0])
	}

	// Both terms must be present, which is what makes search useful rather than
	// merely non-empty.
	settle(t, s, "berita", "jatim", 2)

	if err := s.Delete(ctx, "berita", "1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	settle(t, s, "berita", "bank jatim", 0)
	// Deleting from one index leaves the other alone.
	settle(t, s, "auth/users", "bank", 1)
}

// TestQueryOnAnIndexThatDoesNotExist covers a panel searching before anything
// has been indexed. Meilisearch answers 404; a search that has nothing to find
// is not a failure to report to the reader.
func TestQueryOnAnIndexThatDoesNotExist(t *testing.T) {
	s := live(t)
	hits, err := s.Query(t.Context(), "never_written", "anything", 10)
	if err != nil {
		t.Fatalf("an unwritten index should answer empty, not fail: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("got %d hits", len(hits))
	}
}

// TestNewRequiresAHost keeps the misconfiguration loud rather than turning
// every later search into a connection error.
func TestNewRequiresAHost(t *testing.T) {
	if _, err := meilistore.New(meilistore.Config{}); err == nil {
		t.Error("a Store was built with no host")
	}
}

// TestIDsSurviveTheRoundTrip: Meilisearch restricts document IDs to
// [A-Za-z0-9_-], and a steward key is whatever the model's primary key is.
func TestIDsSurviveTheRoundTrip(t *testing.T) {
	s := live(t)
	ids := []string{"42", "a-b_c", "uuid:7f3e/9", "spasi ada"}
	// Words far enough apart that Meilisearch's typo tolerance cannot confuse
	// them: "marker0" and "marker1" differ by one character and match each
	// other, which made this test look like an ID bug the first time.
	markers := []string{"alpha", "bravo", "charlie", "delta"}
	docs := make([]steward.SearchDoc, 0, len(ids))
	for i, id := range ids {
		docs = append(docs, steward.SearchDoc{
			ID: id, Type: "keys",
			Fields: map[string]string{"Name": markers[i]},
		})
	}
	if err := s.Index(t.Context(), docs...); err != nil {
		t.Fatalf("index: %v", err)
	}
	for i, want := range ids {
		hits := settle(t, s, "keys", markers[i], 1)
		if hits[0].ID != want {
			t.Errorf("id %q came back as %q", want, hits[0].ID)
		}
	}
}
