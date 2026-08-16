package steward

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormRepository is the default Repository backed by a *gorm.DB. Field paths
// in queries resolve through the model's parsed schema; one-hop relation
// paths referenced by grid columns are preloaded automatically by the grid,
// not here.
type GormRepository[T any] struct {
	db       *gorm.DB
	ft       *fieldTable
	preloads []string
	refine   []func(*gorm.DB) *gorm.DB
}

// NewGormRepository builds the default repository for T.
func NewGormRepository[T any](db *gorm.DB) (*GormRepository[T], error) {
	var zero T
	ft, err := newFieldTable(reflect.TypeOf(zero), db.NamingStrategy)
	if err != nil {
		return nil, err
	}
	return &GormRepository[T]{db: db, ft: ft}, nil
}

// With adds relations to preload on List and Find.
func (r *GormRepository[T]) With(relations ...string) *GormRepository[T] {
	r.preloads = append(r.preloads, relations...)
	return r
}

// Query appends a refinement applied to every statement — the escape hatch
// for base scoping ("only rows of this tenant").
func (r *GormRepository[T]) Query(fn func(*gorm.DB) *gorm.DB) *GormRepository[T] {
	r.refine = append(r.refine, fn)
	return r
}

// KeyName implements Repository.
func (r *GormRepository[T]) KeyName() string { return r.ft.pk.Path }

func (r *GormRepository[T]) base(ctx context.Context) *gorm.DB {
	var zero T
	db := r.db.WithContext(ctx).Model(&zero)
	for _, fn := range r.refine {
		db = fn(db)
	}
	return db
}

func (r *GormRepository[T]) withPreloads(db *gorm.DB) *gorm.DB {
	for _, p := range r.preloads {
		db = db.Preload(p)
	}
	return db
}

// column resolves a direct (non-relation) field path to its bare column
// name (for GORM APIs that match field names, like Updates+Select).
func (r *GormRepository[T]) column(path string) (string, error) {
	info, ok := r.ft.byPath[path]
	if !ok || info.DBName == "" {
		return "", fmt.Errorf("steward: field %q is not filterable/sortable on %s", path, r.ft.model.Name)
	}
	return info.DBName, nil
}

// qcolumn resolves a field path to its dialect-quoted column for hand-built
// SQL fragments — reserved words like "order" are valid column names and
// must never reach SQL bare.
func (r *GormRepository[T]) qcolumn(path string) (string, error) {
	col, err := r.column(path)
	if err != nil {
		return "", err
	}
	return quoteColumn(r.db, col), nil
}

// quoteColumn applies the dialect's identifier quoting.
func quoteColumn(db *gorm.DB, name string) string {
	var sb strings.Builder
	db.QuoteTo(&sb, name)
	return sb.String()
}

// condSQL renders one condition as a WHERE fragment plus arguments. A direct
// column compares in place; a relation path becomes an IN (subquery).
func (r *GormRepository[T]) condSQL(c Cond) (string, []any, error) {
	info, ok := r.ft.byPath[c.Path]
	if !ok {
		return "", nil, fmt.Errorf("steward: unknown field %q on %s", c.Path, r.ft.model.Name)
	}
	if info.rel != nil {
		return r.relCondSQL(info.rel, c)
	}
	if info.DBName == "" {
		return "", nil, fmt.Errorf("steward: field %q is not filterable on %s", c.Path, r.ft.model.Name)
	}
	return predicateSQL(quoteColumn(r.db, info.DBName), c)
}

func (r *GormRepository[T]) applyCond(db *gorm.DB, c Cond) (*gorm.DB, error) {
	sql, args, err := r.condSQL(c)
	if err != nil {
		return db, err
	}
	return db.Where(sql, args...), nil
}

