// Package gitcp takes git snapshots of a working tree before each agent
// turn, so changes can be shown (/diff) and rolled back (/undo) without
// touching the user's branch, index or stash.
package gitcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Root returns the repository root containing dir, or "" if dir is not in a repo.
func Root(ctx context.Context, dir string) string {
	root, err := git(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return root
}

// tree writes the full working tree (tracked and untracked, minus ignored
// files) as a git tree object, using a copy of the index so the user's
// staging area is untouched.
func tree(ctx context.Context, root string) (string, error) {
	tmp, err := os.CreateTemp("", "pocketagent-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// Start from the real index so git can reuse its stat cache and only
	// hash files that changed.
	if idx, err := git(ctx, root, nil, "rev-parse", "--path-format=absolute", "--git-path", "index"); err == nil {
		if src, err := os.Open(idx); err == nil {
			dst, _ := os.Create(tmp.Name())
			io.Copy(dst, src)
			src.Close()
			dst.Close()
		}
	}
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if _, err := git(ctx, root, env, "add", "-A"); err != nil {
		return "", err
	}
	return git(ctx, root, env, "write-tree")
}

// Snapshot records the current working tree and returns a commit id.
func Snapshot(ctx context.Context, dir string) (string, error) {
	root := Root(ctx, dir)
	if root == "" {
		return "", fmt.Errorf("not a git repository")
	}
	t, err := tree(ctx, root)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", t, "-m", "pocketagent checkpoint"}
	if head, err := git(ctx, root, nil, "rev-parse", "--verify", "-q", "HEAD"); err == nil && head != "" {
		args = append(args, "-p", head)
	}
	env := []string{"GIT_AUTHOR_NAME=pocketagent", "GIT_AUTHOR_EMAIL=pocketagent@localhost",
		"GIT_COMMITTER_NAME=pocketagent", "GIT_COMMITTER_EMAIL=pocketagent@localhost"}
	return git(ctx, root, env, args...)
}

// Diff returns a patch from `from` (a commit, or "HEAD") to the current
// working tree, including new untracked files.
func Diff(ctx context.Context, dir, from string) (string, error) {
	root := Root(ctx, dir)
	if root == "" {
		return "", fmt.Errorf("not a git repository")
	}
	t, err := tree(ctx, root)
	if err != nil {
		return "", err
	}
	return git(ctx, root, nil, "diff", "--no-color", "--stat", "--patch", from, t)
}

// Restore puts the working tree back to the snapshot: changed and deleted
// files are restored, files created since are removed. The index and
// branch are left alone.
func Restore(ctx context.Context, dir, commit string) error {
	root := Root(ctx, dir)
	if root == "" {
		return fmt.Errorf("not a git repository")
	}
	t, err := tree(ctx, root)
	if err != nil {
		return err
	}
	added, err := git(ctx, root, nil, "diff", "--name-only", "--diff-filter=A", "-z", commit, t)
	if err != nil {
		return err
	}
	for _, f := range strings.Split(added, "\x00") {
		if f != "" {
			os.Remove(filepath.Join(root, f))
		}
	}
	_, err = git(ctx, root, nil, "restore", "--source="+commit, "--worktree", "--", ":/")
	return err
}
