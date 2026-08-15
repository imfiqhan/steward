// Package rules implements Steward's declarative field validation: the
// pipe-separated rule strings a form field carries ("required|max:255|
// unique:posts,slug,{id}").
//
// It is separate from the form builder because validating a value needs only
// the value, a label for the message, and a database handle for uniqueness —
// nothing about the request, the resource, or the panel.
package rules

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// Field carries what a validator needs beyond the value itself: a human label
// for the message, and the handle and record id the unique rule needs.
type Field struct {
	DB    *gorm.DB
	Ctx   context.Context
	Label string
	// RecordID is the row being edited, substituted for {id} in a unique rule.
	// Empty when creating.
	RecordID string
}

// Validate checks a raw submitted value against a pipe-separated rule string
// and returns one human-readable message per broken rule. The {id} placeholder inside unique
// rules substitutes the current record id (dcat's {{id}} convention).
func Validate(f Field, rules, value string) []string {
	var errs []string
	for _, rule := range strings.Split(rules, "|") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		name, arg, _ := strings.Cut(rule, ":")
		if msg := applyRule(f, name, arg, value); msg != "" {
			errs = append(errs, msg)
		}
	}
	return errs
}

var alphaDashRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func applyRule(f Field, name, arg, value string) string {
	// Every rule except required passes on empty input (Laravel semantics:
	// pair with required to force presence).
	if value == "" && name != "required" {
		return ""
	}
	switch name {
	case "required":
		if strings.TrimSpace(value) == "" {
			return f.Label + " is required."
		}
	case "max":
		n, _ := strconv.Atoi(arg)
		if len([]rune(value)) > n {
			return fmt.Sprintf("%s may not be longer than %d characters.", f.Label, n)
		}
	case "min":
		n, _ := strconv.Atoi(arg)
		if len([]rune(value)) < n {
			return fmt.Sprintf("%s must be at least %d characters.", f.Label, n)
		}
	case "email":
		if _, err := mail.ParseAddress(value); err != nil {
			return f.Label + " must be a valid email address."
		}
	case "url":
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return f.Label + " must be a valid URL."
		}
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return f.Label + " must be a whole number."
		}
	case "numeric":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return f.Label + " must be a number."
		}
	case "alpha_dash":
		if !alphaDashRe.MatchString(value) {
			return f.Label + " may only contain letters, numbers, dashes, and underscores."
		}
	case "in":
		allowed := strings.Split(arg, ",")
		for _, a := range allowed {
			if value == strings.TrimSpace(a) {
				return ""
			}
		}
		return fmt.Sprintf("%s must be one of: %s.", f.Label, arg)
	case "gte":
		lim, _ := strconv.ParseFloat(arg, 64)
		if v, err := strconv.ParseFloat(value, 64); err != nil || v < lim {
			return fmt.Sprintf("%s must be at least %v.", f.Label, arg)
		}
	case "lte":
		lim, _ := strconv.ParseFloat(arg, 64)
		if v, err := strconv.ParseFloat(value, 64); err != nil || v > lim {
			return fmt.Sprintf("%s may not be greater than %v.", f.Label, arg)
		}
	case "unique":
		return uniqueRule(f, arg, value)
	}
	return ""
}

// uniqueRule implements unique:table,column[,{id}[,idColumn]].
func uniqueRule(f Field, arg, value string) string {
	if f.DB == nil {
		return ""
	}
	parts := strings.Split(arg, ",")
	if len(parts) < 2 {
		return ""
	}
	table, column := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	exceptID := ""
	idColumn := "id"
	if len(parts) > 2 {
		exceptID = strings.TrimSpace(parts[2])
		if exceptID == "{id}" || exceptID == "{{id}}" {
			exceptID = f.RecordID
		}
	}
	if len(parts) > 3 {
		idColumn = strings.TrimSpace(parts[3])
	}
	q := f.DB.WithContext(f.Ctx).Table(table).Where(column+" = ?", value)
	if exceptID != "" {
		q = q.Where(idColumn+" <> ?", exceptID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return "could not verify uniqueness of " + f.Label + "."
	}
	if count > 0 {
		return f.Label + " has already been taken."
	}
	return ""
}

// known lists every rule applyRule answers to. It is written out rather than
// derived, because the switch it mirrors has no default case: an unknown rule
// there is silently skipped, so "requried" removes a required check without
// saying anything.
var known = map[string]bool{
	"required": true, "email": true, "url": true, "numeric": true,
	"integer": true, "alpha_dash": true, "min": true, "max": true,
	"gte": true, "lte": true, "in": true, "unique": true,
}

// Unknown returns the rule names in a spec that Validate would ignore.
func Unknown(spec string) []string {
	var out []string
	for _, rule := range strings.Split(spec, "|") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		name, _, _ := strings.Cut(rule, ":")
		if name = strings.TrimSpace(name); name != "" && !known[name] {
			out = append(out, name)
		}
	}
	return out
}

// Names lists the known rules, sorted, for an error message that says what was
// allowed rather than only what was not.
func Names() []string {
	out := make([]string, 0, len(known))
	for name := range known {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
