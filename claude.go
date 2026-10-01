package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ContentBlock is a user message block in the Anthropic message format,
// which is what `claude -p --input-format stream-json` accepts.
type ContentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// RunEvents receives what Claude does during a turn, in order.
type RunEvents struct {
	OnSession func(sessionID string)
	OnText    func(text string)
	OnToolUse func(name string, input json.RawMessage)
}

type RunResult struct {
	SessionID string
	CostUSD   float64
	NumTurns  int
	IsError   bool
	Text      string
}

type RunOptions struct {
	Bin            string
	Cwd            string
	SessionID      string // resumed if set
	Model          string
	AllowedTools   []string
	MCPConfig      string // JSON
	PermissionTool string
}

type streamEvent struct {
	Type      string  `json:"type"`
	Subtype   string  `json:"subtype"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	CostUSD   float64 `json:"total_cost_usd"`
	NumTurns  int     `json:"num_turns"`
	Message   *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

var errNoConversation = errors.New("session not found")

// runClaude runs one turn of a headless Claude Code session and streams
// events as they arrive. Cancelling ctx kills the process.
func runClaude(ctx context.Context, opts RunOptions, content []ContentBlock, ev RunEvents) (*RunResult, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.MCPConfig != "" {
		args = append(args, "--mcp-config", opts.MCPConfig)
	}
	if opts.PermissionTool != "" {
		args = append(args, "--permission-prompt-tool", opts.PermissionTool)
	}

	cmd := exec.CommandContext(ctx, opts.Bin, args...)
	cmd.Dir = opts.Cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	msg := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	}
	if err := json.NewEncoder(stdin).Encode(msg); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("write prompt: %w", err)
	}
	// Closing stdin tells claude this is the only message for this turn.
	stdin.Close()

	res := &RunResult{}
	gotResult := false
	r := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e streamEvent
			if json.Unmarshal(line, &e) == nil {
				switch e.Type {
				case "system":
					if e.Subtype == "init" && e.SessionID != "" {
						res.SessionID = e.SessionID
						if ev.OnSession != nil {
							ev.OnSession(e.SessionID)
						}
					}
				case "assistant":
					if e.Message == nil {
						break
					}
					for _, b := range e.Message.Content {
						switch b.Type {
						case "text":
							if strings.TrimSpace(b.Text) != "" && ev.OnText != nil {
								ev.OnText(b.Text)
							}
						case "tool_use":
							if ev.OnToolUse != nil {
								ev.OnToolUse(b.Name, b.Input)
							}
						}
					}
				case "result":
					gotResult = true
					if e.SessionID != "" {
						res.SessionID = e.SessionID
					}
					res.CostUSD = e.CostUSD
					res.NumTurns = e.NumTurns
					res.IsError = e.IsError
					res.Text = e.Result
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
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
			return res, errNoConversation
		}
		if msg == "" {
			msg = waitErr.Error()
		}
		return res, fmt.Errorf("claude exited: %s", msg)
	}
	return res, nil
}
