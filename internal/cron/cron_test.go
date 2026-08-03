package cron

import (
	"testing"
	"time"
)

func at(min, hour, day int, month time.Month, year int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

func TestParseCronErrors(t *testing.T) {
	for _, spec := range []string{
		"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * 13 *", "x * * * *", "*/0 * * * *", "5-1 * * * *",
	} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) should fail", spec)
		}
	}
}

func TestCronMatches(t *testing.T) {
	cases := []struct {
		spec string
		t    time.Time
		want bool
	}{
		{"* * * * *", at(3, 4, 5, time.June, 2026), true},
		{"30 2 * * *", at(30, 2, 1, time.January, 2026), true},
		{"30 2 * * *", at(31, 2, 1, time.January, 2026), false},
		{"*/15 * * * *", at(0, 9, 1, time.March, 2026), true},
		{"*/15 * * * *", at(45, 9, 1, time.March, 2026), true},
		{"*/15 * * * *", at(20, 9, 1, time.March, 2026), false},
		{"0 9-17 * * *", at(0, 12, 2, time.April, 2026), true},
		{"0 9-17 * * *", at(0, 18, 2, time.April, 2026), false},
		{"0 0 1,15 * *", at(0, 0, 15, time.May, 2026), true},
		{"0 0 1,15 * *", at(0, 0, 16, time.May, 2026), false},
		// 2026-07-26 is a Sunday.
		{"0 8 * * 0", at(0, 8, 26, time.July, 2026), true},
		{"0 8 * * 7", at(0, 8, 26, time.July, 2026), true}, // 7 = Sunday
		{"0 8 * * 1-5", at(0, 8, 26, time.July, 2026), false},
		{"0 8 * * 1-5", at(0, 8, 27, time.July, 2026), true}, // Monday
		// dom OR dow when both restricted (standard cron quirk).
		{"0 0 13 * 5", at(0, 0, 13, time.February, 2026), true}, // Feb 13 2026 is a Friday anyway
		{"0 0 1 * 1", at(0, 0, 1, time.June, 2026), true},       // June 1 2026 is a Monday → dow matches
		{"0 0 2 * 3", at(0, 0, 5, time.June, 2026), false},      // Friday the 5th: neither
		{"0 0 * 12 *", at(0, 0, 25, time.December, 2026), true},
		{"0 0 * 12 *", at(0, 0, 25, time.November, 2026), false},
		{"10-50/10 * * * *", at(30, 1, 1, time.January, 2026), true},
		{"10-50/10 * * * *", at(35, 1, 1, time.January, 2026), false},
	}
	for _, tc := range cases {
		expr, err := Parse(tc.spec)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.spec, err)
		}
		if got := expr.Matches(tc.t); got != tc.want {
			t.Errorf("%q at %s = %v, want %v", tc.spec, tc.t, got, tc.want)
		}
	}
}
