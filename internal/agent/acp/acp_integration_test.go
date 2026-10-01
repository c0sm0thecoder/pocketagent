//go:build integration

package acp_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/acp"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type autoHandler struct {
	mu      sync.Mutex
	allow   bool
	asked   []agent.Permission
	text    strings.Builder
	tools   []string
	session string
}

func (h *autoHandler) Session(id string) { h.session = id }
func (h *autoHandler) Text(s string)     { h.mu.Lock(); h.text.WriteString(s); h.mu.Unlock() }
func (h *autoHandler) Tool(t string, k agent.Kind) {
	h.mu.Lock()
	h.tools = append(h.tools, string(k)+": "+t)
	h.mu.Unlock()
}
func (h *autoHandler) Permission(_ context.Context, p agent.Permission) agent.Decision {
	h.mu.Lock()
	h.asked = append(h.asked, p)
	h.mu.Unlock()
	for _, o := range p.Options {
		if o.Kind.Allows() == h.allow && (o.Kind == agent.AllowOnce || o.Kind == agent.RejectOnce) {
			return agent.Decision{OptionID: o.ID}
		}
	}
	return agent.Decision{}
}

// POCKETAGENT_ACP_CMD="npx -y @zed-industries/codex-acp" go test -tags integration -v ./internal/agent/acp/
func TestACPAgent(t *testing.T) {
	cmd := os.Getenv("POCKETAGENT_ACP_CMD")
	if cmd == "" {
		cmd = "npx -y @agentclientprotocol/claude-agent-acp"
	}
	a := acp.New(config.Agent{Command: strings.Fields(cmd), Model: os.Getenv("POCKETAGENT_ACP_MODEL")})
	defer a.Close()
	dir := t.TempDir()

	run := func(allow bool, prompt, session string) (*autoHandler, agent.Result) {
		h := &autoHandler{allow: allow}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		res, err := a.Run(ctx, agent.Request{Conv: "t", Cwd: dir, Mode: agent.ModeAsk, SessionID: session,
			Prompt: []agent.Block{{Text: prompt}}}, h)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		var asked []string
		for _, p := range h.asked {
			asked = append(asked, string(p.Kind)+" "+p.Tool+" | "+strings.ReplaceAll(p.Detail, "\n", " "))
		}
		t.Logf("allow=%v tools=%v asked=%v text=%q stop=%s cost=$%.4f models=%v",
			allow, h.tools, asked, h.text.String(), res.StopReason, res.SessionCostUSD, a.Models("t"))
		return h, res
	}

	h, res := run(true, "Create a file named marker.txt containing the word pocket, using your file or shell tools. Then reply DONE.", "")
	if _, err := os.Stat(dir + "/marker.txt"); err != nil {
		t.Errorf("marker.txt not created (asked %d times)", len(h.asked))
	}
	if res.SessionID == "" {
		t.Fatal("no session id")
	}

	// Same session, same process: the agent should remember.
	h2, _ := run(true, "What was the word you wrote into marker.txt? Reply with just the word.", res.SessionID)
	if !strings.Contains(strings.ToLower(h2.text.String()), "pocket") {
		t.Errorf("session context lost: %q", h2.text.String())
	}
}
