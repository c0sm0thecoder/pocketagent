package tts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeFFmpeg "encodes" by copying the input, prefixed, to the last argument.
const fakeFFmpeg = `for a; do prev=$cur; cur=$a; done; in=""; while [ $# -gt 0 ]; do [ "$1" = "-i" ] && in=$2; shift; done; { printf OPUS:; cat "$in"; } > "$cur"`

func TestSay(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "say", `while [ $# -gt 0 ]; do case $1 in -o) out=$2; shift;; -v) shift;; *) text=$1;; esac; shift; done; printf "%s" "$text" > "$out"`)
	ffmpeg := script(t, dir, "ffmpeg", fakeFFmpeg)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	sp, err := NewSay(config.OptionsFrom(map[string]any{"voice": "Alex", "ffmpeg": ffmpeg}))
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.(interface{ Check() error }).Check(); err != nil {
		t.Errorf("check: %v", err)
	}
	got, err := sp.Speak(context.Background(), "hello there")
	if err != nil || string(got) != "OPUS:hello there" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCommand(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := script(t, dir, "ffmpeg", fakeFFmpeg)
	tts := script(t, dir, "tts", `tr a-z A-Z < "$1" > "$2"`)
	sp, err := NewCommand(config.OptionsFrom(map[string]any{"command": []string{tts, "{text_file}", "{out}"}, "ffmpeg": ffmpeg}))
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.(interface{ Check() error }).Check(); err != nil {
		t.Errorf("check: %v", err)
	}
	got, err := sp.Speak(context.Background(), "abc")
	if err != nil || string(got) != "OPUS:ABC" {
		t.Errorf("got %q, %v", got, err)
	}

	failing, _ := NewCommand(config.OptionsFrom(map[string]any{"command": []string{"sh", "-c", "echo no voice >&2; exit 1"}}))
	if _, err := failing.Speak(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "no voice") {
		t.Errorf("err = %v", err)
	}
	if _, err := NewCommand(config.OptionsFrom(map[string]any{})); err == nil {
		t.Error("empty command accepted")
	}
}

func TestHTTPValidationAndErrors(t *testing.T) {
	if _, err := NewHTTP(config.OptionsFrom(map[string]any{"base_url": "u", "model": "m"})); err == nil {
		t.Error("missing voice accepted")
	}
	sp, _ := NewHTTP(config.OptionsFrom(map[string]any{"base_url": "http://127.0.0.1:1", "model": "m", "voice": "v"}))
	if _, err := sp.Speak(context.Background(), "x"); err == nil {
		t.Error("unreachable endpoint succeeded")
	}
}
