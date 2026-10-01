//go:build integration

package claude_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/claude"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type autoHandler struct {
	allow bool
	asked []string
	text  strings.Builder
}

func (h *autoHandler) Session(string)          {}
func (h *autoHandler) Text(s string)           { h.text.WriteString(s) }
func (h *autoHandler) Tool(string, agent.Kind) {}
func (h *autoHandler) Permission(_ context.Context, p agent.Permission) agent.Decision {
	h.asked = append(h.asked, p.Tool+" "+string(p.Kind))
	if h.allow {
		return agent.Decision{OptionID: "allow"}
	}
	return agent.Decision{OptionID: "deny", Message: "not today"}
}

// go test -tags integration ./internal/agent/claude/
func TestApprovalsThroughBridge(t *testing.T) {
	br, err := bridge.Start("")
	if err != nil {
		t.Fatal(err)
	}
	a := claude.New(config.Agent{})
	for _, allow := range []bool{true, false} {
		h := &autoHandler{allow: allow}
		unregister := br.Register("t", &bridge.Endpoint{Handler: h})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res, err := a.Run(ctx, agent.Request{
			Conv: "t", Cwd: t.TempDir(), Model: "haiku", Mode: agent.ModeAsk, MCP: br.Server("t"),
			Prompt: []agent.Block{{Text: "Use Bash to run exactly: touch marker.txt && echo bridge-ok-123 . Then reply with its output, or DENIED if you were not allowed."}},
		}, h)
		cancel()
		unregister()
		if err != nil {
			t.Fatalf("allow=%v: %v", allow, err)
		}
		out := h.text.String() + res.FinalText
		t.Logf("allow=%v asked=%v out=%q cost=$%.4f", allow, h.asked, out, res.CostUSD)
		if len(h.asked) == 0 || h.asked[0] != "Bash execute" {
			t.Errorf("allow=%v: asked = %v", allow, h.asked)
		}
		if allow != strings.Contains(out, "bridge-ok-123") {
			t.Errorf("allow=%v: output %q", allow, out)
		}
	}
}

func TestImage(t *testing.T) {
	png := t.TempDir() + "/red.png"
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "color=red:s=64x64", "-frames:v", "1", png).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	data, _ := os.ReadFile(png)
	h := &autoHandler{}
	res, err := claude.New(config.Agent{}).Run(context.Background(), agent.Request{
		Cwd: t.TempDir(), Model: "haiku",
		Prompt: []agent.Block{{Image: data, MimeType: "image/png"}, {Text: "What single color fills this image? One lowercase word."}},
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if out := strings.ToLower(h.text.String() + res.FinalText); !strings.Contains(out, "red") {
		t.Errorf("model did not see the image: %q", out)
	}
}
