package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChatUI is the Telegram side that the MCP tools call back into.
type ChatUI interface {
	// AskApproval blocks until the user taps Allow/Deny, ctx is cancelled,
	// or the approval times out.
	AskApproval(ctx context.Context, chatID int64, tool string, input map[string]any) (allow bool, reason string)
	SendFile(ctx context.Context, chatID int64, path, caption string) error
}

const (
	mcpServerName      = "tg"
	permissionToolName = "mcp__tg__approve"
)

// MCPBridge is a localhost MCP server that every claude process connects to.
// It provides the permission prompt tool (so approvals show up as Telegram
// buttons) and a send_file tool so Claude can send images and files back.
// Each claude process gets its chat id in a header, which tells the bridge
// which chat a call belongs to.
type MCPBridge struct {
	ui     ChatUI
	secret string
	addr   string
}

func startMCPBridge(ui ChatUI) (*MCPBridge, error) {
	buf := make([]byte, 24)
	rand.Read(buf)
	b := &MCPBridge{ui: ui, secret: hex.EncodeToString(buf)}

	server := mcp.NewServer(&mcp.Implementation{Name: "claude-telegram", Version: "0.1.0"}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "approve",
		Description: "Internal: asks the Telegram user to approve a tool call. Do not call directly.",
	}, b.handleApprove)

	mcp.AddTool(server, &mcp.Tool{
		Name: "send_file",
		Description: "Send a file from this machine to the user's Telegram chat. " +
			"Images (png/jpg/gif/webp) are shown inline as photos; anything else is sent as a document. " +
			"Use this when the user asks to see a screenshot, chart, image or file.",
	}, b.handleSendFile)

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b.addr = ln.Addr().String()

	go func() {
		err := http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			want := "Bearer " + b.secret
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			handler.ServeHTTP(w, r)
		}))
		log.Printf("mcp bridge stopped: %v", err)
	}()
	return b, nil
}

// ConfigFor returns the --mcp-config JSON for a claude process serving chatID.
func (b *MCPBridge) ConfigFor(chatID int64) string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			mcpServerName: map[string]any{
				"type": "http",
				"url":  "http://" + b.addr + "/mcp",
				"headers": map[string]string{
					"Authorization": "Bearer " + b.secret,
					"X-Chat-ID":     strconv.FormatInt(chatID, 10),
				},
			},
		},
	}
	data, _ := json.Marshal(cfg)
	return string(data)
}

func chatIDFrom(req *mcp.CallToolRequest) (int64, error) {
	if req.Extra == nil || req.Extra.Header == nil {
		return 0, fmt.Errorf("missing request headers")
	}
	return strconv.ParseInt(req.Extra.Header.Get("X-Chat-ID"), 10, 64)
}

func textResult(v any) *mcp.CallToolResult {
	s, ok := v.(string)
	if !ok {
		data, _ := json.Marshal(v)
		s = string(data)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// handleApprove implements Claude Code's --permission-prompt-tool contract:
// input is {tool_name, input, tool_use_id}, output is a JSON string with
// behavior "allow" (plus updatedInput) or "deny" (plus message).
func (b *MCPBridge) handleApprove(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
	chatID, err := chatIDFrom(req)
	if err != nil {
		return textResult(map[string]any{"behavior": "deny", "message": "approval bridge error: " + err.Error()}), nil, nil
	}
	tool, _ := in["tool_name"].(string)
	input, _ := in["input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}

	allow, reason := b.ui.AskApproval(ctx, chatID, tool, input)
	if allow {
		return textResult(map[string]any{"behavior": "allow", "updatedInput": input}), nil, nil
	}
	if reason == "" {
		reason = "The user denied this action."
	}
	return textResult(map[string]any{"behavior": "deny", "message": reason}), nil, nil
}

type sendFileIn struct {
	Path    string `json:"path" jsonschema:"absolute path of the file to send"`
	Caption string `json:"caption,omitempty" jsonschema:"optional short caption"`
}

func (b *MCPBridge) handleSendFile(ctx context.Context, req *mcp.CallToolRequest, in sendFileIn) (*mcp.CallToolResult, any, error) {
	chatID, err := chatIDFrom(req)
	if err != nil {
		return nil, nil, err
	}
	if err := b.ui.SendFile(ctx, chatID, in.Path, in.Caption); err != nil {
		return nil, nil, err
	}
	return textResult("sent"), nil, nil
}
