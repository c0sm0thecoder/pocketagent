// Package discover suggests folders the user probably wants as projects:
// folders where coding agents have run before, and the folders inside the
// usual code directories.
package discover

import (
	"bufio"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Candidate is a suggested project folder.
type Candidate struct {
	Name   string
	Path   string
	Reason string // why it was suggested, e.g. "recent agent sessions"
	Seen   time.Time
}

// HistoryDir is where an agent keeps session logs as JSONL files that
// record the working directory in a "cwd" field. Locations are data so no
// agent is special; add one when an agent stores history that way.
type HistoryDir struct {
	Agent string
	Dir   string // relative to the home directory
}

// HistoryDirs are the known agent history locations.
var HistoryDirs = []HistoryDir{
	{Agent: "Claude Code", Dir: ".claude/projects"},
	{Agent: "Codex", Dir: ".codex/sessions"},
}

// CodeDirs are common parents of project folders, relative to home.
var CodeDirs = []string{"projects", "Projects", "code", "Code", "src", "dev", "work", "repos", "git"}

// Options tunes a search. Zero values use sensible defaults.
type Options struct {
	Home    string   // defaults to the user's home directory
	Exclude []string // paths already registered as projects
	Limit   int      // maximum suggestions; default 12
}

// Suggest returns candidate project folders, most recently used first.
func Suggest(o Options) []Candidate {
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.Limit == 0 {
		o.Limit = 12
	}
	// Folders are compared by identity, not by path text: on case-insensitive
	// file systems ~/projects and ~/Projects are the same folder.
	type entry struct {
		Candidate
		info os.FileInfo
	}
	var found []*entry
	var excluded []os.FileInfo
	for _, p := range o.Exclude {
		if fi, err := os.Stat(p); err == nil {
			excluded = append(excluded, fi)
		}
	}
	same := func(fi os.FileInfo) func(os.FileInfo) bool {
		return func(other os.FileInfo) bool { return os.SameFile(fi, other) }
	}
	add := func(path, reason string, seen time.Time) {
		path = filepath.Clean(path)
		if !usable(path, o.Home) {
			return
		}
		fi, err := os.Stat(path)
		if err != nil || slices.ContainsFunc(excluded, same(fi)) {
			return
		}
		for _, e := range found {
			if os.SameFile(e.info, fi) {
				if seen.After(e.Seen) {
					e.Seen = seen
				}
				return
			}
		}
		found = append(found, &entry{Candidate{Name: filepath.Base(path), Path: path, Reason: reason, Seen: seen}, fi})
	}

	for _, h := range HistoryDirs {
		for path, seen := range historyCwds(filepath.Join(o.Home, h.Dir)) {
			add(path, "recent "+h.Agent+" sessions", seen)
		}
	}
	for _, d := range CodeDirs {
		entries, err := os.ReadDir(filepath.Join(o.Home, d))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := filepath.Join(o.Home, d, e.Name())
			reason := "in ~/" + d
			if isGitRepo(path) {
				reason = "git repo in ~/" + d
			}
			info, _ := e.Info()
			var seen time.Time
			if info != nil {
				seen = info.ModTime()
			}
			add(path, reason, seen)
		}
	}

	paths := make([]string, len(found))
	for i, e := range found {
		paths[i] = e.Path
	}
	out := make([]Candidate, 0, len(found))
	for _, e := range found {
		if !nestedNoise(e.Path, paths) {
			out = append(out, e.Candidate)
		}
	}
	slices.SortFunc(out, func(a, b Candidate) int {
		if c := b.Seen.Compare(a.Seen); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(out) > o.Limit {
		out = out[:o.Limit]
	}
	uniqueNames(out)
	return out
}

// nestedNoise reports whether path sits inside another candidate and should
// be folded into it: sessions started in docs/ or src/ belong to the
// project. A nested git repository under a non-git umbrella folder (a
// monorepo-style parent) is kept, since it is a project of its own.
func nestedNoise(path string, all []string) bool {
	for _, parent := range all {
		if parent == path || !strings.HasPrefix(path, parent+string(filepath.Separator)) {
			continue
		}
		if isGitRepo(path) && !isGitRepo(parent) {
			continue
		}
		return true
	}
	return false
}

// usable rejects folders that make poor projects: missing, temporary,
// hidden, or containers like the home directory itself.
func usable(path, home string) bool {
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	under := func(p, dir string) bool {
		dir = filepath.Clean(dir)
		return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	if !under(path, home) {
		for _, tmp := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"} {
			if under(path, tmp) {
				return false
			}
		}
	}
	containers := []string{home, "/"}
	for _, d := range append(slices.Clone(CodeDirs), "Downloads", "Desktop", "Documents") {
		containers = append(containers, filepath.Join(home, d))
	}
	if slices.Contains(containers, path) {
		return false
	}
	rel, err := filepath.Rel(home, path)
	if err == nil && !strings.HasPrefix(rel, "..") {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return false
			}
		}
	}
	return true
}

func isGitRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// uniqueNames makes names distinct by prefixing the parent folder.
func uniqueNames(cs []Candidate) {
	count := map[string]int{}
	for _, c := range cs {
		count[c.Name]++
	}
	for i, c := range cs {
		if count[c.Name] > 1 {
			cs[i].Name = filepath.Base(filepath.Dir(c.Path)) + "-" + c.Name
		}
	}
}

var cwdField = regexp.MustCompile(`"cwd"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// maxHistoryFiles bounds how many session logs are read.
const maxHistoryFiles = 500

// historyCwds reads the working directory from the most recent session logs.
func historyCwds(dir string) map[string]time.Time {
	type logFile struct {
		path string
		mod  time.Time
	}
	var files []logFile
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // unreadable entries are skipped
		}
		if info, err := d.Info(); err == nil {
			files = append(files, logFile{path, info.ModTime()})
		}
		return nil
	})
	slices.SortFunc(files, func(a, b logFile) int { return b.mod.Compare(a.mod) })
	if len(files) > maxHistoryFiles {
		files = files[:maxHistoryFiles]
	}
	out := map[string]time.Time{}
	for _, f := range files {
		cwd := firstCwd(f.path)
		if cwd == "" {
			continue
		}
		if t, ok := out[cwd]; !ok || f.mod.After(t) {
			out[cwd] = f.mod
		}
	}
	return out
}

// firstCwd returns the first "cwd" value in the head of a JSONL file.
func firstCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head, _ := io.ReadAll(io.LimitReader(bufio.NewReader(f), 64<<10))
	m := cwdField.FindSubmatch(head)
	if m == nil {
		return ""
	}
	return strings.ReplaceAll(string(m[1]), `\/`, "/")
}
