package steward

import (
	"strings"
	"testing"
)

// TestRenderMarkdownProducesMarkup is the half that was missing: the field
// named a format nothing ever rendered, so "# Judul" reached the page as
// "# Judul".
func TestRenderMarkdownProducesMarkup(t *testing.T) {
	src := "# Judul\n\nParagraf dengan **tebal**, *miring*, dan [tautan](https://example.test).\n\n" +
		"- satu\n- dua\n\n" +
		"| Bulan | Jumlah |\n| --- | --- |\n| Jan | 100 |\n\n" +
		"~~dicoret~~ dan `kode`\n"
	got := string(renderMarkdown(src))

	for _, want := range []string{
		"<h1>Judul</h1>", "<strong>tebal</strong>", "<em>miring</em>",
		`<a href="https://example.test">tautan</a>`,
		"<ul>", "<li>satu</li>", "<table>", "<td>Jan</td>",
		"<del>dicoret</del>", "<code>kode</code>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRenderMarkdownSanitizes covers the reason the renderer sits behind the
// allowlist: markdown permits raw HTML, so a renderer without one is a stored
// injection waiting to happen.
func TestRenderMarkdownSanitizes(t *testing.T) {
	vectors := []string{
		"<script>alert(1)</script>",
		"# Judul\n\n<script>alert(1)</script>",
		"[klik](javascript:alert(1))",
		"<img src=x onerror=alert(1)>",
		`<a href="javascript:alert(1)">x</a>`,
		"<iframe src=\"https://evil.example\"></iframe>",
		"<div onclick=\"alert(1)\">x</div>",
		"![x](javascript:alert(1))",
		`<p style="background:url(javascript:alert(1))">x</p>`,
		`<div class="fixed inset-0 z-50">x</div>`,
	}
	forbidden := []string{
		"<script", "javascript:", "onerror", "onclick", "<iframe", "class=",
	}
	for _, in := range vectors {
		got := strings.ToLower(string(renderMarkdown(in)))
		for _, bad := range forbidden {
			if strings.Contains(got, bad) {
				t.Errorf("input %q produced %q, which still contains %q", in, got, bad)
			}
		}
	}
}

// TestRenderMarkdownKeepsAlignmentStyle checks the one declaration the
// allowlist keeps survives the round trip through the renderer.
func TestRenderMarkdownKeepsAlignmentStyle(t *testing.T) {
	got := string(renderMarkdown("| a |\n| :-: |\n| x |\n"))
	if !strings.Contains(got, "text-align: center") {
		t.Errorf("a centred column lost its alignment:\n%s", got)
	}
}

// TestRenderMarkdownTaskListLosesItsBoxes pins a documented consequence of the
// allowlist: <input> is not on it, so a task list renders as an ordinary one.
func TestRenderMarkdownTaskListLosesItsBoxes(t *testing.T) {
	got := string(renderMarkdown("- [x] sudah\n- [ ] belum\n"))
	if strings.Contains(got, "<input") {
		t.Errorf("an input reached the output: %s", got)
	}
	for _, want := range []string{"<ul>", "sudah", "belum"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list itself was lost, missing %q: %s", want, got)
		}
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	if got := string(renderMarkdown("")); strings.TrimSpace(got) != "" {
		t.Errorf("empty markdown rendered %q", got)
	}
}
