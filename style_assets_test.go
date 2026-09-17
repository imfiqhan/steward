//go:build !no_ui

package steward

import (
	"crypto/sha256"
	"io/fs"
	"strings"
	"testing"
)

// A no_ui build embeds no assets at all, so these read nothing and belong
// behind the same tag as the renderer that serves them.

// The build list in tools/assets is a separate module and cannot import this
// one, so the two lists are kept in step by this: a pack named here with no
// bundle built has no stylesheet to serve, and every page under it would load
// a 404 in place of its component layer.
func TestEveryStyleHasABundle(t *testing.T) {
	assets := BuiltinAssets()
	for _, s := range Styles() {
		body, err := fs.ReadFile(assets, s.stylesheet())
		if err != nil {
			t.Errorf("style %q: %v (run `make assets`)", s, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("style %q: bundle is empty", s)
		}
	}
}

// A pack whose bundle matched another's would mean the build wrote the same
// input twice, which looks like success and ships one style under eight names.
//
// Whole bundles, not one declaration: packs may agree on any single rule —
// Luma and Maia give the button the same radius — and still be different
// styles everywhere else.
func TestStyleBundlesDiffer(t *testing.T) {
	assets := BuiltinAssets()
	seen := map[[32]byte]Style{}
	for _, s := range Styles() {
		body, err := fs.ReadFile(assets, s.stylesheet())
		if err != nil {
			t.Fatalf("style %q: %v (run `make assets`)", s, err)
		}
		if btnRule(string(body)) == "" {
			t.Errorf("style %q: bundle carries no .btn rule", s)
		}
		sum := sha256.Sum256(body)
		if other, dup := seen[sum]; dup {
			t.Errorf("style %q and %q have identical bundles; the build wrote one input twice", s, other)
		}
		seen[sum] = s
	}
}

// btnRule returns the minified `.btn{...}` declaration block, or "".
func btnRule(css string) string {
	i := strings.Index(css, ".btn{")
	if i < 0 {
		return ""
	}
	j := strings.Index(css[i:], "}")
	if j < 0 {
		return ""
	}
	return css[i : i+j+1]
}

// The shared bundle must not carry a pack's component visuals: if it did, two
// styles would be in force at once and the selected one would only sometimes
// win.
func TestSharedBundleCarriesNoStylePack(t *testing.T) {
	body, err := fs.ReadFile(BuiltinAssets(), "dist/app.css")
	if err != nil {
		t.Fatalf("shared bundle: %v (run `make assets`)", err)
	}
	if rule := btnRule(string(body)); strings.Contains(rule, "border-radius") {
		t.Errorf("dist/app.css sets a button radius (%q); that belongs to the style pack", rule)
	}
}
