package main

import (
	"html"
	"regexp"
	"strings"
)

// Telegram caps messages at 4096 characters. Leave room for HTML tags.
const maxChunk = 3500

// splitMarkdown breaks text into chunks at line boundaries. A chunk that
// ends inside a ``` fence gets the fence closed, and the next chunk reopens it.
func splitMarkdown(text string) []string {
	var chunks []string
	var cur strings.Builder
	openFence := "" // the fence line currently open, e.g. "```go"

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		s := cur.String()
		if openFence != "" {
			s += "\n```"
		}
		chunks = append(chunks, strings.TrimRight(s, "\n"))
		cur.Reset()
		if openFence != "" {
			cur.WriteString(openFence + "\n")
		}
	}

	for _, line := range strings.Split(text, "\n") {
		// Hard-wrap absurdly long single lines.
		for len(line) > maxChunk {
			flush()
			cur.WriteString(line[:maxChunk] + "\n")
			line = line[maxChunk:]
		}
		if cur.Len()+len(line)+1 > maxChunk {
			flush()
		}
		cur.WriteString(line + "\n")
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") {
			if openFence == "" {
				openFence = t
			} else {
				openFence = ""
			}
		}
	}
	openFence = ""
	flush()
	return chunks
}

var (
	reInlineCode = regexp.MustCompile("`([^`\n]+)`")
	reBold       = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	reItalic     = regexp.MustCompile(`(^|[^\w*])\*([^*\n]+)\*`)
	reLink       = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
	reHeading    = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	reStrike     = regexp.MustCompile(`~~([^~\n]+)~~`)
)

// markdownToHTML converts the Markdown Claude usually writes into the
// small HTML subset Telegram supports. Anything else stays escaped text.
func markdownToHTML(md string) string {
	var out strings.Builder
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "```") {
			lang := strings.TrimSpace(strings.TrimPrefix(t, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			if lang != "" {
				out.WriteString(`<pre><code class="language-` + html.EscapeString(lang) + `">`)
			} else {
				out.WriteString("<pre><code>")
			}
			out.WriteString(html.EscapeString(strings.Join(code, "\n")))
			out.WriteString("</code></pre>\n")
			continue
		}
		out.WriteString(inlineToHTML(line))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func inlineToHTML(line string) string {
	if m := reHeading.FindStringSubmatch(line); m != nil {
		return "<b>" + inlineToHTML(m[1]) + "</b>"
	}
	// Pull inline code out first so its contents are not formatted.
	var codes []string
	line = reInlineCode.ReplaceAllStringFunc(line, func(s string) string {
		codes = append(codes, s[1:len(s)-1])
		return "\x00" + string(rune('A'+len(codes)-1)) + "\x00"
	})
	line = html.EscapeString(line)
	line = reLink.ReplaceAllString(line, `<a href="$2">$1</a>`)
	line = reBold.ReplaceAllString(line, "<b>$1</b>")
	line = reItalic.ReplaceAllString(line, "$1<i>$2</i>")
	line = reStrike.ReplaceAllString(line, "<s>$1</s>")
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		line = "• " + line[2:]
	}
	for i, c := range codes {
		line = strings.Replace(line, "\x00"+string(rune('A'+i))+"\x00", "<code>"+html.EscapeString(c)+"</code>", 1)
	}
	return line
}

// toolSummary renders a one-line description of a tool call.
func toolSummary(name string, input map[string]any) string {
	str := func(k string) string { s, _ := input[k].(string); return s }
	var detail string
	switch name {
	case "Bash":
		detail = str("command")
	case "Read", "Write", "Edit", "NotebookEdit":
		detail = str("file_path")
		if detail == "" {
			detail = str("notebook_path")
		}
	case "Glob", "Grep":
		detail = str("pattern")
	case "WebFetch":
		detail = str("url")
	case "WebSearch":
		detail = str("query")
	case "Task", "Agent":
		detail = str("description")
	}
	detail = strings.ReplaceAll(detail, "\n", " ")
	if len(detail) > 120 {
		detail = detail[:117] + "..."
	}
	if detail == "" {
		return name
	}
	return name + ": " + detail
}
