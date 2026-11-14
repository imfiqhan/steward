package steward

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The Indonesian catalog has to keep up with the English it translates. These
// collect every text the panel shows from where it is written — templates, Go
// calls, validation rules, the client script, the built-in resources — and
// require an entry for each, so a new sentence without a translation fails here
// rather than showing up in English on an Indonesian panel.

var templateKeyRe = regexp.MustCompile(`\{\{-?\s*t\s+("(?:[^"\\]|\\.)*")`)

func templateKeys(t *testing.T) []string {
	t.Helper()
	var out []string
	files, _ := filepath.Glob("templates/*/*.html")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range templateKeyRe.FindAllStringSubmatch(string(raw), -1) {
			s, err := strconv.Unquote(m[1])
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			out = append(out, s)
		}
	}
	return out
}

// goKeyArgs maps a called function or method name to the argument positions
// whose string literals are shown to a reader.
var goKeyArgs = map[string][]int{
	"T": {0}, "Success": {0}, "Error": {0}, "Warning": {0}, "Info": {0},
	"renderError": {2}, "Flash": {1}, "msg": {0},
	"renderLogin": {1}, "ssoFail": {1}, "inviteError": {0}, "fail": {0},
	"twoFactorReject": {1}, "twoFactorEnrolPage": {2}, "renderSignup": {3},
	"statusHTML": {1, 2},
}

// builderLabelArgs are the builder calls of the built-in resources whose
// arguments after the path are labels, and builderTextCalls those whose every
// string argument is text.
var (
	builderLabelArgs = map[string]bool{
		"Column": true, "Field": true, "Text": true, "Textarea": true, "Email": true,
		"Password": true, "Select": true, "MultiSelect": true, "Switch": true,
		"Number": true, "Display": true, "Equal": true, "Like": true,
		"FieldFunc": true, "ColumnFunc": true, "BelongsTo": true, "Datetime": true,
	}
	builderTextCalls = map[string]bool{
		"Title": true, "Group": true, "Help": true, "Placeholder": true,
		"Confirm": true, "Label": true,
	}
	builtinFiles = []string{
		"rbac.go", "menu_sync.go", "queue.go", "tenancy_resources.go",
		"tenancy_invite.go", "export_job.go", "notification.go",
	}
)

func goKeys(t *testing.T, files []string, builtins bool) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	add := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		s, err := strconv.Unquote(lit.Value)
		if err == nil && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := ""
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
					if id, ok := fn.X.(*ast.Ident); ok && (id.Name == "fmt" || id.Name == "errors" || id.Name == "strings") {
						return true
					}
					// a.log.Error(…) and friends write to the log, not the page.
					if sel, ok := fn.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "log" {
						return true
					}
				}
				for _, i := range goKeyArgs[name] {
					if i < len(x.Args) {
						add(x.Args[i])
					}
				}
				if !builtins {
					return true
				}
				switch {
				case builderTextCalls[name]:
					for _, a := range x.Args {
						add(a)
					}
				case builderLabelArgs[name] && len(x.Args) > 0:
					if len(x.Args) == 1 {
						// The label is derived from the path.
						if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if p, err := strconv.Unquote(lit.Value); err == nil {
								out = append(out, splitCamel(p))
							}
						}
					}
					for _, a := range x.Args[1:] {
						add(a)
					}
				case name == "NewAction" && len(x.Args) > 1:
					add(x.Args[1])
				}
			case *ast.KeyValueExpr:
				// Label: "…" in a view model the template translates.
				if k, ok := x.Key.(*ast.Ident); ok && k.Name == "Label" {
					add(x.Value)
				}
				// "Error": "…" in template data, and Options values.
				if k, ok := x.Key.(*ast.BasicLit); ok && k.Kind == token.STRING {
					if ks, _ := strconv.Unquote(k.Value); ks == "Error" || builtins {
						add(x.Value)
					}
				}
			case *ast.AssignStmt:
				// vm.Error = "…" and errs["field"] = "…".
				for i, l := range x.Lhs {
					target := false
					switch lx := l.(type) {
					case *ast.SelectorExpr:
						target = lx.Sel.Name == "Error"
					case *ast.IndexExpr:
						if id, ok := lx.X.(*ast.Ident); ok && id.Name == "errs" {
							target = true
						}
					}
					if target && i < len(x.Rhs) {
						add(x.Rhs[i])
					}
				}
			}
			return true
		})
	}
	return out
}

