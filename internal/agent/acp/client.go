package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

// client receives calls from the agent and forwards them to the handler of
// the turn in progress.
type client struct {
	proc *proc

	mu       sync.Mutex
	ctx      context.Context
	h        agent.Handler
	suppress bool
	text     strings.Builder
	costUSD  float64
}

var _ sdk.Client = (*client)(nil)

func (c *client) begin(ctx context.Context, h agent.Handler) {
	c.mu.Lock()
	c.ctx, c.h = ctx, h
	c.text.Reset()
	c.mu.Unlock()
}

func (c *client) end() {
	c.mu.Lock()
	c.h = nil
	c.mu.Unlock()
}

func (c *client) setSuppress(on bool) {
	c.mu.Lock()
	c.suppress = on
	c.mu.Unlock()
}

func (c *client) cost() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.costUSD
}

// flush sends buffered message chunks as one block of text.
func (c *client) flush() {
	c.mu.Lock()
	text, h := c.text.String(), c.h
	c.text.Reset()
	c.mu.Unlock()
	if h != nil && strings.TrimSpace(text) != "" {
		h.Text(text)
	}
}

func (c *client) SessionUpdate(_ context.Context, n sdk.SessionNotification) error {
	c.mu.Lock()
	h, suppress := c.h, c.suppress
	c.mu.Unlock()
	u := n.Update

	// Keep config and mode state current even while replaying history.
	switch {
	case u.ConfigOptionUpdate != nil:
		c.proc.mu.Lock()
		c.proc.options = u.ConfigOptionUpdate.ConfigOptions
		c.proc.mu.Unlock()
	case u.CurrentModeUpdate != nil:
		c.proc.mu.Lock()
		if c.proc.modes != nil {
			c.proc.modes.CurrentModeId = u.CurrentModeUpdate.CurrentModeId
		}
		c.proc.mu.Unlock()
	case u.UsageUpdate != nil && u.UsageUpdate.Cost != nil && strings.EqualFold(u.UsageUpdate.Cost.Currency, "USD"):
		c.mu.Lock()
		c.costUSD = u.UsageUpdate.Cost.Amount
		c.mu.Unlock()
	}
	if suppress || h == nil {
		return nil
	}

	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			c.mu.Lock()
			c.text.WriteString(t.Text)
			c.mu.Unlock()
		}
	case u.ToolCall != nil:
		c.flush()
		h.Tool(u.ToolCall.Title, agent.Kind(u.ToolCall.Kind))
	}
	return nil
}

func (c *client) RequestPermission(_ context.Context, req sdk.RequestPermissionRequest) (sdk.RequestPermissionResponse, error) {
	c.flush()
	c.mu.Lock()
	h, ctx := c.h, c.ctx
	c.mu.Unlock()
	cancelled := sdk.RequestPermissionResponse{Outcome: sdk.RequestPermissionOutcome{Cancelled: &sdk.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}
	if h == nil {
		return cancelled, nil
	}

	p := describe(req.ToolCall)
	for _, o := range req.Options {
		p.Options = append(p.Options, agent.Option{ID: string(o.OptionId), Label: o.Name, Kind: agent.OptionKind(o.Kind)})
	}
	d := h.Permission(ctx, p)
	if d.OptionID == "" {
		return cancelled, nil
	}
	return sdk.RequestPermissionResponse{Outcome: sdk.RequestPermissionOutcome{
		Selected: &sdk.RequestPermissionOutcomeSelected{OptionId: sdk.PermissionOptionId(d.OptionID), Outcome: "selected"},
	}}, nil
}

func describe(tc sdk.ToolCallUpdate) agent.Permission {
	p := agent.Permission{Kind: agent.KindOther}
	if tc.Kind != nil {
		p.Kind = agent.Kind(*tc.Kind)
	}
	if tc.Title != nil {
		p.Title = *tc.Title
	}
	p.Tool = p.Title
	if i := strings.IndexAny(p.Tool, ":("); i > 0 {
		p.Tool = strings.TrimSpace(p.Tool[:i])
	}
	if p.Tool == "" {
		p.Tool = string(p.Kind)
	}
	if len(p.Tool) > 40 {
		p.Tool = p.Tool[:37] + "..."
	}

	// Prefer a diff or the command over raw JSON.
	for _, c := range tc.Content {
		if c.Diff != nil {
			old := ""
			if c.Diff.OldText != nil {
				old = *c.Diff.OldText
			}
			p.Detail += c.Diff.Path + "\n- " + old + "\n+ " + c.Diff.NewText + "\n"
		}
	}
	if p.Detail == "" {
		if m, ok := tc.RawInput.(map[string]any); ok {
			for _, k := range []string{"command", "cmd"} {
				switch v := m[k].(type) {
				case string:
					p.Detail = v
				case []any:
					var parts []string
					for _, x := range v {
						parts = append(parts, toString(x))
					}
					p.Detail = strings.Join(parts, " ")
				}
				if p.Detail != "" {
					break
				}
			}
		}
	}
	if p.Detail == "" && tc.RawInput != nil {
		data, _ := json.MarshalIndent(tc.RawInput, "", "  ")
		p.Detail = string(data)
	}
	return p
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// pocketagent advertises no fs or terminal capabilities, so agents use their
// own. These are never called by compliant agents.
var errUnsupported = errors.New("not supported by pocketagent")

func (c *client) ReadTextFile(context.Context, sdk.ReadTextFileRequest) (sdk.ReadTextFileResponse, error) {
	return sdk.ReadTextFileResponse{}, errUnsupported
}
func (c *client) WriteTextFile(context.Context, sdk.WriteTextFileRequest) (sdk.WriteTextFileResponse, error) {
	return sdk.WriteTextFileResponse{}, errUnsupported
}
func (c *client) CreateTerminal(context.Context, sdk.CreateTerminalRequest) (sdk.CreateTerminalResponse, error) {
	return sdk.CreateTerminalResponse{}, errUnsupported
}
func (c *client) KillTerminal(context.Context, sdk.KillTerminalRequest) (sdk.KillTerminalResponse, error) {
	return sdk.KillTerminalResponse{}, errUnsupported
}
func (c *client) TerminalOutput(context.Context, sdk.TerminalOutputRequest) (sdk.TerminalOutputResponse, error) {
	return sdk.TerminalOutputResponse{}, errUnsupported
}
func (c *client) ReleaseTerminal(context.Context, sdk.ReleaseTerminalRequest) (sdk.ReleaseTerminalResponse, error) {
	return sdk.ReleaseTerminalResponse{}, errUnsupported
}
func (c *client) WaitForTerminalExit(context.Context, sdk.WaitForTerminalExitRequest) (sdk.WaitForTerminalExitResponse, error) {
	return sdk.WaitForTerminalExitResponse{}, errUnsupported
}

func encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func saveTemp(b agent.Block) (string, error) {
	ext := ".png"
	if strings.Contains(b.MimeType, "jpeg") {
		ext = ".jpg"
	}
	f, err := os.CreateTemp("", "pocketagent-image-*"+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(b.Image)
	return f.Name(), err
}
