package steward

// Aggregate reads for dashboard widgets: totals, group-by counts, and
// time-bucketed series, so a widget does not hand-roll SQL.
//
// Aggregator is an *optional* capability rather than a Repository method: the
// interface has six implementations' worth of surface already, and adding to it
// would break every custom repository. GormRepository implements it; anything
// else may opt in.

import (
	"context"
	"fmt"
	"reflect"
)

// AggFunc is the aggregate to compute.
type AggFunc string

const (
	AggCount AggFunc = "count"
	AggSum   AggFunc = "sum"
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
)

// Period buckets a date or timestamp column.
//
// Week is deliberately absent: the three supported engines disagree on where a
// week starts and on week numbering, and only SQLite is covered by integration
// tests here, so shipping it would mean three subtly different definitions.
type Period string

const (
	PeriodDay   Period = "day"
	PeriodMonth Period = "month"
	PeriodYear  Period = "year"
)

// AggQuery is one aggregate read.
type AggQuery struct {
	// Func defaults to AggCount.
	Func AggFunc
	// Path is the field path to aggregate. Required except for AggCount.
	Path string
	// GroupBy is the field path to group by. Empty returns a single total.
	GroupBy string
	// Period buckets GroupBy by date instead of grouping on its exact value.
	Period Period
	// Conds filter the rows considered, using the same operators as a grid.
	Conds []Cond
	// Scopes are backend-specific refinements — func(*gorm.DB) *gorm.DB for
	// the GORM repository, matching ListQuery.Scopes.
	Scopes []any
	// Limit caps the number of groups returned. Zero means no cap.
	Limit int
	// Desc orders groups by value descending (top-N). Otherwise groups come
	// back ordered by key, which is chronological for a Period.
	Desc bool
}

// AggRow is one group's result.
type AggRow struct {
	Key   string
	Value float64
}

// AggRows is an ordered aggregate result.
type AggRows []AggRow

// Total sums every row's value.
func (rows AggRows) Total() float64 {
	var n float64
	for _, r := range rows {
		n += r.Value
	}
	return n
}

// Chart turns the rows into a single-series chart, keys becoming labels.
func (rows AggRows) Chart(t ChartType, label string) *ChartData {
	cd := &ChartData{Type: t, Labels: make([]string, len(rows))}
	vals := make([]float64, len(rows))
	for i, r := range rows {
		cd.Labels[i] = r.Key
		vals[i] = r.Value
	}
	cd.Series = []ChartSeries{{Label: label, Values: vals}}
	return cd
}

// Aggregator is the optional repository capability behind the helpers below.
type Aggregator interface {
	Aggregate(ctx context.Context, q *AggQuery) (AggRows, error)
}

// aggregatorProvider lets a registered resource expose its repository's
// aggregate support without widening the resourceEntry interface.
type aggregatorProvider interface {
	aggregator() Aggregator
}

func (t *typedResource[T]) aggregator() Aggregator {
	if agg, ok := t.repo.(Aggregator); ok {
		return agg
	}
	return nil
}

// aggregatorFor prefers the registered resource's repository, so custom
// repositories and their query refinements are honoured. For a model that is
// not a resource it falls back to a plain GORM aggregator over Config.DB.
func aggregatorFor[T any](c *Context) (Aggregator, error) {
	var zero T
	t := reflect.TypeOf(zero)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil {
		if entry, ok := c.Admin.byType[t]; ok {
			if p, ok := entry.(aggregatorProvider); ok {
				if agg := p.aggregator(); agg != nil {
					return agg, nil
				}
			}
			return nil, fmt.Errorf("steward: the repository for %s does not support aggregates", t.Name())
		}
	}
	repo, err := NewGormRepository[T](c.Admin.DB())
	if err != nil {
		return nil, err
	}
	return repo, nil
}

// Aggregate runs an arbitrary aggregate against T.
func Aggregate[T any](c *Context, q *AggQuery) (AggRows, error) {
	agg, err := aggregatorFor[T](c)
	if err != nil {
		return nil, err
	}
	return agg.Aggregate(c.Ctx(), q)
}

// Count returns how many T rows match conds.
func Count[T any](c *Context, conds ...Cond) (int64, error) {
	rows, err := Aggregate[T](c, &AggQuery{Func: AggCount, Conds: conds})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return int64(rows[0].Value), nil
}

// Sum totals a numeric field across matching T rows.
func Sum[T any](c *Context, path string, conds ...Cond) (float64, error) {
	rows, err := Aggregate[T](c, &AggQuery{Func: AggSum, Path: path, Conds: conds})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Value, nil
}

// GroupCount counts T grouped by a field path, largest group first — the shape
// a pie or bar chart wants. Pass limit 0 for every group.
func GroupCount[T any](c *Context, path string, limit int, conds ...Cond) (AggRows, error) {
	return Aggregate[T](c, &AggQuery{
		Func: AggCount, GroupBy: path, Conds: conds, Limit: limit, Desc: true,
	})
}

// PeriodCount counts T bucketed by a date field, oldest bucket first.
//
// Buckets with no rows are absent rather than zero: the query only sees rows
// that exist. Fill gaps yourself if a continuous axis matters.
func PeriodCount[T any](c *Context, path string, p Period, conds ...Cond) (AggRows, error) {
	return Aggregate[T](c, &AggQuery{
		Func: AggCount, GroupBy: path, Period: p, Conds: conds,
	})
}

// PeriodSum totals a numeric field bucketed by a date field, oldest first.
func PeriodSum[T any](c *Context, sumPath, datePath string, p Period, conds ...Cond) (AggRows, error) {
	return Aggregate[T](c, &AggQuery{
		Func: AggSum, Path: sumPath, GroupBy: datePath, Period: p, Conds: conds,
	})
}
