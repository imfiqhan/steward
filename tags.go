package steward

import (
	"encoding/json"
	"html/template"
	"strings"
)

// maxTagValues bounds one Tags column. The editor adds a value at a time, so a
// list this long is a client posting whatever it likes rather than someone
// typing.
const maxTagValues = 200

// encodeTags normalises what a Tags field posts into the shape the column
// stores: a compact JSON array, trimmed, with blanks and repeats dropped, and
// "" when nothing is left.
//
// The empty string rather than "[]" is what an empty Files column holds too, so
// one read path answers both.
func encodeTags(raw string) string {
	values := decodeStringList(raw)
	seen := make(map[string]bool, len(values))
	kept := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.Join(strings.Fields(v), " ")
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		kept = append(kept, v)
		if len(kept) == maxTagValues {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return ""
	}
	return string(b)
}

// tagsHTML renders a stored Tags value as the chips a reader sees, for a grid
// column and a detail row alike.
func tagsHTML(v any) template.HTML {
	raw, _ := refParts(v)
	values := decodeStringList(raw)
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<span class="steward-tag-list">`)
	for _, value := range values {
		b.WriteString(`<span class="steward-tag">`)
		b.WriteString(template.HTMLEscapeString(value))
		b.WriteString(`</span>`)
	}
	b.WriteString(`</span>`)
	return template.HTML(b.String()) //nolint:gosec // every value is escaped above
}

// Tags renders a Tags column's stored JSON array as chips rather than as the
// raw array text.
func (c *Column[T]) Tags() *Column[T] {
	c.present = func(v any, _ *T) template.HTML { return tagsHTML(v) }
	return c
}

// Tags renders a Tags column's stored JSON array as chips rather than as the
// raw array text.
func (df *DetailField[T]) Tags() *DetailField[T] {
	df.present = func(v any, _ *T) template.HTML { return tagsHTML(v) }
	return df
}
