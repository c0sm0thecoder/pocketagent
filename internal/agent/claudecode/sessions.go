package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

var (
	_ agent.SessionLister = (*Agent)(nil)
	_ agent.ResumeHinter  = (*Agent)(nil)
)

// historyRoot is where Claude Code keeps sessions: one folder per working
// directory under $CLAUDE_CONFIG_DIR/projects (default ~/.claude/projects).
func (a *Agent) historyRoot() string {
	if a.historyDir != "" {
		return a.historyDir
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// maxSessions bounds how many sessions are listed per folder.
const maxSessions = 20

// Sessions lists Claude Code sessions started in cwd, newest first,
// including ones from a terminal.
func (a *Agent) Sessions(_ context.Context, _, cwd string) ([]agent.SessionInfo, error) {
	// Claude Code files sessions under the path it saw, which may be the
	// symlink-resolved one (macOS: /var is /private/var), so look at both.
	paths := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		paths = append(paths, real)
	}
	var out []agent.SessionInfo
	for _, p := range paths {
		dir := filepath.Join(a.historyRoot(), nonAlnum.ReplaceAllString(p, "-"))
		found, err := sessionsIn(dir)
		if err != nil {
			return nil, err
		}
		for _, s := range found {
			if !slices.ContainsFunc(out, func(o agent.SessionInfo) bool { return o.ID == s.ID }) {
				out = append(out, s)
			}
		}
	}
	slices.SortFunc(out, func(x, y agent.SessionInfo) int { return y.Updated.Compare(x.Updated) })
	if len(out) > maxSessions {
		out = out[:maxSessions]
	}
	return out, nil
}

// sessionsIn reads the sessions in one Claude Code project folder.
func sessionsIn(dir string) ([]agent.SessionInfo, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []agent.SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		title := sessionTitle(path, info.Size())
		if title == "" {
			continue // no user prompt yet: nothing worth resuming
		}
		out = append(out, agent.SessionInfo{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Title: title, Updated: info.ModTime()})
	}
	return out, nil
}

// ResumeCommand is how to continue a session in a terminal.
func (a *Agent) ResumeCommand(cwd, sessionID string) string {
	bin := "claude"
	if len(a.spec.Command) > 0 {
		bin = a.spec.Command[0]
	}
	return "cd " + shellQuote(cwd) + " && " + shellQuote(bin) + " --resume " + shellQuote(sessionID)
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?&;|<>()[]{}#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Session logs can be large, so only the head (first prompt) and the tail
// (latest generated or custom title) are read.
const headTailBytes = 256 << 10

type logLine struct {
	Type        string `json:"type"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Summary     string `json:"summary"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// sessionTitle prefers a title the user set, then the latest generated
// title (Claude Code refines it as the session goes), then a summary, then
// the first prompt.
func sessionTitle(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var custom, generated, summary, prompt string
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			var l logLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				continue // includes the partial first line after a seek
			}
			switch l.Type {
			case "custom-title":
				custom = l.CustomTitle
			case "ai-title":
				generated = l.AITitle
			case "summary":
				summary = l.Summary
			case "user":
				if prompt == "" {
					prompt = promptText(l.Message.Content)
				}
			}
		}
	}
	scan(io.LimitReader(f, headTailBytes))
	if size > headTailBytes {
		if _, err := f.Seek(max(size-headTailBytes, headTailBytes), io.SeekStart); err == nil {
			scan(f)
		}
	}
	for _, t := range []string{custom, generated, summary, prompt} {
		if t != "" {
			return t
		}
	}
	return ""
}

// promptText extracts what the user typed, skipping tool results and the
// wrappers Claude Code adds around slash commands.
func promptText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		for _, b := range blocks {
			if b.Type == "text" {
				s = b.Text
				break
			}
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if strings.HasPrefix(s, "<") || strings.HasPrefix(s, "Caveat:") {
		return ""
	}
	if len(s) > 80 {
		s = s[:77] + "..."
	}
	return s
}
