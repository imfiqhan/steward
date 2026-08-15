package steward

import (
	"bytes"
	"html/template"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"

	"github.com/imfiqhan/steward/internal/htmlsafe"
)

// markdownOnce builds the converter once. goldmark.Markdown is safe for
// concurrent use, and assembling the extension set per render is wasted work.
var (
	markdownOnce sync.Once
	markdownConv goldmark.Markdown
)

func markdownConverter() goldmark.Markdown {
	markdownOnce.Do(func() {
		markdownConv = goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithRendererOptions(
				// Raw HTML is passed through to the sanitizer rather than escaped
				// here. htmlsafe is already the boundary for Richtext, and running
				// two different rules over the same markup is how a gap opens
				// between them.
				html.WithUnsafe(),
			),
		)
	})
	return markdownConv
}

// renderMarkdown converts markdown to HTML and sanitizes the result. The output
// is safe to render unescaped.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := markdownConverter().Convert([]byte(src), &buf); err != nil {
		return template.HTML(htmlsafe.Sanitize(template.HTMLEscapeString(src))) //nolint:gosec // sanitized
	}
	return template.HTML(htmlsafe.Sanitize(buf.String())) //nolint:gosec // htmlsafe is the allowlist boundary
}
