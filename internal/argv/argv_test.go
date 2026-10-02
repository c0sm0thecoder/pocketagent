package argv

import (
	"slices"
	"testing"
)

// User text is substituted literally, never re-expanded.
func TestJoin(t *testing.T) {
	got := Join([]string{"docker", "run", "-v", "{cwd}:{cwd}"}, []string{"agent", "--msg", "{prompt}"},
		map[string]string{"cwd": "/p", "prompt": "a; rm -rf / {cwd}"})
	want := []string{"docker", "run", "-v", "/p:/p", "agent", "--msg", "a; rm -rf / {cwd}"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q", got)
	}
}
