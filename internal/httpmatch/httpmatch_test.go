package httpmatch

import "testing"

func TestParse(t *testing.T) {
	t.Run("methods apply to all lines", func(t *testing.T) {
		rules := Parse("GET,POST", "/auth/users\n/auth/roles")
		if len(rules) != 2 {
			t.Fatalf("want 2 rules, got %d", len(rules))
		}
		for _, r := range rules {
			if len(r.Methods) != 2 || r.Methods[0] != "GET" || r.Methods[1] != "POST" {
				t.Errorf("methods = %v", r.Methods)
			}
		}
	})
	t.Run("per-line method prefix overrides", func(t *testing.T) {
		rules := Parse("GET", "/a\nPOST,PUT:/b")
		if len(rules[0].Methods) != 1 || rules[0].Methods[0] != "GET" {
			t.Errorf("line 1 methods = %v", rules[0].Methods)
		}
		if len(rules[1].Methods) != 2 || rules[1].Methods[0] != "POST" {
			t.Errorf("line 2 methods = %v", rules[1].Methods)
		}
		if rules[1].Path != "/b" {
			t.Errorf("line 2 path = %q", rules[1].Path)
		}
	})
	t.Run("colon in path is not a method prefix", func(t *testing.T) {
		rules := Parse("", "/redirect/https://example.com")
		if len(rules) != 1 || rules[0].Path != "/redirect/https://example.com" {
			t.Errorf("rules = %+v", rules)
		}
	})
	t.Run("empty and blank lines skipped", func(t *testing.T) {
		if rules := Parse("", "\n\n  \n/x\n"); len(rules) != 1 {
			t.Errorf("want 1 rule, got %d", len(rules))
		}
	})
	t.Run("case-insensitive methods", func(t *testing.T) {
		rules := Parse("get,Post", "/x")
		if rules[0].Methods[0] != "GET" || rules[0].Methods[1] != "POST" {
			t.Errorf("methods = %v", rules[0].Methods)
		}
	})
}

func TestMatches(t *testing.T) {
	cases := []struct {
		name         string
		methods, paths string
		method, path string
		want         bool
	}{
		{"exact", "", "/auth/users", "GET", "/auth/users", true},
		{"exact wrong path", "", "/auth/users", "GET", "/auth/user", false},
		{"trailing slash on request", "", "/auth/users", "GET", "/auth/users/", true},
		{"trailing slash on pattern", "", "/auth/users/", "GET", "/auth/users", true},
		{"missing leading slash on pattern", "", "auth/users", "GET", "/auth/users", true},
		{"suffix glob", "", "/auth/users*", "GET", "/auth/users/3/edit", true},
		{"suffix glob root", "", "/auth/users*", "GET", "/auth/users", true},
		{"glob crosses segments", "", "/auth/*", "GET", "/auth/users/3/edit", true},
		{"mid glob", "", "/posts/*/edit", "GET", "/posts/17/edit", true},
		{"mid glob no match", "", "/posts/*/edit", "GET", "/posts/17", false},
		{"bare star", "", "*", "DELETE", "/anything/at/all", true},
		{"method restricted allow", "GET", "/x", "GET", "/x", true},
		{"method restricted deny", "GET", "/x", "POST", "/x", false},
		{"method csv", "GET,POST", "/x", "POST", "/x", true},
		{"method case-insensitive request", "GET", "/x", "get", "/x", true},
		{"empty methods = any", "", "/x", "DELETE", "/x", true},
		{"per-line override wins", "GET", "POST:/x", "POST", "/x", true},
		{"per-line override excludes base", "GET", "POST:/x", "GET", "/x", false},
		{"query string ignored", "", "/x", "GET", "/x?page=2&sort=-id", true},
		{"multi-line any match", "", "/a\n/b\n/c", "GET", "/b", true},
		{"multi-line no match", "", "/a\n/b", "GET", "/c", false},
		{"regex chars quoted", "", "/x.y+z", "GET", "/x.y+z", true},
		{"regex chars not wild", "", "/x.y", "GET", "/xzy", false},
		{"unicode path", "", "/статьи*", "GET", "/статьи/7", true},
		{"prefix does not match bare", "", "/auth/users/*", "GET", "/auth/users", false},
		{"empty pattern list", "GET", "", "GET", "/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := Parse(tc.methods, tc.paths)
			if got := Matches(rules, tc.method, tc.path); got != tc.want {
				t.Errorf("Matches(%q,%q | %q,%q) = %v, want %v",
					tc.methods, tc.paths, tc.method, tc.path, got, tc.want)
			}
		})
	}
}
