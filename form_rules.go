package steward

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// ruleContext carries what validators need beyond the value itself.
type ruleContext struct {
	db       *gorm.DB
	ctx      context.Context
	label    string
	recordID string // current record's id when editing ("" on create)
}

// validateRules checks a raw submitted value against a pipe-separated rule
// string and returns human messages. The {id} placeholder inside unique
// rules substitutes the current record id (dcat's {{id}} convention).
func validateRules(rc ruleContext, rules, value string) []string {
	var errs []string
	for _, rule := range strings.Split(rules, "|") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		name, arg, _ := strings.Cut(rule, ":")
		if msg := applyRule(rc, name, arg, value); msg != "" {
			errs = append(errs, msg)
		}
	}
	return errs
}

var alphaDashRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func applyRule(rc ruleContext, name, arg, value string) string {
	// Every rule except required passes on empty input (Laravel semantics:
	// pair with required to force presence).
	if value == "" && name != "required" {
		return ""
	}
	switch name {
	case "required":
		if strings.TrimSpace(value) == "" {
			return rc.label + " is required."
		}
	case "max":
		n, _ := strconv.Atoi(arg)
		if len([]rune(value)) > n {
			return fmt.Sprintf("%s may not be longer than %d characters.", rc.label, n)
		}
	case "min":
		n, _ := strconv.Atoi(arg)
		if len([]rune(value)) < n {
			return fmt.Sprintf("%s must be at least %d characters.", rc.label, n)
		}
	case "email":
		if _, err := mail.ParseAddress(value); err != nil {
			return rc.label + " must be a valid email address."
		}
	case "url":
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return rc.label + " must be a valid URL."
		}
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return rc.label + " must be a whole number."
		}
	case "numeric":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return rc.label + " must be a number."
		}
	case "alpha_dash":
		if !alphaDashRe.MatchString(value) {
			return rc.label + " may only contain letters, numbers, dashes, and underscores."
		}
	case "in":
		allowed := strings.Split(arg, ",")
		for _, a := range allowed {
			if value == strings.TrimSpace(a) {
				return ""
			}
		}
		return fmt.Sprintf("%s must be one of: %s.", rc.label, arg)
	case "gte":
		lim, _ := strconv.ParseFloat(arg, 64)
		if v, err := strconv.ParseFloat(value, 64); err != nil || v < lim {
			return fmt.Sprintf("%s must be at least %v.", rc.label, arg)
		}
	case "lte":
		lim, _ := strconv.ParseFloat(arg, 64)
		if v, err := strconv.ParseFloat(value, 64); err != nil || v > lim {
			return fmt.Sprintf("%s may not be greater than %v.", rc.label, arg)
		}
	case "unique":
		return uniqueRule(rc, arg, value)
	}
	return ""
}

// uniqueRule implements unique:table,column[,{id}[,idColumn]].
func uniqueRule(rc ruleContext, arg, value string) string {
	if rc.db == nil {
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
			exceptID = rc.recordID
		}
	}
	if len(parts) > 3 {
		idColumn = strings.TrimSpace(parts[3])
	}
	q := rc.db.WithContext(rc.ctx).Table(table).Where(column+" = ?", value)
	if exceptID != "" {
		q = q.Where(idColumn+" <> ?", exceptID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return "could not verify uniqueness of " + rc.label + "."
	}
	if count > 0 {
		return rc.label + " has already been taken."
	}
	return ""
}