// List implements Repository.
func (r *GormRepository[T]) List(ctx context.Context, q *ListQuery) ([]T, int64, error) {
	db := r.base(ctx)
	var err error
	for _, c := range q.Conds {
		if db, err = r.applyCond(db, c); err != nil {
			return nil, 0, err
		}
	}
	for _, c := range q.SearchConds {
		if db, err = r.applyCond(db, c); err != nil {
			return nil, 0, err
		}
	}
	if q.Search != "" && len(q.SearchPaths) > 0 {
		var clauses []string
		var args []any
		for _, p := range q.SearchPaths {
			// Relation paths search through their subquery, so quick search
			// reaches "Author.Name" and "Tags.Tag" rather than silently
			// ignoring them.
			sql, a, cerr := r.condSQL(Cond{Path: p, Op: OpLike, Val: q.Search})
			if cerr != nil {
				continue
			}
			clauses = append(clauses, sql)
			args = append(args, a...)
		}
		if len(clauses) > 0 {
			db = db.Where("("+strings.Join(clauses, " OR ")+")", args...)
		}
	}
	for _, s := range q.Scopes {
		if fn, ok := s.(func(*gorm.DB) *gorm.DB); ok {
			db = fn(db)
		}
	}

	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Ordering. When a search engine ranked the rows, that ranking is the
	// order — expressed as a CASE over the ids, because MySQL's FIELD() and
	// Postgres' array_position have no common spelling and SQLite has neither.
	//
	// It is built as one expression rather than successive Order() calls:
	// GORM's Order takes a string, a clause.OrderByColumn, or a clause.OrderBy,
	// and silently drops anything else — a clause.Expr passed to it does
	// nothing at all, which is how this shipped ordering by id and looking
	// like it worked.
	var order strings.Builder
	var orderArgs []any
	if len(q.IDOrder) > 0 {
		if col, cerr := r.qcolumn(r.ft.pk.Path); cerr == nil {
			order.WriteString("CASE " + col)
			for i, id := range q.IDOrder {
				order.WriteString(" WHEN ? THEN " + strconv.Itoa(i))
				orderArgs = append(orderArgs, id)
			}
			order.WriteString(" ELSE " + strconv.Itoa(len(q.IDOrder)) + " END")
		}
	}
	for _, s := range q.Sorts {
		col, serr := r.qcolumn(s.Path)
		if serr != nil {
			continue
		}
		if order.Len() > 0 {
			order.WriteString(", ")
		}
		order.WriteString(col)
		if s.Desc {
			order.WriteString(" DESC")
		} else {
			order.WriteString(" ASC")
		}
	}
	if order.Len() > 0 {
		db = db.Order(clause.OrderBy{
			Expression: clause.Expr{SQL: order.String(), Vars: orderArgs},
		})
	}

	if q.PerPage > 0 {
		page := max(q.Page, 1)
		db = db.Limit(q.PerPage).Offset((page - 1) * q.PerPage)
	}

	var items []T
	if err := r.withPreloads(db).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// Find implements Repository.
func (r *GormRepository[T]) Find(ctx context.Context, id string) (*T, error) {
	var m T
	db := r.withPreloads(r.base(ctx))
	if err := db.Where(quoteColumn(r.db, r.ft.pk.DBName)+" = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// Create implements Repository.
func (r *GormRepository[T]) Create(ctx context.Context, m *T) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// Update implements Repository; fields are model field paths (converted to
// columns), nil updates all non-zero the GORM way — builders always pass the
// dirty list explicitly.
func (r *GormRepository[T]) Update(ctx context.Context, m *T, fields []string) error {
	db := r.db.WithContext(ctx).Model(m)
	if len(fields) > 0 {
		cols := make([]string, 0, len(fields))
		for _, f := range fields {
			col, err := r.column(f)
			if err != nil {
				return err
			}
			cols = append(cols, col)
		}
		db = db.Select(cols)
	}
	return db.Updates(m).Error
}

// Delete implements Repository (hard delete; ids match the primary key).
func (r *GormRepository[T]) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var zero T
	return r.base(ctx).Where(quoteColumn(r.db, r.ft.pk.DBName)+" IN ?", ids).Delete(&zero).Error
}
