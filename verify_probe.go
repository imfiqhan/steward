package steward

import (
	"context"
	"fmt"
	"sort"

	"gorm.io/gorm"
)

// A panel's declarations are checked against the Go type: a path resolves to a
// struct field, a colour is in the palette. What none of that reaches is the
// database, and the gap between the two is where a whole family of failures
// lives — a column the model declares and the migration never added, a type the
// dialect will not carry through the predicate it is asked for. Both compile,
// both pass every test that does not run the query, and both fail the first
// time someone types in a search box.
//
// So the statements a panel's own declarations build are run. LIMIT 0 is the
// whole trick: the database parses, analyses and plans the query — which is
// where these failures are raised — and returns without reading a row.

// queryProber is the optional half of a resource: one that can run the
// statements its declarations build. Kept off resourceEntry for the reason the
// others are — the interface says what every resource does, and this is not
// that.
type queryProber interface {
	probeQueries(ctx context.Context, a *Admin) []error
}

// probeLimitZero bounds every probe. A predicate on a large table would
// otherwise scan it to answer a question nobody asked.
func probeLimitZero(db *gorm.DB) *gorm.DB { return db.Limit(0) }

// probeQueries runs one bounded query per declaration that becomes SQL, and
// reports what the database refused. Each error names the declaration and,
// where it is known, the line it was written on.
func (t *typedResource[T]) probeQueries(ctx context.Context, a *Admin) []error {
	gr, ok := t.repo.(*GormRepository[T])
	if !ok {
		return nil // a repository of someone else's making answers for itself
	}
	// A table that does not exist yet is a migration that has not run, not a
	// declaration that is wrong. Verify is about the panel, not the deployment.
	if !a.db.Migrator().HasTable(new(T)) {
		return nil
	}

	var errs []error
	run := func(what, site string, q *ListQuery) {
		q.PerPage = 0 // the scope below sets the bound instead
		q.SkipCount = true
		q.Scopes = append(q.Scopes, probeLimitZero)
		if _, _, err := gr.List(ctx, q); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: the database refused this query%s: %w",
				t.res.m.slug, what, at(site), err))
		}
	}

	// Naming every column the model declares, rather than the SELECT * a grid
	// issues. A missing column is invisible to *: the rows come back, the field
	// stays at its zero value, and the panel shows an empty column forever.
	// Asked for by name, the database says it is not there.
	if cols := t.modelColumns(); len(cols) > 0 {
		run("reading the table", "", &ListQuery{
			Scopes: []any{func(db *gorm.DB) *gorm.DB { return db.Select(cols) }},
		})
	}

	// Quick search and the palette put every declared path into one OR, so one
	// path the dialect cannot pattern-match takes the whole statement with it.
	// They are probed together for that reason, and then apart to say which.
	for _, decl := range []struct {
		what  string
		paths []string
	}{
		{"quick search", t.grid.quickSearch},
		{"command search", t.res.commandPaths},
	} {
		if len(decl.paths) == 0 {
			continue
		}
		run(decl.what, "", &ListQuery{Search: probeTerm, SearchPaths: decl.paths})
		if len(decl.paths) > 1 {
			for _, p := range decl.paths {
				run(decl.what+" "+quote(p), "", &ListQuery{Search: probeTerm, SearchPaths: []string{p}})
			}
		}
	}

	// Every filter, one at a time, so a refusal names the filter rather than
	// the set of them.
	for _, fi := range t.grid.filters {
		if fi.info == nil {
			continue // already reported as an unresolved path
		}
		val := probeValueFor(fi.info)
		if val == nil {
			continue
		}
		run("filter "+quote(fi.path), fi.declaredAt,
			&ListQuery{Conds: []Cond{{Path: fi.path, Op: fi.op, Val: val, Val2: val}}})
	}

	// Sorting, which is an expression the database has to accept too.
	for _, col := range t.grid.columns {
		if !col.sortable || col.info == nil || col.info.DBName == "" {
			continue
		}
		run("sorting by "+quote(col.path), col.declaredAt,
			&ListQuery{Sorts: []Sort{{Path: col.path}}})
	}
	return errs
}

// probeTerm is what a search probe looks for. It matches nothing, which is the
// point: the question is whether the database will run the query, not what it
// would return.
const probeTerm = "steward probe"

// probeValueFor is a value of the right shape for a filter's column, so the
// probe exercises the comparison the filter will actually make.
func probeValueFor(info *fieldInfo) any {
	switch info.Kind {
	case kindInt, kindUint, kindFloat:
		return 0
	case kindBool:
		return false
	case kindTime:
		return "2000-01-01"
	default:
		return probeTerm
	}
}

func quote(s string) string { return `"` + s + `"` }

// modelColumns is every column the model declares on its own table. Relation
// paths are left out: they live on another table and are reached by a join the
// probe does not make.
func (t *typedResource[T]) modelColumns() []string {
	seen := map[string]bool{}
	var out []string
	for _, info := range t.ft.byPath {
		if info.Relation != "" || info.DBName == "" || seen[info.DBName] {
			continue
		}
		seen[info.DBName] = true
		out = append(out, info.DBName)
	}
	sort.Strings(out)
	return out
}
