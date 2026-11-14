package steward

import (
	"encoding/json"
	"fmt"
	"html/template"
	"slices"
	"sort"
	"strings"
)

// Locales the framework ships a catalog for.
const (
	LocaleEnglish    = "en"
	LocaleIndonesian = "id"
)

// catalogs maps a locale to its translations, keyed by the English text. English
// needs no entries: a key is its own translation.
var catalogs = map[string]map[string]string{
	LocaleIndonesian: localeID,
}

// locale is the panel's configured language, English when unset.
func (a *Panel) locale() string {
	if a.cfg.Locale == "" {
		return LocaleEnglish
	}
	return a.cfg.Locale
}

// knownLocale reports whether a locale has any text to give: one the framework
// ships, English, or one Config.Translations supplies.
func (a *Panel) knownLocale(l string) bool {
	if l == LocaleEnglish {
		return true
	}
	if _, ok := catalogs[l]; ok {
		return true
	}
	_, ok := a.cfg.Translations[l]
	return ok
}

// localeNames lists the locales Verify accepts, for an error that says what was
// allowed.
func (a *Panel) localeNames() []string {
	out := []string{LocaleEnglish}
	for l := range catalogs {
		out = append(out, l)
	}
	for l := range a.cfg.Translations {
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// lookup returns the translation of an English text in the panel's locale:
// Config.Translations first, then the shipped catalog, then the text itself.
func (a *Panel) lookup(msg string) string {
	l := a.locale()
	if s, ok := a.cfg.Translations[l][msg]; ok && s != "" {
		return s
	}
	if s, ok := catalogs[l][msg]; ok && s != "" {
		return s
	}
	return msg
}

// T translates an English text into the panel's locale and, given arguments,
// formats it with fmt.Sprintf. The English text is the key, so a text with no
// translation comes back as written:
//
//	app.T("%d posts published.", n)
func (a *Panel) T(msg string, args ...any) string {
	s := a.lookup(msg)
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// T translates for the panel this request belongs to; see Panel.T.
func (c *Context) T(msg string, args ...any) string {
	if c == nil || c.Panel == nil {
		if len(args) == 0 {
			return msg
		}
		return fmt.Sprintf(msg, args...)
	}
	return c.Panel.T(msg, args...)
}

// lookup translates a text held in a variable, which T's format handling must
// not see.
func (c *Context) lookup(msg string) string {
	if c == nil || c.Panel == nil {
		return msg
	}
	return c.Panel.lookup(msg)
}

// translateEnvelope translates an envelope's fixed texts on the way out, so a
// message written as a constant — Error("Record not found.") — needs no
// translation call where it is built. A message formatted with values has to
// be translated where it is formatted.
func (a *Panel) translateEnvelope(e *Envelope) {
	if e == nil || e.Data == nil {
		return
	}
	e.Data.Message = a.lookup(e.Data.Message)
	e.Data.Detail = a.lookup(e.Data.Detail)
}

// clientTexts are the strings frontend/src/app.js shows on its own: toasts,
// dialog buttons and labels it builds. They reach the script translated, in a
// JSON block the layout renders; the script falls back to the English it was
// written with.
var clientTexts = []string{
	"Nothing selected.",
	"Export failed — check your connection and retry.",
	"Request failed — check your connection and retry.",
	"Are you sure?",
	"Confirm",
	"Delete this record?",
	"This cannot be undone.",
	"Delete",
	"Delete %s selected record(s)?",
	"Action failed — check your connection and retry.",
	"Reorder failed — reload and retry.",
	"Save failed — check your connection and retry.",
	"Please fix the highlighted fields.",
	"This file is missing from storage.",
	"Upload failed.",
	"Uploaded.",
	"Upload failed — check your connection and retry.",
	"Remove %s",
	"This field holds at most %s files.",
	"Only %s more file(s) fit here; the rest were skipped.",
	"Bold (⌘B)",
	"Italic (⌘I)",
	"Underline (⌘U)",
	"Strikethrough",
	"Heading 2",
	"Heading 3",
	"Paragraph",
	"Bulleted list",
	"Numbered list",
	"Quote",
	"Insert link",
	"Remove link",
	"Insert image",
	"Clear formatting",
	"Formatting",
	"Link URL",
	"Write",
	"Preview",
	"Preview failed.",
	"No matches.",
	"Some sections could not be searched — the panel's log says why.",
	"Search timed out before every section answered.",
	"This file could not be loaded.",
	"This file cannot be shown here. Open it in a new tab.",
	"Copied to clipboard.",
	"Could not copy to the clipboard.",
	"Search %s icons…",
	"Search icons",
	"No icon",
	"No icon matches “%s”.",
	"Too many matches — keep typing.",
	"Choose a date",
	"Previous",
	"Change %s",
	"Next",
	"Today",
	"Open the calendar",
	"month",
	"year",
}

// clientCatalog is clientTexts in the panel's locale, for the layout to embed.
// Empty for English, where the script already has the text.
func (a *Panel) clientCatalog() map[string]string {
	if a.locale() == LocaleEnglish && len(a.cfg.Translations[LocaleEnglish]) == 0 {
		return nil
	}
	out := make(map[string]string, len(clientTexts))
	for _, s := range clientTexts {
		if t := a.lookup(s); t != s {
			out[s] = t
		}
	}
	return out
}

// clientCatalogJS is clientCatalog as the JSON the layout embeds.
func (a *Panel) clientCatalogJS() template.JS {
	cat := a.clientCatalog()
	if len(cat) == 0 {
		return ""
	}
	raw, err := json.Marshal(cat)
	if err != nil {
		return ""
	}
	return template.JS(raw) //nolint:gosec // json.Marshal escapes <, > and & for script context
}

// htmlLang is the value of the layout's lang attribute.
func (a *Panel) htmlLang() string {
	return strings.ReplaceAll(a.locale(), "_", "-")
}
