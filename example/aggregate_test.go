package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	steward "github.com/imfiqhan/steward"
)

type Sale struct {
	ID     uint `gorm:"primaryKey"`
	Region string
	Amount float64
	MadeAt time.Time
}

// Unregistered exercises the fallback path: a model with no resource still
// aggregates, through a repository built on demand.
type Unregistered struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type aggResult struct {
	Count      int64           `json:"count"`
	Filtered   int64           `json:"filtered"`
	Sum        float64         `json:"sum"`
	SumNoRows  float64         `json:"sum_no_rows"`
	ByRegion   steward.AggRows `json:"by_region"`
	ByMonth    steward.AggRows `json:"by_month"`
	MonthSums  steward.AggRows `json:"month_sums"`
	TopRegion  steward.AggRows `json:"top_region"`
	Unregister int64           `json:"unregistered"`
}

func newAggTestServer(t *testing.T) (string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:aggtest?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Sale{}, &Unregistered{}); err != nil {
		t.Fatal(err)
	}
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&[]Sale{
		{Region: "EU", Amount: 100, MadeAt: jan},
		{Region: "EU", Amount: 200, MadeAt: jan},
		{Region: "EU", Amount: 50, MadeAt: feb},
		{Region: "US", Amount: 300, MadeAt: feb},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Unregistered{Name: "x"}).Error; err != nil {
		t.Fatal(err)
	}

	app, err := steward.New(steward.Config{
		DB:         db,
		SecretKey:  []byte("aggregate-test-secret-key"),
		AuthExcept: []string{"/sales*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A custom page is the cleanest way to reach the helpers, which take the
	// *Context a request provides — the same footing a dashboard widget is on.
	steward.Register[Sale](app).Page("GET", "_agg", func(c *steward.Context) error {
		var out aggResult
		var err error
		if out.Count, err = steward.Count[Sale](c); err != nil {
			return err
		}
		if out.Filtered, err = steward.Count[Sale](c, steward.Cond{
			Path: "Region", Op: steward.OpEq, Val: "EU",
		}); err != nil {
			return err
		}
		if out.Sum, err = steward.Sum[Sale](c, "Amount"); err != nil {
			return err
		}
		if out.SumNoRows, err = steward.Sum[Sale](c, "Amount", steward.Cond{
			Path: "Region", Op: steward.OpEq, Val: "NOWHERE",
		}); err != nil {
			return err
		}
		if out.ByRegion, err = steward.GroupCount[Sale](c, "Region", 0); err != nil {
			return err
		}
		if out.TopRegion, err = steward.GroupCount[Sale](c, "Region", 1); err != nil {
			return err
		}
		if out.ByMonth, err = steward.PeriodCount[Sale](c, "MadeAt", steward.PeriodMonth); err != nil {
			return err
		}
		if out.MonthSums, err = steward.PeriodSum[Sale](c, "Amount", "MadeAt", steward.PeriodMonth); err != nil {
			return err
		}
		if out.Unregister, err = steward.Count[Unregistered](c); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, out)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app)
	t.Cleanup(srv.Close)
	return srv.URL + "/admin", ""
}

func TestAggregateHelpers(t *testing.T) {
	base, _ := newAggTestServer(t)
	code, body := get(t, &http.Client{}, base+"/sales/_agg", "application/json")
	if code != http.StatusOK {
		t.Fatalf("GET _agg = %d, want 200; body: %s", code, body)
	}
	var r aggResult
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, body)
	}

	if r.Count != 4 {
		t.Errorf("Count = %d, want 4", r.Count)
	}
	if r.Filtered != 3 {
		t.Errorf("filtered Count = %d, want 3", r.Filtered)
	}
	if r.Sum != 650 {
		t.Errorf("Sum = %v, want 650", r.Sum)
	}
	// SUM over no rows is SQL NULL; it must surface as zero, not an error.
	if r.SumNoRows != 0 {
		t.Errorf("Sum with no matching rows = %v, want 0", r.SumNoRows)
	}

	// GroupCount orders by count descending: EU has 3, US has 1.
	if len(r.ByRegion) != 2 {
		t.Fatalf("ByRegion = %+v, want 2 groups", r.ByRegion)
	}
	if r.ByRegion[0].Key != "EU" || r.ByRegion[0].Value != 3 {
		t.Errorf("ByRegion[0] = %+v, want EU/3", r.ByRegion[0])
	}
	if r.ByRegion[1].Key != "US" || r.ByRegion[1].Value != 1 {
		t.Errorf("ByRegion[1] = %+v, want US/1", r.ByRegion[1])
	}
	if len(r.TopRegion) != 1 || r.TopRegion[0].Key != "EU" {
		t.Errorf("limit 1 = %+v, want just EU", r.TopRegion)
	}

	// PeriodCount buckets by month, oldest first, keys sorting chronologically.
	if len(r.ByMonth) != 2 {
		t.Fatalf("ByMonth = %+v, want 2 buckets", r.ByMonth)
	}
	if r.ByMonth[0].Key != "2026-01" || r.ByMonth[0].Value != 2 {
		t.Errorf("ByMonth[0] = %+v, want 2026-01/2", r.ByMonth[0])
	}
	if r.ByMonth[1].Key != "2026-02" || r.ByMonth[1].Value != 2 {
		t.Errorf("ByMonth[1] = %+v, want 2026-02/2", r.ByMonth[1])
	}

	// PeriodSum totals per bucket: Jan 300, Feb 350.
	if len(r.MonthSums) != 2 || r.MonthSums[0].Value != 300 || r.MonthSums[1].Value != 350 {
		t.Errorf("MonthSums = %+v, want 300 then 350", r.MonthSums)
	}

	// A model that is not a registered resource still aggregates.
	if r.Unregister != 1 {
		t.Errorf("unregistered Count = %d, want 1", r.Unregister)
	}
}

// Rows must be usable as a chart with no reshaping in between.
func TestAggregateRowsFeedAChart(t *testing.T) {
	rows := steward.AggRows{{Key: "2026-01", Value: 2}, {Key: "2026-02", Value: 2}}
	cd := rows.Chart(steward.ChartLine, "Sales")
	if len(cd.Labels) != 2 || cd.Series[0].Values[0] != 2 {
		t.Errorf("chart = %+v", cd)
	}
}
