package quickdsl

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []Term
	}{
		{"empty", "", nil},
		{"bare term", "hello", []Term{{Op: OpLike, Values: []string{"hello"}}}},
		{"two bare terms", "hello world", []Term{
			{Op: OpLike, Values: []string{"hello"}},
			{Op: OpLike, Values: []string{"world"}},
		}},
		{"quoted phrase", `"hello world"`, []Term{{Op: OpLike, Values: []string{"hello world"}}}},
		{"field equals", "title:hello", []Term{{Field: "title", Op: OpEq, Values: []string{"hello"}}}},
		{"field quoted value", `title:"hello world"`, []Term{{Field: "title", Op: OpEq, Values: []string{"hello world"}}}},
		{"contains", "title:%ell%", []Term{{Field: "title", Op: OpLike, Values: []string{"ell"}}}},
		{"prefix", "title:ell%", []Term{{Field: "title", Op: OpPrefix, Values: []string{"ell"}}}},
		{"gt", "stars:>10", []Term{{Field: "stars", Op: OpGt, Values: []string{"10"}}}},
		{"gte", "stars:>=10", []Term{{Field: "stars", Op: OpGte, Values: []string{"10"}}}},
		{"lt", "stars:<10", []Term{{Field: "stars", Op: OpLt, Values: []string{"10"}}}},
		{"lte", "stars:<=10", []Term{{Field: "stars", Op: OpLte, Values: []string{"10"}}}},
		{"ne", "stars:!=10", []Term{{Field: "stars", Op: OpNe, Values: []string{"10"}}}},
		{"in list", "status:(a,b,c)", []Term{{Field: "status", Op: OpIn, Values: []string{"a", "b", "c"}}}},
		{"in list spaces", "status:(a, b)", []Term{{Field: "status", Op: OpIn, Values: []string{"a", "b"}}}},
		{"between", "stars:[1,9]", []Term{{Field: "stars", Op: OpBetween, Values: []string{"1", "9"}}}},
		{"malformed between falls back to eq", "stars:[1]", []Term{{Field: "stars", Op: OpEq, Values: []string{"[1]"}}}},
		{"null", "deleted:NULL", []Term{{Field: "deleted", Op: OpNull, Values: []string{"true"}}}},
		{"mixed", `draft title:%go% stars:>3`, []Term{
			{Op: OpLike, Values: []string{"draft"}},
			{Field: "title", Op: OpLike, Values: []string{"go"}},
			{Field: "stars", Op: OpGt, Values: []string{"3"}},
		}},
		{"lone colon degrades", ":", []Term{{Op: OpLike, Values: []string{""}}}},
		{"trailing colon degrades", "title:", []Term{{Op: OpLike, Values: []string{"title"}}}},
		{"multiple spaces", "a   b", []Term{
			{Op: OpLike, Values: []string{"a"}},
			{Op: OpLike, Values: []string{"b"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q)\n got: %#v\nwant: %#v", tc.input, got, tc.want)
			}
		})
	}
}
