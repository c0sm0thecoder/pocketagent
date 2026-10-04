package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func writeSession(t *testing.T, dir, id string, age time.Duration, lines ...string) {
	t.Helper()
	p := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	os.Chtimes(p, when, when)
}

func TestSessions(t *testing.T) {
	history := t.TempDir()
	cwd := "/Users/me/Projects/My.App"
	dir := filepath.Join(history, "-Users-me-Projects-My-App") // how Claude Code names it
	os.MkdirAll(dir, 0o700)

	writeSession(t, dir, "older", 2*time.Hour,
		`{"type":"user","message":{"role":"user","content":"fix the login bug please"}}`)
	writeSession(t, dir, "titled", time.Hour,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<command-name>/init</command-name>"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"add dark mode"}]}}`,
		`{"type":"ai-title","aiTitle":"Add dark mode"}`,
		`{"type":"ai-title","aiTitle":"Add dark mode and tests"}`)
	writeSession(t, dir, "empty", time.Minute, `{"type":"system"}`) // nothing typed: skipped

	a, err := New(agent.Spec{Command: []string{"claude"}, Options: config.OptionsFrom(map[string]any{"history_dir": history})})
	if err != nil {
		t.Fatal(err)
	}
	ss, err := a.(agent.SessionLister).Sessions(context.Background(), "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != "titled" || ss[0].Title != "Add dark mode and tests" || ss[1].Title != "fix the login bug please" {
		t.Fatalf("sessions = %+v", ss)
	}
	if none, err := a.(agent.SessionLister).Sessions(context.Background(), "", "/never/used"); err != nil || len(none) != 0 {
		t.Errorf("unknown folder: %v %v", none, err)
	}
}

// The latest title is read from the end of a large log.
func TestSessionTitleFromTail(t *testing.T) {
	dir := t.TempDir()
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 1000) + `"}}`
	lines := []string{`{"type":"user","message":{"content":"first prompt"}}`, `{"type":"ai-title","aiTitle":"Early title"}`}
	for range 400 {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"type":"ai-title","aiTitle":"Final title"}`)
	writeSession(t, dir, "big", 0, lines...)
	fi, _ := os.Stat(filepath.Join(dir, "big.jsonl"))
	if got := sessionTitle(filepath.Join(dir, "big.jsonl"), fi.Size()); got != "Final title" {
		t.Errorf("title = %q", got)
	}
}

func TestResumeCommand(t *testing.T) {
	a, _ := New(agent.Spec{Command: []string{"claude"}, Options: agent.NoOptions{}})
	h := a.(agent.ResumeHinter)
	if got := h.ResumeCommand("/Users/me/api", "abc-123"); got != "cd /Users/me/api && claude --resume abc-123" {
		t.Errorf("got %q", got)
	}
	if got := h.ResumeCommand("/Users/me/My Thesis's", "id"); got != `cd '/Users/me/My Thesis'\''s' && claude --resume id` {
		t.Errorf("quoting: %q", got)
	}
}

// A project reached through a symlink still finds sessions filed under the
// resolved path.
func TestSessionsThroughSymlink(t *testing.T) {
	history, root := t.TempDir(), t.TempDir()
	realDir := filepath.Join(root, "real")
	os.MkdirAll(realDir, 0o700)
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skip(err)
	}
	resolved, _ := filepath.EvalSymlinks(realDir)
	dir := filepath.Join(history, nonAlnum.ReplaceAllString(resolved, "-"))
	os.MkdirAll(dir, 0o700)
	writeSession(t, dir, "s", 0, `{"type":"user","message":{"content":"hello"}}`)

	a, _ := New(agent.Spec{Command: []string{"claude"}, Options: config.OptionsFrom(map[string]any{"history_dir": history})})
	ss, err := a.(agent.SessionLister).Sessions(context.Background(), "", link)
	if err != nil || len(ss) != 1 || ss[0].ID != "s" {
		t.Errorf("sessions = %+v, %v", ss, err)
	}
}
