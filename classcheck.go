package steward

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// The stylesheet is compiled from the class names the framework's own templates
// use, so a utility an overlay template reaches for is in the bundle only by
// coincidence. Nothing complains: the class is applied, no rule matches it, and
// the page is subtly wrong — a row that does not centre, a gap that is not
// there. The page looks right in the markup and wrong on screen.
//
// In development the overlay is read for class names that the stylesheet has no
// rule for, and each is named once.

var (
	classAttrRe = regexp.MustCompile(`class="([^"]*)"`)
	actionRe    = regexp.MustCompile(`\{\{[^}]*\}\}`)
	quotedRe    = regexp.MustCompile(`"[^"]*"`)
	// A class the served stylesheet defines, in the form Tailwind escapes it
	// into: .sm\:grid-cols-2, .w-1\/2, .p-2\.5.
	ruleRe = regexp.MustCompile(`\.((?:\\.|[A-Za-z0-9_-])+)`)
)

// unknownTemplateClasses reports class names used by the overlay that the
// stylesheet has no rule for, as "template.html: class-one, class-two".
func unknownTemplateClasses(overlay fs.FS, css string) []string {
	if overlay == nil {
		return nil
	}
	defined := definedClasses(css)
	if len(defined) == 0 {
		return nil // no stylesheet to check against; say nothing rather than guess
	}

	var out []string
	_ = fs.WalkDir(overlay, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		body, err := fs.ReadFile(overlay, p)
		if err != nil {
			return nil
		}
		missing := map[string]bool{}
		// An action inside the attribute may quote a string of its own —
		// class="{{if eq .Theme "dark"}}dark{{end}}" — which would end the
		// attribute early for the match below. The quotes go first; the
		// branches keep the class names they carry.
		text := actionRe.ReplaceAllStringFunc(string(body), func(a string) string {
			return quotedRe.ReplaceAllString(a, " ")
		})
		for _, m := range classAttrRe.FindAllStringSubmatch(text, -1) {
			for _, name := range strings.Fields(actionRe.ReplaceAllString(m[1], " ")) {
				if skipClassName(name) || defined[name] {
					continue
				}
				missing[name] = true
			}
		}
		if len(missing) == 0 {
			return nil
		}
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, p+": "+strings.Join(names, ", "))
		return nil
	})
	sort.Strings(out)
	return out
}

// definedClasses is every class the stylesheet has a rule for, unescaped back
// into the form a template writes.
func definedClasses(css string) map[string]bool {
	out := make(map[string]bool, 4096)
	for _, m := range ruleRe.FindAllStringSubmatch(css, -1) {
		out[strings.ReplaceAll(m[1], `\`, "")] = true
	}
	return out
}

// skipClassName covers what is not a class to check: what a template action
// left behind, and the state attributes a component library toggles.
func skipClassName(name string) bool {
	if name == "" || strings.ContainsAny(name, `{}"'()$`) {
		return true
	}
	// What a template action leaves behind once its delimiters are gone.
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "if", "else", "end", "with", "range", "template", "block", "define",
		"eq", "ne", "lt", "le", "gt", "ge", "and", "or", "not", "printf", "index":
		return true
	}
	// Tailwind writes arbitrary values as they are given, and an overlay may
	// carry any of them; the stylesheet cannot be asked about one it was never
	// built with, and saying so every time would be noise.
	return strings.ContainsAny(name, "[]")
}

// warnUnknownClasses logs what the overlay uses and the stylesheet lacks. It
// runs in development only: the answer cannot change at runtime, and the cost
// is reading every overlay template.
func (a *Admin) warnUnknownClasses() {
	if !a.cfg.Dev || a.cfg.TemplatesFS == nil {
		return
	}
	css, err := readLayered(a.renderer.assetLayers, "dist/app.css")
	if err != nil {
		return
	}
	// A panel's own rules are part of the answer: a class defined in ThemeCSS
	// is as real as one the bundle carries.
	for _, line := range unknownTemplateClasses(a.cfg.TemplatesFS, string(css)+a.cfg.ThemeCSS) {
		a.log.Warn("steward: template uses a class the stylesheet has no rule for", "where", line)
	}
}
