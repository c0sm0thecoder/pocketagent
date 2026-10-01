// Package bridge runs a localhost MCP server that agent processes connect
// to. It provides send_file (so agents can send files to the chat) and
// approve (Claude Code's --permission-prompt-tool).
package bridge

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
	"sync"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/claude"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Name = "pocketagent"

// Endpoint is what a conversation exposes to its agent while a turn runs.
type Endpoint struct {
	Handler  agent.Handler
	SendFile func(ctx context.Context, path, caption string) error
}

type Bridge struct {
	secret string
	addr   string
	host   string

	mu        sync.Mutex
	endpoints map[string]*Endpoint
}

// Start listens on 127.0.0.1. host is the hostname agents use to reach it
// ("127.0.0.1" normally, "host.docker.internal" for sandboxed agents).
func Start(host string) (*Bridge, error) {
	buf := make([]byte, 24)
	rand.Read(buf)
	if host == "" {
		host = "127.0.0.1"
	}
	b := &Bridge{secret: hex.EncodeToString(buf), host: host, endpoints: map[string]*Endpoint{}}

	server := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "approve",
		Description: "Internal: asks the user to approve a tool call. Do not call directly.",
	}, b.handleApprove)
	mcp.AddTool(server, &mcp.Tool{
		Name: "send_file",
		Description: "Send a file from this machine to the user's chat. Images (png/jpg/gif/webp) " +
			"are shown inline; anything else is sent as a document. Use it when the user asks to " +
			"see a screenshot, chart, image or file.",
	}, b.handleSendFile)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	listenHost := "127.0.0.1"
	if host != "127.0.0.1" && host != "localhost" {
		// Containers reach the host through a bridge interface, not loopback.
		listenHost = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", listenHost+":0")
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

// Register exposes ep to the agent running for conv until the returned
// function is called.
func (b *Bridge) Register(conv string, ep *Endpoint) (unregister func()) {
	b.mu.Lock()
	b.endpoints[conv] = ep
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.endpoints[conv] == ep {
			delete(b.endpoints, conv)
		}
		b.mu.Unlock()
	}
}

// Server describes how the agent for conv connects to the bridge.
func (b *Bridge) Server(conv string) *agent.MCPServer {
	_, port, _ := net.SplitHostPort(b.addr)
	return &agent.MCPServer{
		Name: Name,
		URL:  "http://" + net.JoinHostPort(b.host, port) + "/mcp",
		Headers: map[string]string{
			"Authorization":      "Bearer " + b.secret,
			"X-Pocketagent-Conv": conv,
		},
	}
}

func (b *Bridge) endpoint(req *mcp.CallToolRequest) (*Endpoint, error) {
	if req.Extra == nil || req.Extra.Header == nil {
		return nil, fmt.Errorf("missing request headers")
	}
	conv := req.Extra.Header.Get("X-Pocketagent-Conv")
	b.mu.Lock()
	ep := b.endpoints[conv]
	b.mu.Unlock()
	if ep == nil {
		return nil, fmt.Errorf("no active conversation %q", conv)
	}
	return ep, nil
}

func text(v any) *mcp.CallToolResult {
	s, ok := v.(string)
	if !ok {
		data, _ := json.Marshal(v)
		s = string(data)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// handleApprove implements Claude Code's --permission-prompt-tool contract:
// input is {tool_name, input, tool_use_id}; the reply is a JSON string with
// behavior "allow" (plus updatedInput) or "deny" (plus message).
func (b *Bridge) handleApprove(ctx context.Context, req *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
	deny := func(msg string) (*mcp.CallToolResult, any, error) {
		return text(map[string]any{"behavior": "deny", "message": msg}), nil, nil
	}
	ep, err := b.endpoint(req)
	if err != nil {
		return deny("approval bridge error: " + err.Error())
	}
	tool, _ := in["tool_name"].(string)
	input, _ := in["input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}

	p := claude.Describe(tool, input)
	d := ep.Handler.Permission(ctx, p)
	for _, o := range p.Options {
		if o.ID == d.OptionID && o.Kind.Allows() {
			return text(map[string]any{"behavior": "allow", "updatedInput": input}), nil, nil
		}
	}
	if d.Message != "" {
		return deny("The user denied this and said: " + d.Message)
	}
	return deny("The user denied this action.")
}

type sendFileIn struct {
	Path    string `json:"path" jsonschema:"absolute path of the file to send"`
	Caption string `json:"caption,omitempty" jsonschema:"optional short caption"`
}

func (b *Bridge) handleSendFile(ctx context.Context, req *mcp.CallToolRequest, in sendFileIn) (*mcp.CallToolResult, any, error) {
	ep, err := b.endpoint(req)
	if err != nil {
		return nil, nil, err
	}
	if err := ep.SendFile(ctx, in.Path, in.Caption); err != nil {
		return nil, nil, err
	}
	return text("sent"), nil, nil
}
