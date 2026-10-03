// Package claudecode drives the Claude Code CLI headless, using its
// stream-json input and output. Approvals reach the user through a tool the
// adapter contributes to pocketagent's tool server, which Claude Code calls
// as its --permission-prompt-tool.
package claudecode

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
	"github.com/c0sm0thecoder/pocketagent/internal/argv"
)

const approvalTool = "approve"

type Agent struct {
	spec agent.Spec
}

var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.ToolProvider = (*Agent)(nil)
)

// New builds the adapter. It has no options of its own.
func New(spec agent.Spec) (agent.Agent, error) {
	var opts struct{}
	if err := spec.Options.Decode(&opts); err != nil {
		return nil, err
	}
	return &Agent{spec: spec}, nil
}

func (a *Agent) Caps() agent.Caps {
	return agent.Caps{Images: true, Resume: true, DenyMessage: true, Modes: agent.Modes}
}

// Tools contributes the permission-prompt hook.
func (a *Agent) Tools() []agent.Tool {
	return []agent.Tool{{
		Name:        approvalTool,
		Description: "Internal: asks the user to approve a tool call. Do not call directly.",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"tool_name": map[string]any{"type": "string"},
			"input":     map[string]any{"type": "object"},
		}},
		Call: approve,
	}}
}

// approve implements the --permission-prompt-tool contract: input is
// {tool_name, input}; the reply is JSON with behavior "allow" (plus
// updatedInput) or "deny" (plus message).
func approve(ctx context.Context, h agent.Handler, args json.RawMessage) (string, error) {
	var in struct {
		ToolName string         `json:"tool_name"`
		Input    map[string]any `json:"input"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.Input == nil {
		in.Input = map[string]any{}
	}
	p := Describe(in.ToolName, in.Input)
	d := h.Permission(ctx, p)
	reply := map[string]any{"behavior": "deny", "message": "The user denied this action."}
	if o, ok := p.Find(d.OptionID); ok && o.Kind.Allows() {
		reply = map[string]any{"behavior": "allow", "updatedInput": in.Input}
	} else if d.Message != "" {
		reply["message"] = "The user denied this and said: " + d.Message
	}
	data, _ := json.Marshal(reply)
	return string(data), nil
}

// errNoConversation means the session to resume does not exist (for example
// because it belongs to another directory).
var errNoConversation = errors.New("session not found")

func (a *Agent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	res, err := a.run(ctx, req, h)
	if errors.Is(err, errNoConversation) {
		req.SessionID = ""
		h.Message("_Previous session not found, starting a new one._")
		return a.run(ctx, req, h)
	}
	return res, err
}

func (a *Agent) args(req agent.Request) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose"}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if mode, ok := permissionModes[req.Mode]; ok {
		args = append(args, "--permission-mode", mode)
	}
	allowed := append([]string{}, req.AlwaysAllow...)
	if t := req.Tools; t != nil {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			t.Name: map[string]any{"type": "http", "url": t.URL, "headers": t.Headers},
		}})
		prefix := "mcp__" + t.Name + "__"
		args = append(args, "--mcp-config", string(cfg), "--permission-prompt-tool", prefix+approvalTool)
		allowed = append(allowed, prefix+"send_file")
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools", strings.Join(allowed, ","))
	}
	return args
}

var permissionModes = map[agent.Mode]string{
	agent.ModeEdits: "acceptEdits",
	agent.ModePlan:  "plan",
	agent.ModeFull:  "bypassPermissions",
}

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

func userMessage(blocks []agent.Block) map[string]any {
	var content []contentBlock
	for _, b := range blocks {
		switch {
		case b.IsImage():
			content = append(content, contentBlock{Type: "image", Source: &imageSource{
				Type: "base64", MediaType: b.MimeType, Data: base64.StdEncoding.EncodeToString(b.Image),
			}})
		case b.Text != "":
			content = append(content, contentBlock{Type: "text", Text: b.Text})
		}
	}
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}}
}

type event struct {
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

func (a *Agent) run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	cmdline := append(argv.Join(a.spec.Wrap, a.spec.Command, map[string]string{"cwd": req.Cwd}), a.args(req)...)
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), a.spec.Env...)
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
		return agent.Result{}, fmt.Errorf("start %s: %w", cmdline[0], err)
	}
	if err := json.NewEncoder(stdin).Encode(userMessage(req.Prompt)); err != nil {
		_ = cmd.Process.Kill() // the prompt error below is what matters
		_ = cmd.Wait()
		return agent.Result{}, fmt.Errorf("write prompt: %w", err)
	}
	stdin.Close() // one message per turn

	res, gotResult := a.read(stdout, h)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if waitErr != nil && !gotResult {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "No conversation found") {
			return res, errNoConversation
		}
		if msg == "" {
			msg = waitErr.Error()
		}
		return res, fmt.Errorf("%s exited: %s", cmdline[0], msg)
	}
	return res, nil
}

// read consumes the stream-json events until EOF.
func (a *Agent) read(r io.Reader, h agent.Handler) (res agent.Result, gotResult bool) {
	sentText := false
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		var e event
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
							h.Message(b.Text)
						}
					case "tool_use":
						var input map[string]any
						_ = json.Unmarshal(b.Input, &input) // unparsable input still gets a title
						p := Describe(b.Name, input)
						h.ToolCall(p.Title, p.Kind)
					}
				}
			case "result":
				gotResult = true
				if e.SessionID != "" {
					res.SessionID = e.SessionID
				}
				res.CostUSD, res.StopReason = e.CostUSD, e.Subtype
				if e.IsError || !sentText {
					res.Final = e.Result
				}
			}
		}
		if err != nil {
			return res, gotResult
		}
	}
}

// Describe turns a Claude Code tool call into a Permission with a kind,
// a one-line title and a readable detail.
func Describe(tool string, input map[string]any) agent.Permission {
	str := func(k string) string { s, _ := input[k].(string); return s }
	short := func(s string) string {
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		return s
	}
	p := agent.Permission{Tool: tool, Key: tool, Kind: agent.KindOther, Title: tool, Options: agent.DefaultOptions()}
	switch tool {
	case "Bash":
		p.Kind, p.Title, p.Detail = agent.KindExecute, "Bash: "+short(str("command")), str("command")
		if d := str("description"); d != "" {
			p.Detail = "# " + d + "\n" + p.Detail
		}
		return p
	case "Edit", "MultiEdit":
		p.Kind, p.Title = agent.KindEdit, tool+": "+str("file_path")
		p.Detail = str("file_path") + "\n- " + str("old_string") + "\n+ " + str("new_string")
		return p
	case "Write":
		p.Kind, p.Title = agent.KindEdit, "Write: "+str("file_path")
		p.Detail = str("file_path") + "\n" + str("content")
		return p
	case "Read", "NotebookRead":
		p.Kind, p.Title = agent.KindRead, tool+": "+str("file_path")
	case "Glob", "Grep", "LS":
		p.Kind, p.Title = agent.KindSearch, tool+": "+short(str("pattern")+str("path"))
	case "NotebookEdit":
		p.Kind, p.Title = agent.KindEdit, "NotebookEdit: "+str("notebook_path")
	case "WebFetch":
		p.Kind, p.Title = agent.KindFetch, "WebFetch: "+str("url")
	case "WebSearch":
		p.Kind, p.Title = agent.KindFetch, "WebSearch: "+short(str("query"))
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
