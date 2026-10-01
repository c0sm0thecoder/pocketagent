//go:build integration

package stt

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// Speaks a sentence with macOS say (via the tts package, as OGG/Opus like a
// Telegram voice note) and transcribes it with whisper.cpp.
func TestSayThenWhisper(t *testing.T) {
	model := config.ExpandHome("~/.pocketagent/models/ggml-small.bin")
	if _, err := os.Stat(model); err != nil {
		t.Skip("no whisper model at " + model)
	}
	sp, _ := tts.New(config.TTS{Type: "say", FFmpeg: "ffmpeg"})
	ogg, err := sp.Speak(context.Background(), "Please list the files in this directory.")
	if err != nil {
		t.Skipf("say: %v", err)
	}
	p := t.TempDir() + "/voice.oga"
	os.WriteFile(p, ogg, 0o600)

	tr, _ := New(config.Transcriber{Type: "whisper-cpp", Model: model, Language: "auto", FFmpeg: "ffmpeg"})
	got, err := tr.Transcribe(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got), "files") {
		t.Errorf("transcript %q", got)
	}
}
