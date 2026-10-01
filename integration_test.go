//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeUI answers approvals automatically and records what it was asked.
type fakeUI struct {
	allow bool
	mu    sync.Mutex
	asked []string
}

func (f *fakeUI) AskApproval(_ context.Context, chatID int64, tool string, input map[string]any) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, _ := json.Marshal(input)
	f.asked = append(f.asked, tool+" "+string(data))
	return f.allow, "denied by test"
}

func (f *fakeUI) SendFile(context.Context, int64, string, string) error { return nil }

// Run with: go test -tags integration -run TestApprovalBridge -v
func TestApprovalBridge(t *testing.T) {
	for _, allow := range []bool{true, false} {
		ui := &fakeUI{allow: allow}
		bridge, err := startMCPBridge(ui)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		var tools []string
		res, err := runClaude(ctx, RunOptions{
			Bin:            "claude",
			Cwd:            t.TempDir(),
			Model:          "haiku",
			MCPConfig:      bridge.ConfigFor(42),
			PermissionTool: permissionToolName,
		}, []ContentBlock{{Type: "text", Text: "Use the Bash tool to run exactly: touch marker.txt && echo bridge-ok-123 . Then tell me its output, or say DENIED if you were not allowed."}},
			RunEvents{OnToolUse: func(name string, _ json.RawMessage) { tools = append(tools, name) }})
		if err != nil {
			t.Fatalf("allow=%v: %v", allow, err)
		}
		t.Logf("allow=%v tools=%v asked=%v result=%q cost=$%.4f", allow, tools, ui.asked, res.Text, res.CostUSD)
		if len(ui.asked) == 0 || !strings.HasPrefix(ui.asked[0], "Bash") {
			t.Errorf("allow=%v: expected a Bash approval request, got %v", allow, ui.asked)
		}
		if allow != strings.Contains(res.Text, "bridge-ok-123") {
			t.Errorf("allow=%v: unexpected result %q", allow, res.Text)
		}
	}
}

func TestImageInput(t *testing.T) {
	png := t.TempDir() + "/red.png"
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "color=red:s=64x64", "-frames:v", "1", png).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, _ := os.ReadFile(png)
	res, err := runClaude(context.Background(), RunOptions{Bin: "claude", Cwd: t.TempDir(), Model: "haiku"},
		[]ContentBlock{
			{Type: "image", Source: &ImageSource{Type: "base64", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(data)}},
			{Type: "text", Text: "What single color fills this image? Answer with one lowercase word."},
		}, RunEvents{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result=%q", res.Text)
	if !strings.Contains(strings.ToLower(res.Text), "red") {
		t.Errorf("model did not see the image: %q", res.Text)
	}
}

func TestTranscribe(t *testing.T) {
	dir := t.TempDir()
	aiff, ogg := dir+"/v.aiff", dir+"/v.ogg"
	if out, err := exec.Command("say", "-o", aiff, "Please list the files in this directory.").CombinedOutput(); err != nil {
		t.Skipf("say unavailable: %v %s", err, out)
	}
	// Telegram voice notes are OGG/Opus.
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-i", aiff, "-c:a", "libopus", ogg).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	loadDotEnv(".env")
	cfg := &Config{FFmpegBin: "ffmpeg", WhisperBin: "whisper-cli", WhisperLang: "auto",
		WhisperModel: expandHome(envOr("WHISPER_MODEL", "~/.claude-telegram/models/ggml-small.bin"))}
	text, err := transcribe(context.Background(), cfg, ogg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcript=%q", text)
	if !strings.Contains(strings.ToLower(text), "files") {
		t.Errorf("unexpected transcript %q", text)
	}
}
