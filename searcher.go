package steward

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// MemorySearcher is a simple in-process inverted index: lowercase word tokens,
// AND across the query's terms, ranked by summed term frequency.
//
// It is here so the Searcher path can be exercised without standing an engine
// up — in a test, or on a panel small enough not to need one. It holds
// everything in memory and rebuilds from nothing on restart, so a panel that
// depends on search wants a real engine behind the same interface.
type MemorySearcher struct {
	mu    sync.RWMutex
	docs  map[string]SearchDoc      // key → doc
	index map[string]map[string]int // term → key → count
	terms map[string][]string       // key → its terms, for replacing a document
}

var _ Searcher = (*MemorySearcher)(nil)

// NewMemorySearcher returns an empty index.
func NewMemorySearcher() *MemorySearcher {
	return &MemorySearcher{
		docs:  map[string]SearchDoc{},
		index: map[string]map[string]int{},
		terms: map[string][]string{},
	}
}

// docKey namespaces an ID by its type. Two resources will have a row 1 each,
// and without this the second would replace the first.
func docKey(typ, id string) string { return typ + "\x00" + id }

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Index implements Searcher.
func (m *MemorySearcher) Index(_ context.Context, docs ...SearchDoc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, doc := range docs {
		key := docKey(doc.Type, doc.ID)
		m.forget(key)
		var docTerms []string
		for _, val := range doc.Fields {
			for _, term := range tokenize(val) {
				posting := m.index[term]
				if posting == nil {
					posting = map[string]int{}
					m.index[term] = posting
				}
				posting[key]++
				docTerms = append(docTerms, term)
			}
		}
		m.docs[key] = doc
		m.terms[key] = docTerms
	}
	return nil
}

// Delete implements Searcher.
func (m *MemorySearcher) Delete(_ context.Context, typ string, ids ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		key := docKey(typ, id)
		m.forget(key)
		delete(m.docs, key)
		delete(m.terms, key)
	}
	return nil
}

// forget drops a document's postings. The caller holds the lock.
func (m *MemorySearcher) forget(key string) {
	for _, term := range m.terms[key] {
		posting := m.index[term]
		if posting == nil {
			continue
		}
		delete(posting, key)
		if len(posting) == 0 {
			delete(m.index, term)
		}
	}
}

// Query implements Searcher: every term must match, and only documents of the
// type asked for are returned.
func (m *MemorySearcher) Query(_ context.Context, typ, q string, limit int) ([]SearchHit, error) {
	terms := tokenize(q)
	if len(terms) == 0 {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	scores := map[string]float64{}
	for i, term := range terms {
		posting := m.index[term]
		if posting == nil {
			return nil, nil // AND: a term nothing holds empties the result
		}
		for key, count := range posting {
			if i > 0 {
				if _, ok := scores[key]; !ok {
					continue
				}
			}
			scores[key] += float64(count)
		}
		// Intersect: drop what missed this term.
		for key := range scores {
			if _, ok := posting[key]; !ok {
				delete(scores, key)
			}
		}
	}

	hits := make([]SearchHit, 0, len(scores))
	for key, score := range scores {
		doc := m.docs[key]
		if typ != "" && doc.Type != typ {
			continue
		}
		hits = append(hits, SearchHit{ID: doc.ID, Type: doc.Type, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
