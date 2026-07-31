package steward

// HTML sanitization for the Richtext form field.
//
// A contenteditable input submits whatever markup the browser holds, so the
// value arriving at the server is arbitrary HTML from an authenticated but not
// necessarily trustworthy client — and Detail.HTML renders it back unescaped to
// other administrators. That makes this an allowlist, not a blocklist: tags and
// attributes not named here are dropped, so a construct nobody thought of fails
// closed.
//
// Parsing is done with golang.org/x/net/html's tokenizer rather than by hand.
// HTML's error recovery is full of corner cases that regular expressions and
// string scanning get wrong, and each one is an XSS bypass.

import (
	"strings"

	"golang.org/x/net/html"
)

// allowedTags is the markup the Richtext toolbar can produce, plus the
// equivalents a paste may introduce.
var allowedTags = map[string]bool{
	"p": true, "br": true, "hr": true,
	"strong": true, "b": true, "em": true, "i": true, "u": true, "s": true,
	"strike": true, "del": true, "ins": true, "sub": true, "sup": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true,
	"blockquote": true, "pre": true, "code": true,
	"a": true, "div": true, "span": true,
}

// voidTags never receive a closing tag.
var voidTags = map[string]bool{"br": true, "hr": true}

// autoClose names tags that cannot contain themselves, so an unclosed one is
// closed implicitly when the next starts — the shape editors actually emit
// ("<li>a<li>b"). The value is the set of container tags that stop the search,
// which keeps a genuinely nested list ("<ul><li><ul><li>") intact.
var autoClose = map[string]map[string]bool{
	"li": {"ul": true, "ol": true},
	"p":  {},
}

// droppedSubtrees have their entire contents discarded, not just their tags:
// escaping a script body would preserve visible gibberish for no benefit.
var droppedSubtrees = map[string]bool{
	"script": true, "style": true, "iframe": true, "object": true,
	"embed": true, "template": true, "noscript": true, "svg": true, "math": true,
}

// allowedAttrs lists the attributes kept per tag. Everything else goes — in
// particular every "on*" handler and every style attribute, which is where
// most surviving injection vectors live.
var allowedAttrs = map[string]map[string]bool{
	"a": {"href": true, "title": true, "target": true, "rel": true},
}

// safeURLSchemes are the schemes an href may use. A relative URL has no scheme
// and is allowed; "javascript:" and "data:" are the reason this check exists.
var safeURLSchemes = map[string]bool{
	"http": true, "https": true, "mailto": true, "tel": true, "ftp": true,
}

// sanitizeHTML returns html with every disallowed tag, attribute, and URL
// scheme removed, and with tags balanced.
func sanitizeHTML(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	var out strings.Builder
	var open []string // stack of emitted, still-open tags
	skipDepth := 0    // >0 while inside a dropped subtree
	var skipTag string

	z := html.NewTokenizer(strings.NewReader(input))
	for {
		switch z.Next() {
		case html.ErrorToken:
			// Any error (including io.EOF) ends the document; close what is open.
			for i := len(open) - 1; i >= 0; i-- {
				out.WriteString("</" + open[i] + ">")
			}
			return out.String()

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			// Text is re-escaped, so a literal "<" in the input can never
			// become markup in the output.
			out.WriteString(html.EscapeString(string(z.Text())))

		case html.StartTagToken:
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			if skipDepth > 0 {
				if name == skipTag {
					skipDepth++
				}
				continue
			}
			if droppedSubtrees[name] {
				skipDepth, skipTag = 1, name
				continue
			}
			if !allowedTags[name] {
				continue // drop the tag, keep its children
			}
			open = closeImplicit(&out, open, name)
			writeStartTag(&out, name, tok.Attr)
			if !voidTags[name] {
				open = append(open, name)
			}

		case html.EndTagToken:
			name := strings.ToLower(z.Token().Data)
			if skipDepth > 0 {
				if name == skipTag {
					skipDepth--
					if skipDepth == 0 {
						skipTag = ""
					}
				}
				continue
			}
			if !allowedTags[name] || voidTags[name] {
				continue
			}
			// Close down to the matching tag so the output stays balanced even
			// when the input is not. An end tag with no open counterpart is
			// dropped rather than emitted.
			idx := -1
			for i := len(open) - 1; i >= 0; i-- {
				if open[i] == name {
					idx = i
					break
				}
			}
			if idx < 0 {
				continue
			}
			for i := len(open) - 1; i >= idx; i-- {
				out.WriteString("</" + open[i] + ">")
			}
			open = open[:idx]

		case html.SelfClosingTagToken:
			if skipDepth > 0 {
				continue
			}
			tok := z.Token()
			name := strings.ToLower(tok.Data)
			if !allowedTags[name] {
				continue
			}
			writeStartTag(&out, name, tok.Attr)
			if !voidTags[name] {
				out.WriteString("</" + name + ">")
			}

		case html.CommentToken, html.DoctypeToken:
			// Dropped: comments can hide conditional markup, and a doctype has
			// no meaning inside a fragment.
			continue
		}
	}
}

