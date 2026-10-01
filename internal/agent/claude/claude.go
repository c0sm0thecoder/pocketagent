// Package claude runs the Claude Code CLI headless with stream-json I/O.
// Approvals arrive through the pocketagent MCP bridge, which Claude Code
// calls as its --permission-prompt-tool.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type Agent struct {
	cfg config.Agent
}

func New(cfg config.Agent) *Agent {
	if len(cfg.Command) == 0 {
		cfg.Command = config.Command{"claude"}
	}
	if cfg.Models == nil {
		cfg.Models = []string{"opus", "sonnet", "haiku"}
	}
	return &Agent{cfg: cfg}
}

func (a *Agent) Caps() agent.Caps {
	return agent.Caps{
		Images:      true,
		Resume:      true,
		DenyMessage: true,
		Modes:       []agent.Mode{agent.ModeAsk, agent.ModeEdits, agent.ModePlan, agent.ModeYolo},
	}
}

func (a *Agent) Models(string) []string { return a.cfg.Models }
func (a *Agent) Close()                 {}

// ErrNoConversation means the session to resume does not exist (for example
// because it belongs to another directory).
var ErrNoConversation = errors.New("session not found")

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type streamEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"total_cost_usd"`
	Message   *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

func (a *Agent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	res, err := a.run(ctx, req, h)
	if errors.Is(err, ErrNoConversation) {
		req.SessionID = ""
		h.Text("_Previous session not found, starting a new one._")
		return a.run(ctx, req, h)
	}
	return res, err
}

func (a *Agent) args(req agent.Request) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	model := req.Model
	if model == "" {
		model = a.cfg.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	switch req.Mode {
	case agent.ModeEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.ModePlan:
		args = append(args, "--permission-mode", "plan")
	case agent.ModeYolo:
		args = append(args, "--permission-mode", "bypassPermissions")
	}
	allowed := append([]string{}, req.AlwaysAllow...)
	if req.MCP != nil {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			req.MCP.Name: map[string]any{"type": "http", "url": req.MCP.URL, "headers": req.MCP.Headers},
		}})
		args = append(args, "--mcp-config", string(cfg))
		args = append(args, "--permission-prompt-tool", "mcp__"+req.MCP.Name+"__approve")
		allowed = append(allowed, "mcp__"+req.MCP.Name+"__send_file")
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	return args
}

func (a *Agent) run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	argv := append(config.Expand(a.cfg.Wrap, map[string]string{"cwd": req.Cwd}), a.cfg.Command...)
	argv = append(argv, a.args(req)...)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), a.cfg.Env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agent.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return agent.Result{}, fmt.Errorf("start %s: %w", argv[0], err)
	}

	var content []contentBlock
	for _, b := range req.Prompt {
		if b.IsImage() {
			content = append(content, contentBlock{Type: "image", Source: &imageSource{
				Type: "base64", MediaType: b.MimeType, Data: base64.StdEncoding.EncodeToString(b.Image),
			}})
		} else if b.Text != "" {
			content = append(content, contentBlock{Type: "text", Text: b.Text})
		}
	}
	msg := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}}
	if err := json.NewEncoder(stdin).Encode(msg); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return agent.Result{}, fmt.Errorf("write prompt: %w", err)
	}
	// Closing stdin tells claude this is the only message for this turn.
	stdin.Close()

	res := agent.Result{}
	gotResult, sentText := false, false
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		var e streamEvent
		if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &e) == nil {
			switch e.Type {
			case "system":
				if e.Subtype == "init" && e.SessionID != "" {
					res.SessionID = e.SessionID
					h.Session(e.SessionID)
				}
			case "assistant":
				if e.Message == nil {
					break
				}
				for _, b := range e.Message.Content {
					switch b.Type {
					case "text":
						if strings.TrimSpace(b.Text) != "" {
							sentText = true
							h.Text(b.Text)
						}
					case "tool_use":
						var input map[string]any
						json.Unmarshal(b.Input, &input)
						p := Describe(b.Name, input)
						h.Tool(p.Title, p.Kind)
					}
				}
			case "result":
				gotResult = true
				if e.SessionID != "" {
					res.SessionID = e.SessionID
				}
				res.CostUSD = e.CostUSD
				res.StopReason = e.Subtype
				if e.IsError || !sentText {
					res.FinalText = e.Result
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				cmd.Process.Kill()
			}
			break
		}
	}

	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if waitErr != nil && !gotResult {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "No conversation found") {
			return res, ErrNoConversation
		}
		if msg == "" {
			msg = waitErr.Error()
		}
		return res, fmt.Errorf("claude exited: %s", msg)
	}
	return res, nil
}

// Describe turns a Claude Code tool call into a Permission with a kind,
// a one-line title and a readable detail.
func Describe(tool string, input map[string]any) agent.Permission {
	str := func(k string) string { s, _ := input[k].(string); return s }
	p := agent.Permission{Tool: tool, Key: tool, Kind: agent.KindOther, Title: tool, Options: agent.DefaultOptions()}
	short := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		return s
	}
	switch tool {
	case "Bash":
		p.Kind = agent.KindExecute
		p.Title = "Bash: " + short(str("command"))
		p.Detail = str("command")
		if d := str("description"); d != "" {
			p.Detail = "# " + d + "\n" + p.Detail
		}
		return p
	case "Read", "NotebookRead":
		p.Kind = agent.KindRead
		p.Title = tool + ": " + str("file_path")
	case "Glob", "Grep", "LS":
		p.Kind = agent.KindSearch
		p.Title = tool + ": " + short(str("pattern")+str("path"))
	case "Edit", "MultiEdit":
		p.Kind = agent.KindEdit
		p.Title = tool + ": " + str("file_path")
		p.Detail = str("file_path") + "\n- " + str("old_string") + "\n+ " + str("new_string")
		return p
	case "Write":
		p.Kind = agent.KindEdit
		p.Title = "Write: " + str("file_path")
		p.Detail = str("file_path") + "\n" + str("content")
		return p
	case "NotebookEdit":
		p.Kind = agent.KindEdit
		p.Title = "NotebookEdit: " + str("notebook_path")
	case "WebFetch":
		p.Kind = agent.KindFetch
		p.Title = "WebFetch: " + str("url")
	case "WebSearch":
		p.Kind = agent.KindFetch
		p.Title = "WebSearch: " + short(str("query"))
	case "TodoWrite", "Task", "Agent":
		p.Kind = agent.KindThink
		if d := str("description"); d != "" {
			p.Title = tool + ": " + short(d)
		}
	}
	data, _ := json.MarshalIndent(input, "", "  ")
	p.Detail = string(data)
	return p
}
