package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func session(t *testing.T, path, cwd string, age time.Duration) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	line := `{"type":"session_meta","payload":{"cwd":"` + cwd + `","x":1}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	os.Chtimes(path, when, when)
}

func names(cs []Candidate) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestSuggest(t *testing.T) {
	home := t.TempDir()
	api := mkdir(t, home, "projects", "api")
	mkdir(t, api, ".git")
	web := mkdir(t, home, "code", "web")
	thesis := mkdir(t, home, "study", "thesis")
	other := mkdir(t, home, "elsewhere", "api") // same name as ~/projects/api
	mkdir(t, home, "projects", ".hidden")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(api, old, old)
	os.Chtimes(web, old.Add(-time.Hour), old.Add(-time.Hour))

	// Agent history, newest first: thesis, then other; plus noise.
	session(t, filepath.Join(home, ".claude/projects/a/1.jsonl"), thesis, time.Minute)
	session(t, filepath.Join(home, ".codex/sessions/2026/10/01/r.jsonl"), other, time.Hour)
	session(t, filepath.Join(home, ".claude/projects/b/2.jsonl"), home, 2*time.Minute)              // home: a container
	session(t, filepath.Join(home, ".claude/projects/c/3.jsonl"), "/no/longer/exists", time.Minute) // gone
	session(t, filepath.Join(home, ".claude/projects/d/4.jsonl"), filepath.Join(os.TempDir(), "x"), time.Minute)

	got := Suggest(Options{Home: home})
	if names(got) != "thesis,elsewhere-api,projects-api,web" {
		t.Fatalf("suggestions = %s (%+v)", names(got), got)
	}
	if !strings.Contains(got[0].Reason, "Claude Code") || !strings.Contains(got[1].Reason, "Codex") {
		t.Errorf("reasons = %q, %q", got[0].Reason, got[1].Reason)
	}
	if got[2].Reason != "git repo in ~/projects" || got[3].Reason != "in ~/code" {
		t.Errorf("reasons = %q, %q", got[2].Reason, got[3].Reason)
	}

	// Existing projects are excluded, and the limit applies.
	got = Suggest(Options{Home: home, Exclude: []string{thesis}, Limit: 2})
	if names(got) != "elsewhere-api,projects-api" {
		t.Errorf("with exclude and limit: %s", names(got))
	}
}

func TestSuggestEmptyHome(t *testing.T) {
	if got := Suggest(Options{Home: t.TempDir()}); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestFirstCwd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"a":1}`+"\n"+`{"cwd":"\/Users\/me\/code \"x\""}`+"\n"), 0o600)
	if got := firstCwd(p); got != `/Users/me/code \"x\"` {
		t.Errorf("got %q", got)
	}
	if firstCwd("/missing") != "" {
		t.Error("missing file")
	}
}

// The same folder reached by two spellings (case-insensitive file systems,
// symlinks) is suggested once.
func TestSameFolderOnce(t *testing.T) {
	home := t.TempDir()
	real := mkdir(t, home, "projects", "api")
	if err := os.Symlink(filepath.Join(home, "projects"), filepath.Join(home, "code")); err != nil {
		t.Skip(err)
	}
	session(t, filepath.Join(home, ".claude/projects/a/1.jsonl"), real, time.Minute)
	got := Suggest(Options{Home: home})
	if len(got) != 1 || got[0].Path != real {
		t.Errorf("got %+v", got)
	}
	if got := Suggest(Options{Home: home, Exclude: []string{filepath.Join(home, "code", "api")}}); len(got) != 0 {
		t.Errorf("excluded folder suggested under another path: %+v", got)
	}
}

// Sessions started in a project's subfolders fold into the project, but a
// git repo inside a non-git umbrella folder stays a project of its own.
func TestNestedFolders(t *testing.T) {
	home := t.TempDir()
	thesis := mkdir(t, home, "study", "thesis")
	mkdir(t, thesis, ".git")
	docs := mkdir(t, thesis, "docs", "planning")
	umbrella := mkdir(t, home, "work", "acme")
	backend := mkdir(t, umbrella, "backend")
	mkdir(t, backend, ".git")
	notes := mkdir(t, umbrella, "notes")
	for i, p := range []string{thesis, docs, umbrella, backend, notes} {
		session(t, filepath.Join(home, ".claude/projects", fmt.Sprint(i), "s.jsonl"), p, time.Duration(i)*time.Minute)
	}
	if got := names(Suggest(Options{Home: home})); got != "thesis,acme,backend" {
		t.Errorf("got %s", got)
	}
}
