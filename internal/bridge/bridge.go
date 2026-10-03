// Package bridge serves tools to agent processes over MCP on localhost.
//
// Each agent gets its own namespace (/mcp/<agent>) holding the shared tools
// plus any the agent contributes via agent.ToolProvider. A tool call is
// routed to the Handler of the conversation that made it.
package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

// Name is the MCP server name agents see.
const Name = "pocketagent"

const convHeader = "X-Pocketagent-Conv"

type Bridge struct {
	secret string
	host   string
	addr   string
	shared []agent.Tool

	mu       sync.Mutex
	servers  map[string]*mcp.Server // by agent name
	handlers map[string]agent.Handler
}

// Start listens on a random port. host is how agents reach it: "" or
// 127.0.0.1 normally, host.docker.internal for agents in containers.
// shared tools are offered to every agent.
func Start(host string, shared ...agent.Tool) (*Bridge, error) {
	buf := make([]byte, 24)
	rand.Read(buf)
	if host == "" {
		host = "127.0.0.1"
	}
	b := &Bridge{
		secret: hex.EncodeToString(buf), host: host, shared: shared,
		servers: map[string]*mcp.Server{}, handlers: map[string]agent.Handler{},
	}

	listen := "127.0.0.1:0"
	if host != "127.0.0.1" && host != "localhost" {
		listen = ":0" // containers reach the host through a bridge interface
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", listen)
	if err != nil {
		return nil, err
	}
	b.addr = ln.Addr().String()

	mcpHandler := mcp.NewStreamableHTTPHandler(b.serverFor, nil)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			want := []byte("Bearer " + b.secret)
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       5 * time.Minute,
		// No write timeout: an approval call legitimately waits for the user.
	}
	go func() {
		log.Printf("tool server stopped: %v", srv.Serve(ln))
	}()
	return b, nil
}

// AddAgent creates the namespace for an agent with the shared tools plus extra.
func (b *Bridge) AddAgent(name string, extra []agent.Tool) {
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: "1"}, nil)
	for _, t := range append(append([]agent.Tool{}, b.shared...), extra...) {
		s.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.Schema}, b.call(t))
	}
	b.mu.Lock()
	b.servers[name] = s
	b.mu.Unlock()
}

func (b *Bridge) serverFor(r *http.Request) *mcp.Server {
	name := strings.TrimPrefix(r.URL.Path, "/mcp/")
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.servers[name]
}

// Attach routes tool calls from conv's agent to h until detach is called.
func (b *Bridge) Attach(conv string, h agent.Handler) (detach func()) {
	b.mu.Lock()
	b.handlers[conv] = h
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.handlers[conv] == h {
			delete(b.handlers, conv)
		}
		b.mu.Unlock()
	}
}

// Server is how agentName, running for conv, connects.
func (b *Bridge) Server(agentName, conv string) *agent.ToolServer {
	_, port, _ := net.SplitHostPort(b.addr)
	return &agent.ToolServer{
		Name:    Name,
		URL:     "http://" + net.JoinHostPort(b.host, port) + "/mcp/" + agentName,
		Headers: map[string]string{"Authorization": "Bearer " + b.secret, convHeader: conv},
	}
}

func (b *Bridge) call(t agent.Tool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Extra == nil || req.Extra.Header == nil {
			return nil, fmt.Errorf("missing request headers")
		}
		conv := req.Extra.Header.Get(convHeader)
		b.mu.Lock()
		h := b.handlers[conv]
		b.mu.Unlock()
		if h == nil {
			return nil, fmt.Errorf("no active conversation %q", conv)
		}
		out, err := t.Call(ctx, h, req.Params.Arguments)
		if err != nil {
			// MCP reports tool failures as results, so the agent can read them.
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // see above
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
	}
}
