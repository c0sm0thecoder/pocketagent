package gitcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotDiffRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}

	run("init", "-q")
	write("a.txt", "one\n")
	write("gone.txt", "keep me\n")
	run("add", ".")
	run("commit", "-qm", "init")
	write("a.txt", "one\nuncommitted\n") // a dirty change that exists before the turn
	run("add", "a.txt")                  // ...and is staged

	cp, err := Snapshot(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	// The "agent turn": edit, create, delete.
	write("a.txt", "rewritten by agent\n")
	write("new.txt", "created by agent\n")
	os.Remove(filepath.Join(dir, "gone.txt"))

	d, err := Diff(ctx, dir, cp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rewritten by agent", "new.txt", "gone.txt"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}

	if err := Restore(ctx, dir, cp); err != nil {
		t.Fatal(err)
	}
	if got := read("a.txt"); got != "one\nuncommitted\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := read("new.txt"); got != "<missing>" {
		t.Errorf("new.txt should be removed, got %q", got)
	}
	if got := read("gone.txt"); got != "keep me\n" {
		t.Errorf("gone.txt = %q", got)
	}
	// The user's staged change survives.
	cmd := exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = dir
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "a.txt" {
		t.Errorf("index changed: staged = %q", out)
	}
}
