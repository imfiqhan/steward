package steward

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// searchWindow bounds how many hits are taken from the engine for one query.
//
// The engine ranks, then the rows are read through the repository so filters,
// sorts, and a policy's row scope still apply. That second pass can only narrow
// what the first returned, so a window is a real limit: a query matching more
// than this many documents may drop matches that the filters would have kept.
// It is set high enough that a person paging through results never reaches it,
// and low enough that one query cannot pull an entire index into memory.
const searchWindow = 1000

// indexer is the optional half of a resource: one that declared Searchable can
// build its own documents.
type indexer interface {
	searchDocs(rows any) []SearchDoc
	searchType() string
	indexable() bool
}

// Searchable declares which paths go into the search index, and turns quick
// search and the command palette over to Config.Searcher for this resource.
//
//	posts.Searchable("Title", "Body")
//
// Without a Searcher configured this does nothing — the SQL LIKE path stays.
// The two are deliberately separate: an app can declare what its records are
// findable by and decide later what answers the query.
func (r *Resource[T]) Searchable(paths ...string) *Resource[T] {
	r.searchPaths = append(r.searchPaths, paths...)
	return r
}

func (t *typedResource[T]) searchType() string { return t.res.m.slug }

// indexable reports whether this resource has both a declaration and an engine.
func (t *typedResource[T]) indexable() bool {
	return len(t.res.searchPaths) > 0 && t.res.a.cfg.Searcher != nil
}

// searchDocs turns rows into documents. It takes any because it is reached
// through an interface that cannot name T.
func (t *typedResource[T]) searchDocs(rows any) []SearchDoc {
	items, ok := rows.([]T)
	if !ok {
		return nil
	}
	out := make([]SearchDoc, 0, len(items))
	for i := range items {
		out = append(out, t.searchDoc(&items[i]))
	}
	return out
}

// searchDoc reads one row's declared paths into a document.
func (t *typedResource[T]) searchDoc(row *T) SearchDoc {
	doc := SearchDoc{
		ID:         t.rowKey(row),
		Type:       t.res.m.slug,
		Fields:     map[string]string{},
		Attributes: t.res.searchPaths,
	}
	for _, path := range t.res.searchPaths {
		info, err := t.ft.lookup(path)
		if err != nil {
			continue
		}
		v, ok := info.value(reflect.ValueOf(row))
		if !ok || v == nil {
			continue
		}
		doc.Fields[path] = strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	}
	return doc
}

// indexRow writes one record to the engine. Failures are logged rather than
// returned: a search index that cannot be written is a degraded search, and
// refusing the save would make it a degraded panel.
func (t *typedResource[T]) indexRow(ctx context.Context, row *T) {
	if !t.indexable() {
		return
	}
	if err := t.res.a.cfg.Searcher.Index(ctx, t.searchDoc(row)); err != nil {
		t.res.a.log.Warn("steward: indexing a record", "resource", t.res.m.slug, "err", err)
	}
}

// unindexRows removes records from the engine.
func (t *typedResource[T]) unindexRows(ctx context.Context, ids []string) {
	if !t.indexable() || len(ids) == 0 {
		return
	}
	if err := t.res.a.cfg.Searcher.Delete(ctx, t.res.m.slug, ids...); err != nil {
		t.res.a.log.Warn("steward: removing records from the index",
			"resource", t.res.m.slug, "err", err)
	}
}

// searchIDs asks the engine which records match, in its order.
func (t *typedResource[T]) searchIDs(ctx context.Context, query string, limit int) ([]string, bool) {
	if !t.indexable() || strings.TrimSpace(query) == "" {
		return nil, false
	}
	if limit <= 0 || limit > searchWindow {
		limit = searchWindow
	}
	hits, err := t.res.a.cfg.Searcher.Query(ctx, t.res.m.slug, query, limit)
	if err != nil {
		// Falling back to SQL beats showing nothing, and the log says which
		// happened rather than leaving a quietly worse result.
		t.res.a.log.Warn("steward: search engine query", "resource", t.res.m.slug, "err", err)
		return nil, false
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids, true
}

// Reindex rebuilds one resource's documents from the database, in batches.
//
// This is what makes the feature usable on a table that already has rows:
// indexing on write only ever covers what is written afterwards, so without a
// backfill the engine answers for the newest records and silently omits the
// rest.
func (t *typedResource[T]) Reindex(ctx context.Context, batch int) (int, error) {
	if !t.indexable() {
		return 0, nil
	}
	if batch <= 0 {
		batch = 500
	}
	total := 0
	for page := 1; ; page++ {
		items, _, err := t.repo.List(ctx, &ListQuery{Page: page, PerPage: batch})
		if err != nil {
			return total, err
		}
		if len(items) == 0 {
			return total, nil
		}
		docs := make([]SearchDoc, 0, len(items))
		for i := range items {
			docs = append(docs, t.searchDoc(&items[i]))
		}
		if err := t.res.a.cfg.Searcher.Index(ctx, docs...); err != nil {
			return total, fmt.Errorf("indexing %s page %d: %w", t.res.m.slug, page, err)
		}
		total += len(docs)
		if len(items) < batch {
			return total, nil
		}
	}
}

// Reindex rebuilds every searchable resource's documents. It reports what it
// wrote per resource, so a backfill that skipped something says so.
func (a *Admin) Reindex(ctx context.Context, batch int) (map[string]int, error) {
	if err := a.Build(); err != nil {
		return nil, err
	}
	if a.cfg.Searcher == nil {
		return nil, fmt.Errorf("steward: Reindex needs Config.Searcher")
	}
	out := map[string]int{}
	for _, entry := range a.registry {
		r, ok := entry.(reindexer)
		if !ok || !r.indexable() {
			continue
		}
		n, err := r.reindex(ctx, batch)
		out[entry.meta().slug] = n
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// reindexer is Reindex behind the type-erased registry.
type reindexer interface {
	indexable() bool
	reindex(ctx context.Context, batch int) (int, error)
}

func (t *typedResource[T]) reindex(ctx context.Context, batch int) (int, error) {
	return t.Reindex(ctx, batch)
}
