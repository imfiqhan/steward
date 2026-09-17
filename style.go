package steward

import (
	"fmt"
	"sort"
	"strings"
)

// Style names the Basecoat style pack a panel is drawn with.
//
// A style pack owns component visuals: radius, shadows, focus rings, spacing,
// variant treatment and state treatment. It does not own colour — colour is
// design tokens, and Config.ThemeCSS redefines those over whichever pack is
// selected. The two are separate layers, so a palette written once holds
// across every style here.
//
// Each pack is a complete, standalone bundle. Two cannot be combined: the
// second would not sit on top of the first, it would fight it rule for rule.
type Style string

// The packs Basecoat ships. Vega is the default and is what every panel
// built before this option existed was drawn with.
const (
	StyleVega Style = "vega"
	StyleNova Style = "nova"
	StyleMaia Style = "maia"
	StyleLyra Style = "lyra"
	StyleMira Style = "mira"
	StyleLuma Style = "luma"
	StyleSera Style = "sera"
	StyleRhea Style = "rhea"
)

// DefaultStyle is what an unset Config.Style resolves to.
const DefaultStyle = StyleVega

// styles is every pack, and the single list the build, the validator and the
// asset embed all read. A pack added to the vendor tree without an entry here
// is built but unreachable.
var styles = []Style{
	StyleVega,
	StyleNova,
	StyleMaia,
	StyleLyra,
	StyleMira,
	StyleLuma,
	StyleSera,
	StyleRhea,
}

// Styles returns every style pack a panel can be configured with.
func Styles() []Style {
	out := make([]Style, len(styles))
	copy(out, styles)
	return out
}

// String reports the pack's name, which is also its value in Config.Style.
func (s Style) String() string { return string(s) }

// valid reports whether s is a pack Steward ships a bundle for.
func (s Style) valid() bool {
	for _, known := range styles {
		if s == known {
			return true
		}
	}
	return false
}

// stylesheet is the pack's bundle inside the asset tree. It carries only the
// component layer; tokens, utilities and Steward's own rules are in the shared
// bundle that loads before it.
func (s Style) stylesheet() string { return "dist/style-" + string(s) + ".css" }

// resolveStyle applies the default and rejects a name with no bundle. An
// unknown pack is refused rather than defaulted: a panel asking for a style it
// does not get is a panel that looks wrong everywhere, and quietly serving a
// different one hides that.
func resolveStyle(s Style) (Style, error) {
	if s == "" {
		return DefaultStyle, nil
	}
	if s.valid() {
		return s, nil
	}
	names := make([]string, 0, len(styles))
	for _, known := range styles {
		names = append(names, string(known))
	}
	sort.Strings(names)
	return "", fmt.Errorf("steward: Config.Style %q is not a Basecoat style pack; pick one of %s",
		s, strings.Join(names, ", "))
}
