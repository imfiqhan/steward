package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jinzhu/inflection"
)

// ---- field spec ---------------------------------------------------------------

type fieldSpec struct {
	Name     string // snake_case as given
	GoName   string // CamelCase
	Type     string // base type keyword
	Args     string // enum values / fk table
	Nullable bool
	Unique   bool
	Index    bool
}

// splitTopLevel splits on commas that are not inside parentheses, so
// "a:string,b:enum(x,y),c:int" yields three entries.
func splitTopLevel(spec string) []string {
	var out []string
	depth := 0
	var b strings.Builder
	for _, r := range spec {
		switch {
		case r == '(':
			depth++
			b.WriteRune(r)
		case r == ')':
			depth--
			b.WriteRune(r)
		case r == ',' && depth == 0:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

func parseFields(spec string) ([]fieldSpec, error) {
	var out []fieldSpec
	for _, raw := range splitTopLevel(spec) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) < 2 {
			return nil, fmt.Errorf("field %q: want name:type[:modifier]", raw)
		}
		f := fieldSpec{Name: parts[0], GoName: goName(parts[0])}
		typ := parts[1]
		if i := strings.IndexByte(typ, '('); i >= 0 {
			f.Args = strings.TrimSuffix(typ[i+1:], ")")
			typ = typ[:i]
		}
		f.Type = typ
		for _, mod := range parts[2:] {
			switch mod {
			case "nullable":
				f.Nullable = true
			case "unique":
				f.Unique = true
			case "index":
				f.Index = true
			default:
				return nil, fmt.Errorf("field %q: unknown modifier %q", f.Name, mod)
			}
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no fields parsed from %q", spec)
	}
	return out, nil
}

func goName(snake string) string {
	parts := strings.Split(snake, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if p == "id" {
			b.WriteString("ID")
			continue
		}
		if p == "url" {
			b.WriteString("URL")
			continue
		}
		r := []rune(p)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// goType maps a spec type to the model field's Go type.
func (f fieldSpec) goType() string {
	base := map[string]string{
		"string": "string", "text": "string", "markdown": "string",
		"email": "string", "url": "string", "password": "string",
		"color": "string", "image": "string", "file": "string",
		"json": "string", "enum": "string", "time": "string",
		"int": "int64", "uint": "uint", "float": "float64", "decimal": "float64",
		"bool": "bool",
		"date": "time.Time", "datetime": "time.Time",
		"fk": "uint",
	}[f.Type]
	if base == "" {
		base = "string"
	}
	if f.Nullable && f.Type != "bool" {
		return "*" + base
	}
	return base
}

func (f fieldSpec) gormTag() string {
	var parts []string
	switch f.Type {
	case "string", "email", "url", "password", "color", "image", "file":
		parts = append(parts, "size:255")
	case "text", "markdown":
		parts = append(parts, "type:text")
	case "json":
		parts = append(parts, "type:json")
	case "enum":
		parts = append(parts, "size:32")
	case "time":
		parts = append(parts, "size:8")
	}
	if f.Unique {
		parts = append(parts, "uniqueIndex")
	} else if f.Index || f.Type == "fk" {
		parts = append(parts, "index")
	}
	if len(parts) == 0 {
		return ""
	}
	return " `gorm:\"" + strings.Join(parts, ";") + "\"`"
}

// formLine renders the resource-file form declaration for the field.
func (f fieldSpec) formLine() string {
	var rules []string
	if !f.Nullable && f.Type != "bool" {
		rules = append(rules, "required")
	}
	call := ""
	switch f.Type {
	case "text":
		call = fmt.Sprintf("f.Textarea(%q)", f.GoName)
	case "markdown":
		call = fmt.Sprintf("f.Markdown(%q)", f.GoName)
	case "email":
		call = fmt.Sprintf("f.Email(%q)", f.GoName)
		rules = append(rules, "max:255")
	case "url":
		call = fmt.Sprintf("f.URL(%q)", f.GoName)
	case "password":
		call = fmt.Sprintf("f.Password(%q)", f.GoName)
	case "color":
		call = fmt.Sprintf("f.Color(%q)", f.GoName)
	case "image":
		call = fmt.Sprintf("f.Image(%q)", f.GoName)
		rules = nil
	case "file":
		call = fmt.Sprintf("f.File(%q)", f.GoName)
		rules = nil
	case "int", "uint":
		call = fmt.Sprintf("f.Number(%q)", f.GoName)
	case "float", "decimal":
		call = fmt.Sprintf("f.Decimal(%q)", f.GoName)
	case "bool":
		call = fmt.Sprintf("f.Switch(%q)", f.GoName)
	case "date":
		call = fmt.Sprintf("f.Date(%q)", f.GoName)
	case "datetime":
		call = fmt.Sprintf("f.Datetime(%q)", f.GoName)
	case "time":
		call = fmt.Sprintf("f.Time(%q)", f.GoName)
	case "enum":
		opts := enumOptions(f.Args)
		call = fmt.Sprintf("f.Select(%q).Options(steward.Options{%s})", f.GoName, opts)
		rules = append(rules, "in:"+f.Args)
	case "fk":
		rel := goName(inflection.Singular(f.Args))
		return fmt.Sprintf("\t\tf.Number(%q) // TODO: replace with f.BelongsTo(%q, %q, \"Name\") once the %s model exists",
			f.GoName, f.GoName, rel, rel)
	case "json":
		call = fmt.Sprintf("f.Textarea(%q)", f.GoName)
	default:
		call = fmt.Sprintf("f.Text(%q)", f.GoName)
		rules = append(rules, "max:255")
	}
	if len(rules) > 0 {
		call += fmt.Sprintf(".Rules(%q)", strings.Join(rules, "|"))
	}
	return "\t\t" + call
}

// gridLine renders the resource-file grid column declaration.
func (f fieldSpec) gridLine() string {
	switch f.Type {
	case "bool":
		return fmt.Sprintf("\t\tg.Column(%q).Bool()", f.GoName)
	case "enum":
		return fmt.Sprintf("\t\tg.Column(%q).Badge(map[any]string{%s})", f.GoName, enumBadges(f.Args))
	case "text", "markdown", "json":
		return fmt.Sprintf("\t\tg.Column(%q).Limit(50)", f.GoName)
	case "image":
		return fmt.Sprintf("\t\tg.Column(%q).Image(48, 48)", f.GoName)
	case "int", "uint", "float", "decimal", "date", "datetime":
		return fmt.Sprintf("\t\tg.Column(%q).Sortable()", f.GoName)
	case "url":
		return fmt.Sprintf("\t\tg.Column(%q)", f.GoName)
	default:
		return fmt.Sprintf("\t\tg.Column(%q)", f.GoName)
	}
}

func enumOptions(args string) string {
	var parts []string
	for _, v := range strings.Split(args, ",") {
		v = strings.TrimSpace(v)
		parts = append(parts, fmt.Sprintf("%q: %q", v, goName(v)))
	}
	return strings.Join(parts, ", ")
}

func enumBadges(args string) string {
	colors := []string{"green", "azure", "orange", "purple", "red"}
	var parts []string
	for i, v := range strings.Split(args, ",") {
		v = strings.TrimSpace(v)
		parts = append(parts, fmt.Sprintf("%q: %q", v, colors[i%len(colors)]))
	}
	return strings.Join(parts, ", ")
}

// ---- make:resource -------------------------------------------------------------

func cmdMakeResource(args []string) error {
	var name, fields, dir, dsn, driver, table, structPath string
	fromDB, fromStruct, force := false, false, false
	rest := args
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--fields":
			i++
			fields = rest[i]
		case "--dir":
			i++
			dir = rest[i]
		case "--force":
			force = true
		case "--from-db":
			fromDB = true
		case "--from-struct":
			fromStruct = true
			if i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
				i++
				structPath = rest[i]
			}
		case "--dsn":
			i++
			dsn = rest[i]
		case "--db":
			i++
			driver = rest[i]
		case "--table":
			i++
			table = rest[i]
		default:
			if name == "" && !strings.HasPrefix(rest[i], "-") {
				name = rest[i]
			}
		}
	}
	if name == "" {
		return fmt.Errorf("usage: steward make:resource <Name> --fields \"...\" | --from-db --dsn ... | --from-struct <path>")
	}
	if dir == "" {
		dir = "."
	}
	module, err := readModule(dir)
	if err != nil {
		return err
	}

	var specs []fieldSpec
	switch {
	case fromDB:
		if dsn == "" {
			return fmt.Errorf("--from-db needs --dsn (and optionally --db sqlite|mysql|postgres, --table)")
		}
		if table == "" {
			table = inflection.Plural(toSnake(name))
		}
		specs, err = introspectDB(driver, dsn, table)
	case fromStruct:
		if structPath == "" {
			structPath = filepath.Join(dir, "models")
		}
		specs, err = structFields(structPath, name)
	case fields != "":
		specs, err = parseFields(fields)
	default:
		return fmt.Errorf("pick a source: --fields \"...\", --from-db --dsn ..., or --from-struct <path>")
	}
	if err != nil {
		return err
	}
	// From-struct generation reuses the existing model; skip writing it.
	skipModel := fromStruct

	typeName := goName(strings.ToLower(name[:1]) + name[1:])
	if unicode.IsUpper(rune(name[0])) {
		typeName = name
	}
	snake := toSnake(typeName)
	tableName := inflection.Plural(snake)
	if table != "" {
		tableName = table
	}

	needsTime := false
	var modelFields []string
	for _, f := range specs {
		gt := f.goType()
		if strings.Contains(gt, "time.Time") {
			needsTime = true
		}
		modelFields = append(modelFields, fmt.Sprintf("\t%s %s%s", f.GoName, gt, f.gormTag()))
	}
	timeImport := ""
	if needsTime {
		timeImport = "import \"time\"\n\n"
	} else {
		timeImport = "import \"time\"\n\n" // timestamps always need it
	}

	model := fmt.Sprintf(`package models

%s// %s is managed by the %s admin resource.
type %s struct {
	ID uint `+"`gorm:\"primaryKey\"`"+`
%s
	CreatedAt time.Time
	UpdatedAt time.Time
}
`, timeImport, typeName, tableName, typeName, strings.Join(modelFields, "\n"))

	ts := time.Now().Format("20060102150405")
	migration := fmt.Sprintf(`package migrations

import (
	"gorm.io/gorm"

	"%s/models"
	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "%s_create_%s",
		Up: func(tx *gorm.DB) error {
			return tx.Migrator().AutoMigrate(&models.%s{})
		},
		Down: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable(&models.%s{})
		},
	})
}
`, module, ts, tableName, typeName, typeName)

	var gridLines, formLines []string
	gridLines = append(gridLines, fmt.Sprintf("\t\tg.Column(%q).Sortable().Width(60)", "ID"))
	for _, f := range specs {
		gridLines = append(gridLines, f.gridLine())
		formLines = append(formLines, f.formLine())
	}
	gridLines = append(gridLines, "\t\tg.Column(\"CreatedAt\", \"Created\").Sortable()")

	var search []string
	for _, f := range specs {
		if f.Type == "string" || f.Type == "text" || f.Type == "markdown" {
			search = append(search, fmt.Sprintf("%q", f.GoName))
		}
	}
	quick := ""
	if len(search) > 0 {
		quick = fmt.Sprintf("\t\tg.QuickSearch(%s)\n", strings.Join(search, ", "))
	}

	resource := fmt.Sprintf(`package resources

import (
	steward "github.com/imfiqhan/steward"

	"%s/models"
)

// Register%s wires the %s admin resource.
func Register%s(a *steward.Admin) {
	steward.Register[models.%s](a).
		Title(%q).
		Icon("file").
		Grid(func(g *steward.Grid[models.%s]) {
%s
%s		}).
		Form(func(f *steward.Form[models.%s]) {
%s
		})
}
`, module, typeName, tableName, typeName, typeName,
		inflection.Plural(splitCamelWords(typeName)), typeName,
		strings.Join(gridLines, "\n"), quick, typeName, strings.Join(formLines, "\n"))

	files := map[string]string{
		filepath.Join(dir, "resources", snake+".go"): resource,
	}
	if !skipModel {
		files[filepath.Join(dir, "models", snake+".go")] = model
	}
	// From-db generation targets an existing table; no create migration.
	if !fromDB {
		files[filepath.Join(dir, "migrations", ts+"_create_"+tableName+".go")] = migration
	}
	for path, content := range files {
		if err := writeFile(path, []byte(content), force); err != nil {
			return err
		}
		fmt.Println("created:", path)
	}

	if err := insertRegistration(filepath.Join(dir, "resources", "registry.go"), "Register"+typeName+"(a)"); err != nil {
		fmt.Printf("note: %v\nadd this line to resources.RegisterAll yourself:\n\tRegister%s(a)\n", err, typeName)
	} else {
		fmt.Println("registered in resources/registry.go")
	}
	fmt.Println("next: go run . migrate up && go run . serve")
	return nil
}

// insertRegistration appends the call before the marker comment.
func insertRegistration(registry, call string) error {
	raw, err := os.ReadFile(registry)
	if err != nil {
		return err
	}
	const marker = "// steward:register"
	content := string(raw)
	if !strings.Contains(content, marker) {
		return fmt.Errorf("no %q marker in %s", marker, registry)
	}
	if strings.Contains(content, call) {
		return nil // already registered
	}
	content = strings.Replace(content, marker, call+"\n\t"+marker, 1)
	return os.WriteFile(registry, []byte(content), 0o644)
}

func readModule(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("reading go.mod (is %s a project directory?): %w", dir, err)
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if mod, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(mod), nil
		}
	}
	return "", fmt.Errorf("no module line in %s/go.mod", dir)
}

func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func splitCamelWords(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---- make:migration --------------------------------------------------------------

func cmdMakeMigration(args []string) error {
	var name, dir string
	for i := 0; i < len(args); i++ {
		if args[i] == "--dir" && i+1 < len(args) {
			dir = args[i+1]
			i++
			continue
		}
		if name == "" && !strings.HasPrefix(args[i], "-") {
			name = args[i]
		}
	}
	if name == "" {
		return fmt.Errorf("usage: steward make:migration <name>")
	}
	if dir == "" {
		dir = "."
	}
	ts := time.Now().Format("20060102150405")
	content := fmt.Sprintf(`package migrations

import (
	"gorm.io/gorm"

	"github.com/imfiqhan/steward/migrate"
)

func init() {
	All = append(All, migrate.Migration{
		Name: "%s_%s",
		Up: func(tx *gorm.DB) error {
			// TODO: apply the change
			return nil
		},
		Down: func(tx *gorm.DB) error {
			// TODO: revert the change
			return nil
		},
	})
}
`, ts, toSnake(name))
	path := filepath.Join(dir, "migrations", ts+"_"+toSnake(name)+".go")
	if err := writeFile(path, []byte(content), false); err != nil {
		return err
	}
	fmt.Println("created:", path)
	return nil
}
