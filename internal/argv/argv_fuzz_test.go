package argv

import (
	"strings"
	"testing"
)

// Substituted values are never re-expanded, whatever they contain.
func FuzzExpandIsSinglePass(f *testing.F) {
	f.Add("{prompt}", "{cwd} {model}", "/home")
	f.Add("--x={prompt}{cwd}", "}{", "{prompt}")
	f.Fuzz(func(t *testing.T, tmpl, prompt, cwd string) {
		got := Expand([]string{tmpl}, map[string]string{"prompt": prompt, "cwd": cwd})[0]
		// Rebuild the expected result by splitting on placeholders only.
		var want strings.Builder
		for rest := tmpl; rest != ""; {
			switch {
			case strings.HasPrefix(rest, "{prompt}"):
				want.WriteString(prompt)
				rest = rest[len("{prompt}"):]
			case strings.HasPrefix(rest, "{cwd}"):
				want.WriteString(cwd)
				rest = rest[len("{cwd}"):]
			default:
				want.WriteByte(rest[0])
				rest = rest[1:]
			}
		}
		if got != want.String() {
			t.Fatalf("Expand(%q) = %q, want %q", tmpl, got, want.String())
		}
	})
}
