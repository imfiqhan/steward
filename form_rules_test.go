package steward

import (
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	rc := ruleContext{label: "Title"}
	cases := []struct {
		rules, value string
		wantErr      string // substring, "" = valid
	}{
		{"required", "", "required"},
		{"required", "x", ""},
		{"required", "   ", "required"},
		{"max:5", "abcdef", "longer than 5"},
		{"max:5", "abcde", ""},
		{"max:5", "", ""}, // non-required rules pass on empty
		{"min:3", "ab", "at least 3"},
		{"min:3", "abc", ""},
		{"email", "not-an-email", "valid email"},
		{"email", "a@b.co", ""},
		{"url", "not a url", "valid URL"},
		{"url", "https://example.com/x", ""},
		{"integer", "12.5", "whole number"},
		{"integer", "12", ""},
		{"numeric", "abc", "must be a number"},
		{"numeric", "12.5", ""},
		{"in:draft,published", "archived", "one of"},
		{"in:draft,published", "draft", ""},
		{"gte:1", "0", "at least 1"},
		{"gte:1", "2", ""},
		{"lte:10", "11", "not be greater than 10"},
		{"alpha_dash", "a b", "letters, numbers"},
		{"alpha_dash", "a-b_c1", ""},
		{"required|max:5|in:a,b", "", "required"}, // first failure only? all rules run
	}
	for _, tc := range cases {
		errs := validateRules(rc, tc.rules, tc.value)
		if tc.wantErr == "" {
			if len(errs) > 0 {
				t.Errorf("rules %q value %q: unexpected errors %v", tc.rules, tc.value, errs)
			}
			continue
		}
		found := false
		for _, e := range errs {
			if strings.Contains(e, tc.wantErr) {
				found = true
			}
		}
		if !found {
			t.Errorf("rules %q value %q: want error containing %q, got %v", tc.rules, tc.value, tc.wantErr, errs)
		}
	}
}
