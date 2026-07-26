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
	Page        int
	PerPage     int
	Scopes      []any
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
