package steward

import (
	"reflect"
	"testing"
)

func TestPageWindow(t *testing.T) {
	cases := []struct {
		cur, last int
		want      []int // 0 = ellipsis
	}{
		{1, 1, []int{1}},
		{1, 5, []int{1, 2, 3, 4, 5}},
		{4, 7, []int{1, 2, 3, 4, 5, 6, 7}},
		{1, 8, []int{1, 2, 0, 8}},
		{2, 8, []int{1, 2, 3, 0, 8}},
		{4, 8, []int{1, 2, 3, 4, 5, 0, 8}}, // single-page gaps collapse
		{5, 8, []int{1, 0, 4, 5, 6, 7, 8}}, // both directions
		{8, 8, []int{1, 0, 7, 8}},
		{19, 37, []int{1, 0, 18, 19, 20, 0, 37}},
		{37, 37, []int{1, 0, 36, 37}},
		{36, 37, []int{1, 0, 35, 36, 37}},
		{3, 100, []int{1, 2, 3, 4, 0, 100}},
	}
	for _, c := range cases {
		if got := pageWindow(c.cur, c.last); !reflect.DeepEqual(got, c.want) {
			t.Errorf("pageWindow(%d, %d) = %v, want %v", c.cur, c.last, got, c.want)
		}
	}
}