// closeImplicit closes an open tag that the tag about to be written cannot sit
// inside, emitting the end tags and returning the trimmed stack.
func closeImplicit(out *strings.Builder, open []string, name string) []string {
	boundaries, ok := autoClose[name]
	if !ok {
		return open
	}
	for i := len(open) - 1; i >= 0; i-- {
		if boundaries[open[i]] {
			return open // a container intervenes, so this is genuine nesting
		}
		if open[i] == name {
			for j := len(open) - 1; j >= i; j-- {
				out.WriteString("</" + open[j] + ">")
			}
			return open[:i]
		}
	}
	return open
}

// writeStartTag emits a tag with only its allowlisted attributes.
func writeStartTag(out *strings.Builder, name string, attrs []html.Attribute) {
	out.WriteString("<" + name)
	allowed := allowedAttrs[name]
	for _, at := range attrs {
		key := strings.ToLower(at.Key)
		if at.Namespace != "" || !allowed[key] {
			continue
		}
		val := at.Val
		switch key {
		case "href":
			if !safeURL(val) {
				continue
			}
		case "target":
			// Only _blank is useful, and it must carry rel="noopener".
			if val != "_blank" {
				continue
			}
		case "rel":
			val = "noopener noreferrer"
		}
		out.WriteString(" " + key + `="` + html.EscapeString(val) + `"`)
	}
	// A link opening a new tab always gets noopener, whether or not the client
	// sent rel.
	if name == "a" && hasTargetBlank(attrs) && !hasAttr(attrs, "rel") {
		out.WriteString(` rel="noopener noreferrer"`)
	}
	out.WriteString(">")
}

func hasTargetBlank(attrs []html.Attribute) bool {
	for _, at := range attrs {
		if strings.EqualFold(at.Key, "target") && at.Val == "_blank" {
			return true
		}
	}
	return false
}

func hasAttr(attrs []html.Attribute, key string) bool {
	for _, at := range attrs {
		if strings.EqualFold(at.Key, key) {
			return true
		}
	}
	return false
}

// safeURL reports whether a URL may appear in an href.
//
// The scheme is read by hand rather than with net/url, because the check must
// mirror what a browser does with the raw attribute: strip the control
// characters and whitespace browsers ignore, then look at what precedes the
// first colon. net/url would reject some strings browsers still navigate.
func safeURL(raw string) bool {
	var cleaned strings.Builder
	for _, r := range raw {
		// Browsers discard tabs, newlines, and C0 controls inside URLs, which
		// is how "java\nscript:" slips past naive checks.
		if r <= 0x20 || r == 0x7F {
			continue
		}
		cleaned.WriteRune(r)
	}
	s := cleaned.String()
	if s == "" {
		return false
	}
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return true // relative URL
	}
	// A colon appearing after a path or query separator is part of the path,
	// not a scheme ("/a:b", "?x=1:2", "#a:b").
	for _, sep := range []byte{'/', '?', '#'} {
		if i := strings.IndexByte(s, sep); i >= 0 && i < colon {
			return true
		}
	}
	return safeURLSchemes[strings.ToLower(s[:colon])]
}