var trCallRe = regexp.MustCompile(`\btr\(("(?:[^"\\]|\\.)*")`)

func scriptKeys(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("frontend/src/app.js")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range trCallRe.FindAllStringSubmatch(string(raw), -1) {
		s, err := strconv.Unquote(m[1])
		if err != nil {
			t.Fatalf("app.js: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func allKeys(t *testing.T) []string {
	t.Helper()
	var goFiles []string
	all, _ := filepath.Glob("*.go")
	for _, f := range all {
		// form_embed.go's fail() builds Verify errors, which are for developers.
		if !strings.HasSuffix(f, "_test.go") && f != "locale_id.go" && f != "form_embed.go" {
			goFiles = append(goFiles, f)
		}
	}
	keys := templateKeys(t)
	keys = append(keys, goKeys(t, goFiles, false)...)
	keys = append(keys, goKeys(t, builtinFiles, true)...)
	keys = append(keys, goKeys(t, []string{"internal/rules/rules.go"}, false)...)
	keys = append(keys, clientTexts...)
	slices.Sort(keys)
	return slices.Compact(keys)
}

// untranslated are texts that read the same in Indonesian, or are not prose.
var untranslated = map[string]bool{
	"Email": true, "Slug": true, "URI": true, "IP": true, "ID": true,
	"Ctrl": true, "Steward": true, "%s: %s": true, "xxxx-xxxx-xxxx-xxxx": true,
	"you@company.com": true, "you@example.com": true, "/posts": true,
	"GET,POST — empty allows every method": true, "/posts*\nGET:/authors": true,
	"Icon": true, "Menu": true, "Admin": true, "Status": true, "Payload": true,
	"max": true, "min": true, "%v": true, "key": true,
	"users": true, "user_id": true, "steward": true,
	// Pagination link kinds the template switches on, not shown as text.
	"gap": true, "next": true, "prev": true,
}

func TestIndonesianCatalogCoversEveryText(t *testing.T) {
	var missing []string
	for _, k := range allKeys(t) {
		if untranslated[k] {
			continue
		}
		if localeID[k] == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var b strings.Builder
		for _, m := range missing {
			b.WriteString(strconv.Quote(m) + "\n")
		}
		t.Errorf("%d texts have no Indonesian translation:\n%s", len(missing), b.String())
	}
}

// The script's tr() calls and clientTexts are the same list: a text the
// script asks for that the layout never sends stays English.
func TestClientTextsMatchTheScript(t *testing.T) {
	want := scriptKeys(t)
	want = append(want, "month", "year") // passed to tr through a variable
	slices.Sort(want)
	want = slices.Compact(want)
	got := slices.Clone(clientTexts)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("clientTexts and app.js tr() calls differ:\n got %q\nwant %q", got, want)
	}
}

// A translation keeps the verbs its key has, in order, or Sprintf garbles it.
func TestTranslationsKeepTheirVerbs(t *testing.T) {
	verbs := regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)
	for k, v := range localeID {
		if !slices.Equal(verbs.FindAllString(k, -1), verbs.FindAllString(v, -1)) {
			t.Errorf("%q → %q changes its format verbs", k, v)
		}
	}
}

func TestPanelTranslates(t *testing.T) {
	a := &Panel{cfg: Config{Locale: LocaleIndonesian, Translations: map[string]map[string]string{
		LocaleIndonesian: {"Save changes": "Simpan sekarang"},
		"jv":             {"Save changes": "Simpen"},
	}}}
	if got := a.T("Save changes"); got != "Simpan sekarang" {
		t.Errorf("an override should win over the shipped catalog, got %q", got)
	}
	if got := a.T("%s created.", "Pos"); got != "Pos berhasil dibuat." {
		t.Errorf("formatted = %q", got)
	}
	if got := a.T("No such text anywhere."); got != "No such text anywhere." {
		t.Errorf("a missing key should fall back to English, got %q", got)
	}
	a.cfg.Locale = "jv"
	if !a.knownLocale("jv") || a.T("Save changes") != "Simpen" {
		t.Error("a locale supplied through Translations should be usable")
	}
	en := &Panel{}
	if en.T("Save changes") != "Save changes" || en.clientCatalogJS() != "" {
		t.Error("an English panel should translate nothing and embed nothing")
	}
}
