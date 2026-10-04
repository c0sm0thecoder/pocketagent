//go:build integration

package claudecode_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/claudecode"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
)

type autoHandler struct {
	allow bool
	asked []string
	text  strings.Builder
}

func (h *autoHandler) Session(string)                                 {}
func (h *autoHandler) Message(s string)                               { h.text.WriteString(s) }
func (h *autoHandler) ToolCall(string, agent.Kind)                    {}
func (h *autoHandler) SendFile(context.Context, string, string) error { return nil }
func (h *autoHandler) Permission(_ context.Context, p agent.Permission) agent.Decision {
	h.asked = append(h.asked, p.Tool+" "+string(p.Kind))
	if h.allow {
		return agent.Decision{OptionID: "allow"}
	}
	return agent.Decision{OptionID: "deny", Message: "not today"}
}

func newAgent(t *testing.T) agent.Agent {
	t.Helper()
	a, err := claudecode.New(agent.Spec{Name: "claude-code", Command: []string{"claude"}, Options: agent.NoOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// go test -tags integration ./internal/agent/claudecode/
func TestApprovalsThroughToolServer(t *testing.T) {
	a := newAgent(t)
	tools, err := bridge.Start("", bridge.SendFile)
	if err != nil {
		t.Fatal(err)
	}
	tools.AddAgent("claude-code", a.(agent.ToolProvider).Tools())
	for _, allow := range []bool{true, false} {
		h := &autoHandler{allow: allow}
		detach := tools.Attach("t", h)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res, err := a.Run(ctx, agent.Request{
			Conv: "t", Cwd: t.TempDir(), Model: "haiku", Mode: agent.ModeAsk, Tools: tools.Server("claude-code", "t"),
			Prompt: []agent.Block{{Text: "Use Bash to run exactly: touch marker.txt && echo bridge-ok-123 . Then reply with its output, or DENIED if you were not allowed."}},
		}, h)
		cancel()
		detach()
		if err != nil {
			t.Fatalf("allow=%v: %v", allow, err)
		}
		out := h.text.String() + res.Final
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
	res, err := newAgent(t).Run(context.Background(), agent.Request{
		Cwd: t.TempDir(), Model: "haiku",
		Prompt: []agent.Block{{Image: data, MimeType: "image/png"}, {Text: "What single color fills this image? One lowercase word."}},
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if out := strings.ToLower(h.text.String() + res.Final); !strings.Contains(out, "red") {
		t.Errorf("model did not see the image: %q", out)
	}
}

// A session started in a terminal shows up in Sessions and continues here
// with its context.
func TestContinueTerminalSession(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("claude", "-p", "--model", "haiku", "Remember this word for later: zebra-42. Reply only OK.")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terminal session: %v %s", err, out)
	}
	a := newAgent(t)
	// Claude Code files sessions under the resolved path (macOS: /private/var/...).
	real, _ := filepath.EvalSymlinks(dir)
	ss, err := a.(agent.SessionLister).Sessions(context.Background(), "", real)
	if err != nil || len(ss) == 0 {
		t.Fatalf("terminal session not listed: %v %v", ss, err)
	}
	t.Logf("listed: %+v", ss[0])
	h := &autoHandler{}
	res, err := a.Run(context.Background(), agent.Request{Cwd: real, Model: "haiku", SessionID: ss[0].ID,
		Prompt: []agent.Block{{Text: "What was the word I asked you to remember? Reply with just the word."}}}, h)
	if err != nil {
		t.Fatal(err)
	}
	if out := h.text.String() + res.Final; !strings.Contains(out, "zebra-42") {
		t.Errorf("context lost: %q", out)
	}
	t.Logf("hint: %s", a.(agent.ResumeHinter).ResumeCommand(real, res.SessionID))
}
