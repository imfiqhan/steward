package steward

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// periodFormats holds each engine's date-formatting call and the pattern for
// every supported bucket. Patterns are chosen so keys sort lexicographically in
// chronological order, which is what lets the caller skip a sort.
var periodFormats = map[string]struct {
	fn    string // %s = column, %s = pattern
	day   string
	month string
	year  string
}{
	"sqlite":   {fn: "strftime('%[2]s', %[1]s)", day: "%Y-%m-%d", month: "%Y-%m", year: "%Y"},
	"mysql":    {fn: "DATE_FORMAT(%[1]s, '%[2]s')", day: "%Y-%m-%d", month: "%Y-%m", year: "%Y"},
	"postgres": {fn: "to_char(%[1]s, '%[2]s')", day: "YYYY-MM-DD", month: "YYYY-MM", year: "YYYY"},
}

// periodExpr builds the SQL that buckets a quoted column by period. The pattern
// comes from the table above, never from caller input, so nothing here is
// interpolated from user data.
func periodExpr(dialect, quotedCol string, p Period) (string, error) {
	f, ok := periodFormats[dialect]
	if !ok {
		return "", fmt.Errorf("steward: period bucketing is not supported on %q (sqlite, mysql, postgres are)", dialect)
	}
	var pattern string
	switch p {
	case PeriodDay:
		pattern = f.day
	case PeriodMonth:
		pattern = f.month
	case PeriodYear:
		pattern = f.year
	default:
		return "", fmt.Errorf("steward: unknown period %q", p)
	}
	return fmt.Sprintf(f.fn, quotedCol, pattern), nil
}

// valueExpr builds the aggregate call.
func valueExpr(fn AggFunc, quotedCol string) (string, error) {
	if fn == "" || fn == AggCount {
		return "COUNT(*)", nil
	}
	if quotedCol == "" {
		return "", fmt.Errorf("steward: AggQuery.Path is required for %q", fn)
	}
	switch fn {
	case AggSum:
		return "SUM(" + quotedCol + ")", nil
	case AggAvg:
		return "AVG(" + quotedCol + ")", nil
	case AggMin:
		return "MIN(" + quotedCol + ")", nil
	case AggMax:
		return "MAX(" + quotedCol + ")", nil
	}
	return "", fmt.Errorf("steward: unknown aggregate %q", fn)
}

// aggScan receives one result row. Pointers distinguish SQL NULL — SUM over no
// rows is NULL, not zero — from a genuine value.
type aggScan struct {
	AggKey   *string
	AggValue *float64
}

// Aggregate implements Aggregator over GORM.
func (r *GormRepository[T]) Aggregate(ctx context.Context, q *AggQuery) (AggRows, error) {
	if q == nil {
		return nil, fmt.Errorf("steward: AggQuery is nil")
	}

	var valCol string
	if q.Func != "" && q.Func != AggCount {
		col, err := r.qcolumn(q.Path)
		if err != nil {
			return nil, err
		}
		valCol = col
	}
	valSQL, err := valueExpr(q.Func, valCol)
	if err != nil {
		return nil, err
	}

	db := r.base(ctx)
	for _, c := range q.Conds {
		if db, err = r.applyCond(db, c); err != nil {
			return nil, err
		}
	}
	for _, s := range q.Scopes {
		if fn, ok := s.(func(*gorm.DB) *gorm.DB); ok {
			db = fn(db)
		}
	}

	// No grouping: one row, no key.
	if q.GroupBy == "" {
		var out aggScan
		if err := db.Select(valSQL + " AS agg_value").Scan(&out).Error; err != nil {
			return nil, err
		}
		var v float64
		if out.AggValue != nil {
			v = *out.AggValue
		}
		return AggRows{{Value: v}}, nil
	}

	groupCol, err := r.qcolumn(q.GroupBy)
	if err != nil {
		return nil, err
	}
	groupSQL := groupCol
	if q.Period != "" {
		if groupSQL, err = periodExpr(r.db.Dialector.Name(), groupCol, q.Period); err != nil {
			return nil, err
		}
	}

	db = db.Select(groupSQL + " AS agg_key, " + valSQL + " AS agg_value").Group(groupSQL)
	if q.Desc {
		db = db.Order("agg_value DESC")
	} else {
		db = db.Order("agg_key ASC")
	}
	if q.Limit > 0 {
		db = db.Limit(q.Limit)
	}

	var scanned []aggScan
	if err := db.Scan(&scanned).Error; err != nil {
		return nil, err
	}
	rows := make(AggRows, 0, len(scanned))
	for _, s := range scanned {
		row := AggRow{}
		if s.AggKey != nil {
			row.Key = *s.AggKey
		}
		if s.AggValue != nil {
			row.Value = *s.AggValue
		}
		rows = append(rows, row)
	}
	return rows, nil
}
