// Package httpmatch ports dcat-admin's permission matcher: a permission row
// carries a comma-separated HTTP method list and a newline/comma-separated
// list of path patterns with * globs; each path line may override the method
// list with a "GET,POST:" prefix. Matching follows Laravel's Str::is: * can
// cross path segments and patterns are anchored at both ends.
package httpmatch

import (
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Rule is one path pattern with the methods allowed on it (empty = any).
type Rule struct {
	Methods []string
	Path    string
}

// Parse expands a permission row into rules. methodCSV applies to every
// line that has no "METHOD[,METHOD]:" prefix of its own. Empty lines are
// skipped; a bare "*" path matches everything.
func Parse(methodCSV, pathLines string) []Rule {
	baseMethods := splitMethods(methodCSV)
	var rules []Rule
	for line := range strings.SplitSeq(pathLines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		methods := baseMethods
		// A method prefix looks like "GET:" or "GET,POST:/path". Only treat
		// the colon as a prefix delimiter when everything before it parses
		// as method names (guards against "https://…" style paths).
		if head, tail, ok := strings.Cut(line, ":"); ok {
			if ms := splitMethods(head); len(ms) > 0 && allMethods(ms) {
				methods = ms
				line = strings.TrimSpace(tail)
			}
		}
		if line == "" {
			continue
		}
		rules = append(rules, Rule{Methods: methods, Path: normalize(line)})
	}
	return rules
}

var methodNames = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

func splitMethods(csv string) []string {
	var out []string
	for m := range strings.SplitSeq(csv, ",") {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func allMethods(ms []string) bool {
	for _, m := range ms {
		if !methodNames[m] {
			return false
		}
	}
	return true
}

// normalize trims trailing slashes and guarantees one leading slash (the
// bare "*" wildcard passes through untouched).
func normalize(p string) string {
	if p == "*" {
		return "*"
	}
	p = "/" + strings.Trim(p, "/")
	return p
}

var (
	reCacheMu sync.RWMutex
	reCache   = map[string]*regexp.Regexp{}
)

// compile turns a glob pattern into an anchored regexp (Laravel Str::is
// semantics: * matches anything including slashes).
func compile(pattern string) *regexp.Regexp {
	reCacheMu.RLock()
	re, ok := reCache[pattern]
	reCacheMu.RUnlock()
	if ok {
		return re
	}
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	re = regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	reCacheMu.Lock()
	reCache[pattern] = re
	reCacheMu.Unlock()
	return re
}

// Matches reports whether any rule covers the method+path. The path should
// be relative to the admin prefix; query strings are ignored; a trailing
// slash never changes the outcome.
func Matches(rules []Rule, method, path string) bool {
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	path = normalize(path)
	method = strings.ToUpper(method)
	for _, r := range rules {
		if len(r.Methods) > 0 && !slices.Contains(r.Methods, method) {
			continue
		}
		if r.Path == "*" || compile(r.Path).MatchString(path) {
			return true
		}
	}
	return false
}
