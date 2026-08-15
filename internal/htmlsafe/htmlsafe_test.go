package htmlsafe

import (
	"strings"
	"testing"
)

// TestSanitizeHTMLBlocksInjection is the security-critical case: nothing that
// can execute may survive. Each input is checked for the absence of the
// dangerous construct rather than against an exact expected string, so the
// assertion stays meaningful if the allowlist grows.
func TestSanitizeHTMLBlocksInjection(t *testing.T) {
	vectors := []string{
		`<script>alert(1)</script>`,
		`<SCRIPT>alert(1)</SCRIPT>`,
		`<scr<script>ipt>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<p onclick="alert(1)">click</p>`,
		`<p onmouseover=alert(1)>hover</p>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<a href="java&#115;cript:alert(1)">x</a>`,
		"<a href=\"java\tscript:alert(1)\">x</a>",
		"<a href=\"java\nscript:alert(1)\">x</a>",
		"<a href=\"\x01javascript:alert(1)\">x</a>",
		`<a href=" javascript:alert(1)">x</a>`,
		`<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
		`<a href="vbscript:msgbox(1)">x</a>`,
		`<iframe src="https://evil.example"></iframe>`,
		`<object data="x.swf"></object>`,
		`<embed src="x.swf">`,
		`<svg><script>alert(1)</script></svg>`,
		`<svg onload=alert(1)>`,
		`<math><mtext><script>alert(1)</script></mtext></math>`,
		`<style>body{background:url(javascript:alert(1))}</style>`,
		`<p style="background:url(javascript:alert(1))">x</p>`,
		`<!--[if IE]><script>alert(1)</script><![endif]-->`,
		`<form action="/x"><input name="y"></form>`,
		`<base href="https://evil.example/">`,
		`<meta http-equiv="refresh" content="0;url=https://evil.example">`,
		`<link rel="stylesheet" href="https://evil.example/x.css">`,
		`<template><script>alert(1)</script></template>`,
		`<noscript><p>x</p></noscript>`,
		`<body onload=alert(1)>`,
		`<div><p>text</p><script>alert(1)</script></div>`,
	}
	forbidden := []string{
		"<script", "</script", "javascript:", "vbscript:", "data:text/html",
		"onerror", "onclick", "onload", "onmouseover", "<iframe", "<object",
		"<embed", "<svg", "<math", "<style", "<form", "<input", "<base",
		"<meta", "<link", "<template", "<!--", "style=",
	}
	for _, in := range vectors {
		got := strings.ToLower(Sanitize(in))
		for _, bad := range forbidden {
			if strings.Contains(got, bad) {
				t.Errorf("input %q produced %q, which still contains %q", in, got, bad)
			}
		}
	}
}

// TestSanitizeHTMLKeepsEditorialMarkup covers what an article body actually
// carries. Dropping an unknown tag keeps its children, so a table that is not
// allowlisted does not merely lose its borders — its cells run together into a
// single line of text, and an image disappears outright.
func TestSanitizeHTMLKeepsEditorialMarkup(t *testing.T) {
	in := `<table class="t" width="600"><tbody>` +
		`<tr><th scope="col">Bulan</th><th>Jumlah</th></tr>` +
		`<tr><td colspan="2" style="text-align: center;">Jan</td></tr>` +
		`</tbody></table>` +
		`<figure><img src="/uploads/foto.jpg" alt="Foto" width="800" height="600">` +
		`<figcaption>Keterangan</figcaption></figure>`
	got := Sanitize(in)
	for _, want := range []string{
		"<table>", "<tbody>", "<tr>", `<th scope="col">`, `<td colspan="2"`,
		`<img src="/uploads/foto.jpg" alt="Foto" width="800" height="600">`,
		"<figure>", "<figcaption>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "class=") || strings.Contains(got, "width=\"600\"") {
		t.Errorf("presentational attributes survived on the table: %q", got)
	}
}

// TestSanitizeHTMLFiltersStyle keeps alignment and nothing else. A class would
// be worse than a style here: the panel's utility classes are ambient, so
// "fixed inset-0 z-50" on stored content is a full-viewport overlay.
func TestSanitizeHTMLFiltersStyle(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p style="text-align: justify;">x</p>`, `<p style="text-align: justify">x</p>`},
		{`<p style="TEXT-ALIGN: CENTER">x</p>`, `<p style="text-align: center">x</p>`},
		{`<p style="text-align:justify;font-family:Arial;font-size:12pt">x</p>`,
			`<p style="text-align: justify">x</p>`},
		{`<p style="font-family: Arial; mso-spacerun: yes">x</p>`, `<p>x</p>`},
		{`<p style="text-align: expression(alert(1))">x</p>`, `<p>x</p>`},
		{`<p style="background: url(javascript:alert(1))">x</p>`, `<p>x</p>`},
		{`<p class="MsoNormal">x</p>`, `<p>x</p>`},
		{`<div class="fixed inset-0 z-50">x</div>`, `<div>x</div>`},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeHTMLImageSources holds an image's src to the same schemes as a
// link's, which is what keeps an SVG data URI from carrying script.
func TestSanitizeHTMLImageSources(t *testing.T) {
	kept := []string{"/uploads/a.jpg", "https://example.test/a.jpg", "../b.png"}
	for _, src := range kept {
		if got := Sanitize(`<img src="` + src + `">`); !strings.Contains(got, src) {
			t.Errorf("src %q was dropped: %q", src, got)
		}
	}
	dropped := []string{
		"javascript:alert(1)",
		"data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+",
		"vbscript:msgbox(1)",
	}
	for _, src := range dropped {
		got := Sanitize(`<img src="` + src + `">`)
		if strings.Contains(got, "src=") {
			t.Errorf("src %q survived: %q", src, got)
		}
	}
}

// TestSanitizeHTMLKeepsFormatting checks the editor's own output survives — a
// sanitizer that strips everything would pass the security test and be useless.
func TestSanitizeHTMLKeepsFormatting(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p>Hello <strong>world</strong></p>`, `<p>Hello <strong>world</strong></p>`},
		{`<p><em>a</em> <u>b</u> <s>c</s></p>`, `<p><em>a</em> <u>b</u> <s>c</s></p>`},
		{`<h2>Heading</h2>`, `<h2>Heading</h2>`},
		{`<ul><li>one</li><li>two</li></ul>`, `<ul><li>one</li><li>two</li></ul>`},
		{`<ol><li>one</li></ol>`, `<ol><li>one</li></ol>`},
		{`<blockquote>quoted</blockquote>`, `<blockquote>quoted</blockquote>`},
		{`line<br>break`, `line<br>break`},
		{`<p>a</p><hr><p>b</p>`, `<p>a</p><hr><p>b</p>`},
		{`<a href="https://example.com">link</a>`, `<a href="https://example.com">link</a>`},
		{`<a href="/berita/x">relative</a>`, `<a href="/berita/x">relative</a>`},
		{`<a href="mailto:a@b.test">mail</a>`, `<a href="mailto:a@b.test">mail</a>`},
		{`<p>plain text</p>`, `<p>plain text</p>`},
		{`<pre><code>x := 1</code></pre>`, `<pre><code>x := 1</code></pre>`},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeHTMLEscapesText(t *testing.T) {
	// A disallowed tag is dropped but its text is kept, escaped — so it can
	// never re-enter as markup.
	got := Sanitize(`<p>5 &lt; 6 &amp; 7 &gt; 6</p>`)
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("entities were not preserved: %q", got)
	}
	if got := Sanitize(`a < b`); strings.Contains(got, "< b") {
		t.Errorf("a bare < should be escaped: %q", got)
	}
}

func TestSanitizeHTMLBalancesTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<p>unclosed`, `<p>unclosed</p>`},
		{`<p><strong>both unclosed`, `<p><strong>both unclosed</strong></p>`},
		{`</p>stray close`, `stray close`},
		{`<p>a<strong>b</p>c`, `<p>a<strong>b</strong></p>c`},
		// An unclosed <li> is closed implicitly by the next one, the way a
		// browser parses it.
		{`<ul><li>a<li>b</ul>`, `<ul><li>a</li><li>b</li></ul>`},
		{`<p>a<p>b`, `<p>a</p><p>b</p>`},
		// A genuinely nested list is left alone.
		{`<ul><li>a<ul><li>b</li></ul></li></ul>`, `<ul><li>a<ul><li>b</li></ul></li></ul>`},
	}
	for _, tc := range cases {
		if got := Sanitize(tc.in); got != tc.want {
			t.Errorf("Sanitize(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeHTMLTargetBlankGetsNoopener(t *testing.T) {
	got := Sanitize(`<a href="https://example.com" target="_blank">x</a>`)
	if !strings.Contains(got, `target="_blank"`) {
		t.Errorf("target was dropped: %q", got)
	}
	if !strings.Contains(got, "noopener") {
		t.Errorf("target=_blank must carry rel=noopener: %q", got)
	}
	// A hostile rel is replaced, not trusted.
	got = Sanitize(`<a href="https://example.com" target="_blank" rel="opener">x</a>`)
	if strings.Contains(got, `rel="opener"`) {
		t.Errorf("rel should have been normalized: %q", got)
	}
	// Any other target value is not useful and is dropped.
	got = Sanitize(`<a href="https://example.com" target="_top">x</a>`)
	if strings.Contains(got, "target=") {
		t.Errorf("target=_top should be dropped: %q", got)
	}
}

func TestSanitizeHTMLEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t"} {
		if got := Sanitize(in); got != "" {
			t.Errorf("Sanitize(%q) = %q, want empty", in, got)
		}
	}
}

