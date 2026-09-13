package steward

import (
	"testing"

	"github.com/imfiqhan/steward/migrate"
)

// A database migrated from nothing holds every migration in one batch, so the
// last batch is all of them: rolling it back takes the panel's own tables with
// it, which is not what "undo the last change" means to anyone typing it.
func TestDownPlan(t *testing.T) {
	applied := func(batch int, names ...string) []migrate.Status {
		out := make([]migrate.Status, 0, len(names))
		for _, n := range names {
			out = append(out, migrate.Status{Name: n, Applied: true, Batch: batch})
		}
		return out
	}

	fresh := applied(1, "0001", "0002", "0003", "0004", "0005", "0006")
	later := append(append([]migrate.Status{}, fresh...), applied(2, "0007", "0008")...)
	pending := append(append([]migrate.Status{}, fresh...), migrate.Status{Name: "0009"})

	for _, tc := range []struct {
		name       string
		sts        []migrate.Status
		steps      int
		wantN      int
		everything bool
	}{
		{"one batch is the whole database", fresh, 0, 6, true},
		{"a second batch is a real step back", later, 0, 2, false},
		{"counting past what is applied stops there", later, 99, 8, true},
		{"a few steps of many", later, 3, 3, false},
		{"one step of one batch", fresh, 1, 1, false},
		{"pending migrations do not count", pending, 0, 6, true},
		{"nothing applied", []migrate.Status{{Name: "0001"}}, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, everything := downPlan(tc.sts, tc.steps)
			if n != tc.wantN || everything != tc.everything {
				t.Errorf("downPlan(steps=%d) = (%d, %v), want (%d, %v)",
					tc.steps, n, everything, tc.wantN, tc.everything)
			}
		})
	}
}
