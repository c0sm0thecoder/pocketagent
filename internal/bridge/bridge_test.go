package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

// headerTransport adds the bridge's headers, as an agent's MCP client would.
type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, s *agent.ToolServer) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   s.URL,
		HTTPClient: &http.Client{Transport: headerTransport(s.Headers)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func toolNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// recorder is a Handler that records what reaches it.
type recorder struct {
	agent.Handler // unused methods panic, which is what we want in a test
	conv          string
	files         []string
}

func (r *recorder) SendFile(_ context.Context, path, _ string) error {
	r.files = append(r.files, r.conv+":"+path)
	return nil
}

func TestNamespacesAndRouting(t *testing.T) {
	echo := agent.Tool{
		Name:   "echo",
		Schema: map[string]any{"type": "object"},
		Call: func(_ context.Context, _ agent.Handler, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}
	b, err := Start("", SendFile)
	if err != nil {
		t.Fatal(err)
	}
	b.AddAgent("plain", nil)
	b.AddAgent("special", []agent.Tool{echo})

	// Each agent sees the shared tools plus only its own.
	if got := toolNames(t, connect(t, b.Server("plain", "c1"))); !slices.Equal(got, []string{"send_file"}) {
		t.Errorf("plain sees %v", got)
	}
	special := connect(t, b.Server("special", "c2"))
	if got := toolNames(t, special); !slices.Equal(got, []string{"echo", "send_file"}) {
		t.Errorf("special sees %v", got)
	}

	// Calls reach the handler of the conversation that made them.
	h1, h2 := &recorder{conv: "c1"}, &recorder{conv: "c2"}
	defer b.Attach("c1", h1)()
	defer b.Attach("c2", h2)()
	plain := connect(t, b.Server("plain", "c1"))
	for _, s := range []*mcp.ClientSession{plain, special} {
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_file", Arguments: map[string]any{"path": "/tmp/x.png"}})
		if err != nil || res.IsError {
			t.Fatalf("call: %v %+v", err, res)
		}
	}
	if !slices.Equal(h1.files, []string{"c1:/tmp/x.png"}) || !slices.Equal(h2.files, []string{"c2:/tmp/x.png"}) {
		t.Errorf("routing: %v %v", h1.files, h2.files)
	}

	// Relative paths are refused.
	res, _ := plain.CallTool(context.Background(), &mcp.CallToolParams{Name: "send_file", Arguments: map[string]any{"path": "x.png"}})
	if res == nil || !res.IsError {
		t.Errorf("relative path accepted: %+v", res)
	}
}

func TestRejectsMissingToken(t *testing.T) {
	b, err := Start("")
	if err != nil {
		t.Fatal(err)
	}
	b.AddAgent("a", nil)
	resp, err := http.Post(b.Server("a", "c").URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

// Every tool advertises all its behaviour hints, so clients never fall back
// to MCP's defaults (which assume destructive and open world).
func TestToolsAdvertiseHints(t *testing.T) {
	b, err := Start("", SendFile)
	if err != nil {
		t.Fatal(err)
	}
	approve := agent.Tool{Name: "approve", Schema: map[string]any{"type": "object"},
		Hints: agent.ToolHints{Title: "Approve", ReadOnly: true}, Call: SendFile.Call}
	b.AddAgent("a", []agent.Tool{approve})
	res, err := connect(t, b.Server("a", "c")).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil || a.Title == "" || a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s: incomplete annotations %+v", tool.Name, a)
		}
		switch tool.Name {
		case "send_file":
			if a.ReadOnlyHint || *a.DestructiveHint || a.IdempotentHint || !*a.OpenWorldHint {
				t.Errorf("send_file hints = %+v", a)
			}
		case "approve":
			if !a.ReadOnlyHint || *a.DestructiveHint || *a.OpenWorldHint {
				t.Errorf("approve hints = %+v", a)
			}
		}
	}
}