func TestSafeURL(t *testing.T) {
	safe := []string{
		"https://example.com", "http://example.com/a?b=1", "/relative/path",
		"relative", "mailto:a@b.test", "tel:+62211234", "#anchor",
		"?query=1", "/path/with:colon", "?a=1:2", "#a:b",
		"ftp://files.example.com",
	}
	for _, u := range safe {
		if !safeURL(u) {
			t.Errorf("safeURL(%q) = false, want true", u)
		}
	}
	unsafe := []string{
		"javascript:alert(1)", "JAVASCRIPT:alert(1)", "  javascript:alert(1)",
		"java\tscript:alert(1)", "java\nscript:alert(1)", "\x00javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>", "vbscript:msgbox(1)",
		"file:///etc/passwd", "about:blank", "blob:https://x", "", "   ",
	}
	for _, u := range unsafe {
		if safeURL(u) {
			t.Errorf("safeURL(%q) = true, want false", u)
		}
	}
}

// TestSanitizeHTMLIsIdempotent matters because a value is sanitized on save and
// may be re-saved after an edit; a second pass must not alter it further.
func TestSanitizeHTMLIsIdempotent(t *testing.T) {
	inputs := []string{
		`<p>Hello <strong>world</strong></p>`,
		`<a href="https://example.com" target="_blank">x</a>`,
		`<ul><li>a</li><li>b</li></ul>`,
		`<script>alert(1)</script><p>kept</p>`,
		`<p>5 &lt; 6</p>`,
		`<p>unclosed`,
	}
	for _, in := range inputs {
		once := Sanitize(in)
		twice := Sanitize(once)
		if once != twice {
			t.Errorf("not idempotent for %q:\n first %q\nsecond %q", in, once, twice)
		}
	}
}
