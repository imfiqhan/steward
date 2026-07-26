package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
)

// structFields builds field specs from a Go struct declaration (static
// go/ast parse — the source file does not need to compile against steward).
func structFields(path, typeName string) ([]fieldSpec, error) {
	fset := token.NewFileSet()
	var files []*ast.File

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		pkgs, err := parser.ParseDir(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, pkg := range pkgs {
			for _, f := range pkg.Files {
				files = append(files, f)
			}
		}
	} else {
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	var st *ast.StructType
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != typeName {
				return true
			}
			if s, ok := ts.Type.(*ast.StructType); ok {
				st = s
			}
			return false
		})
		if st != nil {
			break
		}
	}
	if st == nil {
		return nil, fmt.Errorf("struct %q not found in %s", typeName, path)
	}

	var specs []fieldSpec
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 || !field.Names[0].IsExported() {
			continue // embedded or unexported
		}
		goName := field.Names[0].Name
		if goName == "ID" || goName == "CreatedAt" || goName == "UpdatedAt" || goName == "DeletedAt" {
			continue
		}
		spec, ok := specFromASTType(field.Type)
		if !ok {
			continue // relations, slices, maps — not form material
		}
		spec.Name = toSnake(goName)
		spec.GoName = goName

		if field.Tag != nil {
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`")).Get("gorm")
			if strings.Contains(tag, "type:text") || strings.Contains(tag, "type:longtext") {
				spec.Type = "text"
			}
			if strings.Contains(tag, "type:json") {
				spec.Type = "json"
			}
			if strings.Contains(tag, "uniqueIndex") {
				spec.Unique = true
			} else if strings.Contains(tag, "index") {
				spec.Index = true
			}
			if strings.Contains(tag, "-") && strings.Contains(tag, `gorm:"-"`) {
				continue
			}
		}
		applyNameHeuristics(&spec)
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("struct %q yielded no generatable fields", typeName)
	}
	return specs, nil
}

// specFromASTType maps a field's Go type to a spec type; ok=false skips it.
func specFromASTType(expr ast.Expr) (fieldSpec, bool) {
	nullable := false
	if star, ok := expr.(*ast.StarExpr); ok {
		nullable = true
		expr = star.X
	}
	var name string
	switch t := expr.(type) {
	case *ast.Ident:
		name = t.Name
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			name = pkg.Name + "." + t.Sel.Name
		}
	default:
		return fieldSpec{}, false
	}

	spec := fieldSpec{Nullable: nullable}
	switch name {
	case "string":
		spec.Type = "string"
	case "bool":
		spec.Type = "bool"
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		spec.Type = "int"
	case "float32", "float64":
		spec.Type = "decimal"
	case "time.Time":
		spec.Type = "datetime"
	default:
		return fieldSpec{}, false
	}
	return spec, true
}
