// Package tts turns replies into voice notes.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type Speaker interface {
	// Speak returns OGG/Opus audio, the format Telegram voice notes use.
	Speak(ctx context.Context, text string) ([]byte, error)
}

// New returns nil when TTS is disabled.
func New(c config.TTS) (Speaker, error) {
	switch c.Type {
	case "none", "":
		return nil, nil
	case "say":
		return &say{voice: c.Voice, ffmpeg: c.FFmpeg}, nil
	case "openai":
		base := strings.TrimRight(c.BaseURL, "/")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model, voice := c.Model, c.Voice
		if model == "" {
			model = "gpt-4o-mini-tts"
		}
		if voice == "" {
			voice = "alloy"
		}
		return &openAI{base: base, key: c.APIKey, model: model, voice: voice}, nil
	case "command":
		if len(c.Command) == 0 {
			return nil, fmt.Errorf("tts.command is required for type command")
		}
		return &command{argv: c.Command, ffmpeg: c.FFmpeg}, nil
	}
	return nil, fmt.Errorf("unknown tts.type %q (say, openai, command, none)", c.Type)
}

var (
	reCodeBlock = regexp.MustCompile("(?s)```.*?```")
	reMarkup    = regexp.MustCompile("[*_`#>]+")
)

// Speakable strips Markdown and code so the voice reads prose only.
func Speakable(md string) string {
	s := reCodeBlock.ReplaceAllString(md, " (code omitted) ")
	s = reMarkup.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func toOpus(ctx context.Context, ffmpeg, in string) ([]byte, error) {
	out := in + ".ogg"
	defer os.Remove(out)
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-y", "-i", in, "-c:a", "libopus", "-b:a", "32k", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(b))
	}
	return os.ReadFile(out)
}

type say struct{ voice, ffmpeg string }

func (s *say) Speak(ctx context.Context, text string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pocketagent-tts-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	aiff := filepath.Join(dir, "speech.aiff")
	args := []string{"-o", aiff}
	if s.voice != "" {
		args = append(args, "-v", s.voice)
	}
	cmd := exec.CommandContext(ctx, "say", append(args, text)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say: %v: %s", err, bytes.TrimSpace(b))
	}
	return toOpus(ctx, s.ffmpeg, aiff)
}

type openAI struct{ base, key, model, voice string }

func (o *openAI) Speak(ctx context.Context, text string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{
		"model": o.model, "voice": o.voice, "input": text, "response_format": "opus",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.key != "" {
		req.Header.Set("Authorization", "Bearer "+o.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("speech API: %s: %s", resp.Status, bytes.TrimSpace(data))
	}
	return data, nil
}

// command runs a template with {text_file} (input) and {out} (an audio file
// in any format ffmpeg reads).
type command struct {
	argv   []string
	ffmpeg string
}

func (c *command) Speak(ctx context.Context, text string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pocketagent-tts-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "text.txt"), filepath.Join(dir, "speech.wav")
	if err := os.WriteFile(in, []byte(text), 0o600); err != nil {
		return nil, err
	}
	argv := config.Expand(c.argv, map[string]string{"text_file": in, "out": out})
	if b, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s: %v: %s", argv[0], err, bytes.TrimSpace(b))
	}
	return toOpus(ctx, c.ffmpeg, out)
}
