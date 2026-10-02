package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownToHTML(t *testing.T) {
	cases := map[string]string{
		"**bold** and `a<b>`":         "<b>bold</b> and <code>a&lt;b&gt;</code>",
		"## Title":                    "<b>Title</b>",
		"- item with *em*":            "• item with <i>em</i>",
		"see [docs](https://x.io/a)":  `see <a href="https://x.io/a">docs</a>`,
		"```go\nif a < b {}\n```":     "<pre><code class=\"language-go\">if a &lt; b {}</code></pre>",
		"`**not bold**` but **bold**": "<code>**not bold**</code> but <b>bold</b>",
	}
	for in, want := range cases {
		if got := markdownToHTML(in); got != want {
			t.Errorf("markdownToHTML(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestSplitMarkdownKeepsFencesBalanced(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("intro\n```go\n")
	for i := 0; i < 400; i++ {
		sb.WriteString("fmt.Println(\"line of code number\", i)\n")
	}
	sb.WriteString("```\noutro")

	chunks := splitMarkdown(sb.String())
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > maxChunk+10 {
			t.Errorf("chunk %d too long: %d", i, len(c))
		}
		if n := strings.Count(c, "```"); n%2 != 0 {
			t.Errorf("chunk %d has unbalanced fences (%d)", i, n)
		}
	}
	if !strings.HasSuffix(chunks[len(chunks)-1], "outro") {
		t.Errorf("last chunk lost the tail")
	}
}
