// Package quickdsl parses the grid quick-search box's mini query language,
// ported from dcat-admin:
//
//	foo bar          bare terms — LIKE across the configured search columns
//	"foo bar"        quoted phrase — one bare term with spaces
//	title:hello      field equals value
//	title:%ell%      field contains
//	title:ell%       field starts with
//	stars:>10        comparison: > >= < <= !=
//	status:(a,b,c)   IN list
//	stars:[1,9]      BETWEEN
//	deleted:NULL     IS NULL
//
// Field names are resolved (and typo-checked) by the caller; the parser only
// splits terms and classifies operators.
package quickdsl

import "strings"

// Op mirrors the caller's condition operators.
type Op string

// Operators produced by the parser.
const (
	OpEq      Op = "eq"
	OpNe      Op = "ne"
	OpGt      Op = "gt"
	OpGte     Op = "gte"
	OpLt      Op = "lt"
	OpLte     Op = "lte"
	OpLike    Op = "like"
	OpPrefix  Op = "prefix"
	OpIn      Op = "in"
	OpBetween Op = "between"
	OpNull    Op = "null"
)

// Term is one parsed search term. Field == "" means a bare term for the
// LIKE-across-columns fallback. Values holds 1 value (most ops), 2
// (between), or n (in).
type Term struct {
	Field  string
	Op     Op
	Values []string
}

// Parse splits the query into terms. It never fails: anything unparseable
// degrades to a bare term so user input always searches something.
func Parse(input string) []Term {
	var terms []Term
	for _, tok := range tokenize(input) {
		terms = append(terms, classify(tok))
	}
	return terms
}

// tokenize splits on spaces, honoring double quotes (which may enclose the
// whole token or just the value part of field:"some value") and keeping
// (…)/[…] lists intact so "status:(a, b)" stays one token.
func tokenize(s string) []string {
	var toks []string
	var b strings.Builder
	inQuote := false
	depth := 0
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == '(' || r == '[') && !inQuote:
			depth++
			b.WriteRune(r)
		case (r == ')' || r == ']') && !inQuote:
			if depth > 0 {
				depth--
			}
			b.WriteRune(r)
		case r == ' ' && !inQuote && depth == 0:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return toks
}

func classify(tok string) Term {
	field, val, ok := strings.Cut(tok, ":")
	if !ok || field == "" || val == "" {
		return Term{Op: OpLike, Values: []string{strings.Trim(tok, ":")}}
	}

	switch {
	case val == "NULL":
		return Term{Field: field, Op: OpNull, Values: []string{"true"}}
	case strings.HasPrefix(val, ">="):
		return Term{Field: field, Op: OpGte, Values: []string{val[2:]}}
	case strings.HasPrefix(val, "<="):
		return Term{Field: field, Op: OpLte, Values: []string{val[2:]}}
	case strings.HasPrefix(val, "!="):
		return Term{Field: field, Op: OpNe, Values: []string{val[2:]}}
	case strings.HasPrefix(val, ">"):
		return Term{Field: field, Op: OpGt, Values: []string{val[1:]}}
	case strings.HasPrefix(val, "<"):
		return Term{Field: field, Op: OpLt, Values: []string{val[1:]}}
	case strings.HasPrefix(val, "(") && strings.HasSuffix(val, ")"):
		return Term{Field: field, Op: OpIn, Values: splitList(val[1 : len(val)-1])}
	case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
		parts := splitList(val[1 : len(val)-1])
		if len(parts) == 2 {
			return Term{Field: field, Op: OpBetween, Values: parts}
		}
		return Term{Field: field, Op: OpEq, Values: []string{val}}
	case strings.HasPrefix(val, "%") && strings.HasSuffix(val, "%") && len(val) > 1:
		return Term{Field: field, Op: OpLike, Values: []string{strings.Trim(val, "%")}}
	case strings.HasSuffix(val, "%"):
		return Term{Field: field, Op: OpPrefix, Values: []string{strings.TrimSuffix(val, "%")}}
	default:
		return Term{Field: field, Op: OpEq, Values: []string{val}}
	}
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
