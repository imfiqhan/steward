package main

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/jinzhu/inflection"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// skippedColumns are managed by the model skeleton, never generated.
var skippedColumns = map[string]bool{
	"id": true, "created_at": true, "updated_at": true, "deleted_at": true,
}

// introspectDB reads a live table's columns into field specs.
func introspectDB(driver, dsn, table string) ([]fieldSpec, error) {
	var dial gorm.Dialector
	switch driver {
	case "sqlite", "":
		dial = sqlite.Open(dsn)
	case "mysql":
		dial = mysql.Open(dsn)
	case "postgres":
		dial = postgres.Open(dsn)
	default:
		return nil, fmt.Errorf("unknown --db %q (want sqlite, mysql, postgres)", driver)
	}
	db, err := gorm.Open(dial, &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, fmt.Errorf("connecting: %w", err)
	}
	if !db.Migrator().HasTable(table) {
		return nil, fmt.Errorf("table %q not found", table)
	}
	cols, err := db.Migrator().ColumnTypes(table)
	if err != nil {
		return nil, fmt.Errorf("reading columns of %q: %w", table, err)
	}

	var specs []fieldSpec
	for _, col := range cols {
		name := strings.ToLower(col.Name())
		if skippedColumns[name] {
			continue
		}
		if pk, _ := col.PrimaryKey(); pk {
			continue
		}
		spec := fieldSpec{Name: name, GoName: goName(name)}
		if nullable, ok := col.Nullable(); ok && nullable {
			spec.Nullable = true
		}
		if unique, ok := col.Unique(); ok && unique {
			spec.Unique = true
		}

		full := ""
		if ct, ok := col.ColumnType(); ok {
			full = strings.ToLower(ct)
		}
		dbType := strings.ToLower(col.DatabaseTypeName())
		switch {
		case strings.HasPrefix(full, "enum("):
			spec.Type = "enum"
			spec.Args = strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(full, "enum("), ")"), "'", "")
		case dbType == "bool" || dbType == "boolean" || full == "tinyint(1)":
			spec.Type = "bool"
		case strings.Contains(dbType, "int"):
			spec.Type = "int"
		case dbType == "decimal" || dbType == "numeric" || strings.Contains(dbType, "float") ||
			strings.Contains(dbType, "double") || dbType == "real":
			spec.Type = "decimal"
		case strings.Contains(dbType, "datetime") || strings.Contains(dbType, "timestamp"):
			spec.Type = "datetime"
		case dbType == "date":
			spec.Type = "date"
		case dbType == "time":
			spec.Type = "time"
		case strings.Contains(dbType, "json"):
			spec.Type = "json"
		case strings.Contains(dbType, "blob") || dbType == "bytea":
			continue // binary columns have no sensible form field
		case strings.Contains(full, "varchar") || strings.Contains(full, "character varying") ||
			strings.HasPrefix(full, "char("):
			spec.Type = "string"
		case strings.Contains(dbType, "text"):
			spec.Type = "text"
		default:
			spec.Type = "string"
		}
		applyNameHeuristics(&spec)
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("table %q yielded no generatable columns", table)
	}
	return specs, nil
}

// applyNameHeuristics upgrades string-ish specs based on the column name —
// the inference dcat-admin never had.
func applyNameHeuristics(spec *fieldSpec) {
	name := strings.ToLower(spec.Name)
	if strings.HasSuffix(name, "_id") && spec.Type == "int" {
		spec.Type = "fk"
		spec.Args = inflection.Plural(strings.TrimSuffix(name, "_id"))
		return
	}
	// SQLite's type affinity reports booleans as numeric — recover the
	// intent from conventional names.
	if spec.Type == "int" || spec.Type == "decimal" {
		if strings.HasPrefix(name, "is_") || strings.HasPrefix(name, "has_") ||
			name == "featured" || name == "active" || name == "enabled" ||
			name == "visible" || name == "published" || name == "show" {
			spec.Type = "bool"
			spec.Nullable = false
		}
		return
	}
	if spec.Type != "string" && spec.Type != "text" {
		return
	}
	switch {
	case strings.Contains(name, "email"):
		spec.Type = "email"
	case strings.Contains(name, "password"):
		spec.Type = "password"
	case strings.Contains(name, "url") || strings.Contains(name, "link"):
		spec.Type = "url"
	case strings.Contains(name, "color") || strings.Contains(name, "colour"):
		spec.Type = "color"
	case strings.Contains(name, "image") || strings.Contains(name, "avatar") ||
		strings.Contains(name, "photo") || strings.Contains(name, "cover"):
		spec.Type = "image"
	case name == "body" || name == "content" || strings.Contains(name, "markdown"):
		if spec.Type == "text" {
			spec.Type = "markdown"
		}
	}
}
