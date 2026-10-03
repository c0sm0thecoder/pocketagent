package config

import "testing"

// Malformed config files produce errors, never panics.
func FuzzParse(f *testing.F) {
	f.Add([]byte(base + "agents: {a: {type: t, command: c}}\n"))
	f.Add([]byte("agents:\n  a:\n    type: x\n    command: [1, 2]\n    extra: {deep: [1]}\n"))
	f.Add([]byte("telegram: [\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		c, err := Parse(raw, "/tmp/c.yaml")
		if err == nil {
			for _, a := range c.Agents {
				var anything map[string]any
				_ = a.Options.Decode(&anything)
			}
		}
	})
}
