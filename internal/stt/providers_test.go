package stt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWhisperCpp(t *testing.T) {
	dir := t.TempDir()
	// ffmpeg's last argument is the output file.
	ffmpeg := script(t, dir, "ffmpeg", `for a; do out=$a; done; echo wav > "$out"`)
	whisper := script(t, dir, "whisper", `echo "  hello"; echo "world  "; echo "args: $*" >&2`)
	model := filepath.Join(dir, "model.bin")
	os.WriteFile(model, []byte("m"), 0o600)

	tr, err := NewWhisperCpp(config.OptionsFrom(map[string]any{"binary": whisper, "ffmpeg": ffmpeg, "model": model, "language": "az"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.(interface{ Check() error }).Check(); err != nil {
		t.Errorf("check: %v", err)
	}
	got, err := tr.Transcribe(context.Background(), filepath.Join(dir, "voice.oga"))
	if err != nil || got != "hello world" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestWhisperCppFailures(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := script(t, dir, "ffmpeg", `for a; do out=$a; done; echo wav > "$out"`)
	silent := script(t, dir, "silent", `echo "[BLANK_AUDIO]"`)
	broken := script(t, dir, "broken", `echo "bad model" >&2; exit 1`)

	tr, _ := NewWhisperCpp(config.OptionsFrom(map[string]any{"binary": silent, "ffmpeg": ffmpeg, "model": "/nope"}))
	if err := tr.(interface{ Check() error }).Check(); err == nil {
		t.Error("check passed with a missing model")
	}
	if _, err := tr.Transcribe(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "couldn't hear") {
		t.Errorf("blank audio: %v", err)
	}
	tr, _ = NewWhisperCpp(config.OptionsFrom(map[string]any{"binary": broken, "ffmpeg": ffmpeg, "model": "/m"}))
	if _, err := tr.Transcribe(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Errorf("failing whisper: %v", err)
	}
	badFFmpeg := script(t, dir, "badffmpeg", `echo "unsupported codec" >&2; exit 1`)
	tr, _ = NewWhisperCpp(config.OptionsFrom(map[string]any{"binary": silent, "ffmpeg": badFFmpeg, "model": "/m"}))
	if _, err := tr.Transcribe(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "unsupported codec") {
		t.Errorf("failing ffmpeg: %v", err)
	}
}

func TestCommandFailureAndCheck(t *testing.T) {
	tr, _ := NewCommand(config.OptionsFrom(map[string]any{"command": []string{"sh", "-c", "echo nope >&2; exit 3"}}))
	if _, err := tr.Transcribe(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
	missing, _ := NewCommand(config.OptionsFrom(map[string]any{"command": []string{"/no/such/tool"}}))
	if err := missing.(interface{ Check() error }).Check(); err == nil {
		t.Error("check passed for a missing command")
	}
	if _, err := NewCommand(config.OptionsFrom(map[string]any{})); err == nil {
		t.Error("empty command accepted")
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	tr, _ := NewHTTP(config.OptionsFrom(map[string]any{"base_url": "http://127.0.0.1:1", "model": "m"}))
	f := filepath.Join(t.TempDir(), "a.oga")
	os.WriteFile(f, []byte("x"), 0o600)
	if _, err := tr.Transcribe(context.Background(), f); err == nil {
		t.Error("unreachable endpoint succeeded")
	}
	if _, err := tr.Transcribe(context.Background(), "/no/such/file"); err == nil {
		t.Error("missing file succeeded")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/m.bin"); got != filepath.Join(home, "m.bin") {
		t.Errorf("got %q", got)
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Errorf("got %q", got)
	}
}
