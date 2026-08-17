package steward

import "context"

// Op is a filter comparison operator.
type Op string

// Filter operators supported by ListQuery conditions.
const (
	OpEq      Op = "eq"
	OpNe      Op = "ne"
	OpGt      Op = "gt"
	OpGte     Op = "gte"
	OpLt      Op = "lt"
	OpLte     Op = "lte"
	OpLike    Op = "like"
	OpPrefix  Op = "prefix"
	OpIn      Op = "in"
	OpBetween Op = "between"
	OpNull    Op = "null"
)

// Cond is one filter condition against a field path ("Title", "Status").
// Val2 is the upper bound for OpBetween.
type Cond struct {
	Path string
	Op   Op
	Val  any
	Val2 any
}

// Sort orders results by a field path.
type Sort struct {
	Path string
	Desc bool
}

// ListQuery describes one grid page load: filters, quick search, ordering,
// and pagination. Scopes carry backend-specific query refinements (row
// policies, filter scopes) — for the GORM repository they are
// func(*gorm.DB) *gorm.DB, passed through untyped so the interface stays
// backend-neutral.
type ListQuery struct {
	Conds       []Cond
	Search      string
	SearchConds []Cond // parsed quick-search terms (field-targeted)
	SearchPaths []string
	Sorts       []Sort
	// SkipCount drops the COUNT that pages a grid. A caller that shows a fixed
	// few rows and never paginates pays for it twice over: on a large table the
	// count scans every match while the rows themselves stop at the limit.
	// Total is 0 when it is set.
	SkipCount bool

	// IDOrder ranks the rows by primary key, most relevant first, and is what a
	// search engine's answer arrives as. It orders before Sorts, because the
	// order a person asked for beats the order they did not — a chosen column
	// sort is added to Sorts and takes precedence by not setting this at all.
	//
	// Without it the ranking is thrown away: the IDs go in as an IN condition
	// and the database returns them in whatever order it likes, so "the 1000
	// best matches" becomes "10 of them, by id".
	IDOrder []string
	Page    int
	PerPage int

	// After pages by primary key rather than by OFFSET: rows come back ordered
	// by key ascending, starting past this value, and Sorts and Page are
	// ignored. It exists for walking a whole table — OFFSET re-reads every row
	// it has already skipped, so the last page of a large export costs as much
	// as all of the ones before it.
	//
	// Nil means offset paging. A caller walks with the last key it saw.
	After any
	Scopes  []any
}

// Repository is the data seam Grid, Form, and Detail render through. The
// GORM implementation is the default; anything that can list, fetch, and
// mutate records — a REST API, a file store — can back a resource.
type Repository[T any] interface {
	Find(ctx context.Context, id string) (*T, error)
	List(ctx context.Context, q *ListQuery) (items []T, total int64, err error)
	Create(ctx context.Context, m *T) error
	// Update persists the named fields (all when nil).
	Update(ctx context.Context, m *T, fields []string) error
	Delete(ctx context.Context, ids []string) error
	KeyName() string
}
