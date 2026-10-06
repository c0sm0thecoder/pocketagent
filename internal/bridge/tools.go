package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

// SendFile lets an agent send a file from this machine to the conversation.
var SendFile = agent.Tool{
	Name: "send_file",
	Description: "Send a file from this machine to the user's chat. Images (png/jpg/gif/webp) " +
		"are shown inline; anything else is sent as a document. Use it when the user asks to " +
		"see a screenshot, chart, image or file.",
	// Sends to the user's chat: changes nothing locally, but leaves the
	// machine, and sending twice sends two messages.
	Hints: agent.ToolHints{Title: "Send a file to the chat", OpenWorld: true},
	Schema: map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "absolute path of the file to send"},
			"caption": map[string]any{"type": "string", "description": "optional short caption"},
		},
	},
	Call: func(ctx context.Context, h agent.Handler, args json.RawMessage) (string, error) {
		var in struct {
			Path    string `json:"path"`
			Caption string `json:"caption"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return "", err
		}
		if !filepath.IsAbs(in.Path) {
			return "", fmt.Errorf("path must be absolute")
		}
		if err := h.SendFile(ctx, in.Path, in.Caption); err != nil {
			return "", err
		}
		return "sent", nil
	},
}
