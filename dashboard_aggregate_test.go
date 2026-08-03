package steward

import (
	"strings"
	"testing"
)

// The MySQL and Postgres expressions cannot be executed here — only SQLite is a
// test dependency — so they are pinned at the string level instead. If either
// engine's syntax is wrong, this is where it should be corrected.
func TestPeriodExpr(t *testing.T) {
	cases := []struct {
		dialect string
		period  Period
		want    string
	}{
		{"sqlite", PeriodDay, `strftime('%Y-%m-%d', "created_at")`},
		{"sqlite", PeriodMonth, `strftime('%Y-%m', "created_at")`},
		{"sqlite", PeriodYear, `strftime('%Y', "created_at")`},
		{"mysql", PeriodDay, `DATE_FORMAT("created_at", '%Y-%m-%d')`},
		{"mysql", PeriodMonth, `DATE_FORMAT("created_at", '%Y-%m')`},
		{"mysql", PeriodYear, `DATE_FORMAT("created_at", '%Y')`},
		{"postgres", PeriodDay, `to_char("created_at", 'YYYY-MM-DD')`},
		{"postgres", PeriodMonth, `to_char("created_at", 'YYYY-MM')`},
		{"postgres", PeriodYear, `to_char("created_at", 'YYYY')`},
	}
	for _, c := range cases {
		got, err := periodExpr(c.dialect, `"created_at"`, c.period)
		if err != nil {
			t.Errorf("%s/%s: %v", c.dialect, c.period, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.dialect, c.period, got, c.want)
		}
	}
}

func TestPeriodExprRejectsUnknownInputs(t *testing.T) {
	if _, err := periodExpr("sqlserver", `"c"`, PeriodDay); err == nil {
		t.Error("unsupported dialect was accepted")
	} else if !strings.Contains(err.Error(), "sqlserver") {
		t.Errorf("error should name the dialect: %v", err)
	}
	if _, err := periodExpr("sqlite", `"c"`, Period("week")); err == nil {
		t.Error("unknown period was accepted")
	}
}

func TestValueExpr(t *testing.T) {
	// An empty func means count, so a widget can leave it unset.
	for _, fn := range []AggFunc{"", AggCount} {
		got, err := valueExpr(fn, "")
		if err != nil {
			t.Fatalf("%q: %v", fn, err)
		}
		if got != "COUNT(*)" {
			t.Errorf("%q = %q, want COUNT(*)", fn, got)
		}
	}
	for _, c := range []struct {
		fn   AggFunc
		want string
	}{
		{AggSum, `SUM("amount")`},
		{AggAvg, `AVG("amount")`},
		{AggMin, `MIN("amount")`},
		{AggMax, `MAX("amount")`},
	} {
		got, err := valueExpr(c.fn, `"amount"`)
		if err != nil {
			t.Fatalf("%s: %v", c.fn, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.fn, got, c.want)
		}
	}
}

func TestValueExprRequiresPathExceptForCount(t *testing.T) {
	if _, err := valueExpr(AggSum, ""); err == nil {
		t.Error("SUM without a column was accepted")
	} else if !strings.Contains(err.Error(), "Path is required") {
		t.Errorf("error should point at Path: %v", err)
	}
	if _, err := valueExpr(AggFunc("median"), `"x"`); err == nil {
		t.Error("unknown aggregate was accepted")
	}
}

func TestAggRowsChartAndTotal(t *testing.T) {
	rows := AggRows{{Key: "Jan", Value: 3}, {Key: "Feb", Value: 4}}
	if got := rows.Total(); got != 7 {
		t.Errorf("Total() = %v, want 7", got)
	}

	cd := rows.Chart(ChartLine, "Signups")
	if cd.Type != ChartLine {
		t.Errorf("type = %q", cd.Type)
	}
	if len(cd.Labels) != 2 || cd.Labels[0] != "Jan" || cd.Labels[1] != "Feb" {
		t.Errorf("labels = %v", cd.Labels)
	}
	if len(cd.Series) != 1 || cd.Series[0].Label != "Signups" {
		t.Fatalf("series = %+v", cd.Series)
	}
	if got := cd.Series[0].Values; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("values = %v", got)
	}
	// The result must be chartable without further work.
	if _, err := cd.payload(); err != nil {
		t.Errorf("chart built from rows is not renderable: %v", err)
	}
}

func TestAggRowsChartOfNothingIsRejectedNotPanicking(t *testing.T) {
	cd := AggRows{}.Chart(ChartBar, "empty")
	if _, err := cd.payload(); err == nil {
		t.Error("an empty chart should be reported, not rendered")
	}
}
