package telegram

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	reTag      = regexp.MustCompile(`</?([a-z]+)[^>]*>`)
	allowedTag = map[string]bool{"b": true, "i": true, "s": true, "code": true, "pre": true, "a": true}
)

// Agent output is untrusted: whatever it contains, the HTML sent to
// Telegram must only use allowed tags, keep them balanced, and never leak a
// raw '<' from the input.
func FuzzMarkdownToHTML(f *testing.F) {
	for _, s := range []string{
		"**bold** and `code`", "```go\nx := 1 < 2\n```", "# heading", "- item *em*",
		"[link](https://x.io)", "<script>alert(1)</script>", "**unclosed", "`a`b`c`", "~~s~~ \x00",
		"```\nno close", "[x](javascript:alert(1))", "a<b>c</b>&amp;",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		if !utf8.ValidString(md) {
			return
		}
		out := markdownToHTML(md)
		var stack []string
		for _, m := range reTag.FindAllStringSubmatch(out, -1) {
			tag := m[1]
			if !allowedTag[tag] {
				t.Fatalf("disallowed tag %q in %q", m[0], out)
			}
			if strings.HasPrefix(m[0], "</") {
				if len(stack) == 0 || stack[len(stack)-1] != tag {
					t.Fatalf("unbalanced %q in %q (input %q)", m[0], out, md)
				}
				stack = stack[:len(stack)-1]
			} else {
				stack = append(stack, tag)
			}
		}
		if len(stack) != 0 {
			t.Fatalf("unclosed %v in %q (input %q)", stack, out, md)
		}
		if strings.Contains(reTag.ReplaceAllString(out, ""), "<") {
			t.Fatalf("raw '<' in %q (input %q)", out, md)
		}
		for _, href := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(out, -1) {
			if !strings.HasPrefix(href[1], "http://") && !strings.HasPrefix(href[1], "https://") {
				t.Fatalf("non-http link %q", href[1])
			}
		}
	})
}

// Splitting never loses text, respects Telegram's size limit, and keeps
// code fences balanced in every chunk.
func FuzzSplitMarkdown(f *testing.F) {
	f.Add("short")
	f.Add("intro\n```go\n" + strings.Repeat("line of code\n", 400) + "```\noutro")
	f.Add(strings.Repeat("x", maxChunk*2+7))
	f.Fuzz(func(t *testing.T, md string) {
		chunks := splitMarkdown(md)
		for i, c := range chunks {
			if len(c) > maxChunk+len("```go\n")+len("\n```")+64 {
				t.Fatalf("chunk %d is %d bytes", i, len(c))
			}
			if strings.Count(c, "```")%2 != 0 && strings.Count(md, "```")%2 == 0 {
				t.Fatalf("chunk %d has unbalanced fences", i)
			}
		}
		strip := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), "") }
		joined := strings.Join(chunks, "")
		for _, word := range strings.Fields(strings.ReplaceAll(md, "`", "")) {
			if !strings.Contains(strip(joined), strip(word)) {
				t.Fatalf("lost %q", word)
			}
		}
	})
}
