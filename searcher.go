package steward

import (
	"context"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// MemorySearcher is a simple in-process inverted-index Searcher: lowercase
// word tokens, AND semantics across query terms, scored by matched-term
// frequency. Suitable for small-to-medium corpora; swap in an external
// engine behind the same interface when you outgrow it.
type MemorySearcher struct {
	mu    sync.RWMutex
	docs  map[string]SearchDoc      // id → doc
	index map[string]map[string]int // term → id → count
	terms map[string][]string       // id → its terms (for reindex/delete)
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

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Index adds or replaces a document.
func (m *MemorySearcher) Index(_ context.Context, doc SearchDoc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Drop the previous version's postings.
	for _, term := range m.terms[doc.ID] {
		if posting := m.index[term]; posting != nil {
			delete(posting, doc.ID)
			if len(posting) == 0 {
				delete(m.index, term)
			}
		}
	}
	var docTerms []string
	for _, val := range doc.Fields {
		for _, term := range tokenize(val) {
			posting := m.index[term]
			if posting == nil {
				posting = map[string]int{}
				m.index[term] = posting
			}
			posting[doc.ID]++
			docTerms = append(docTerms, term)
		}
	}
	m.docs[doc.ID] = doc
	m.terms[doc.ID] = docTerms
	return nil
}

// Query implements Searcher: every term must match; results rank by summed
// term frequency.
func (m *MemorySearcher) Query(_ context.Context, q string, limit int) ([]SearchHit, error) {
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
			return nil, nil // AND semantics: a missing term empties the result
		}
		for id, count := range posting {
			if i > 0 {
				if _, ok := scores[id]; !ok {
					continue
				}
			}
			scores[id] += float64(count)
		}
		// Intersect: drop ids that missed this term.
		for id := range scores {
			if _, ok := posting[id]; !ok {
				delete(scores, id)
			}
		}
	}

	hits := make([]SearchHit, 0, len(scores))
	for id, score := range scores {
		hits = append(hits, SearchHit{ID: id, Type: m.docs[id].Type, Score: score})
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
