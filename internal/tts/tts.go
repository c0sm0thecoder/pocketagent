// Package tts turns replies into voice messages.
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/argv"
)

// Speaker turns text into OGG/Opus audio, the usual voice-message format.
type Speaker interface {
	Speak(ctx context.Context, text string) ([]byte, error)
}

// Options decodes a provider's config keys.
type Options interface {
	Decode(v any) error
}

// Factory builds a Speaker from its options.
type Factory func(Options) (Speaker, error)

func onPath(bins ...string) error {
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			return fmt.Errorf("%s not found on PATH", b)
		}
	}
	return nil
}

var (
	reCodeBlock = regexp.MustCompile("(?s)```.*?```")
	reMarkup    = regexp.MustCompile("[*_`#>]+")
)

// Speakable strips Markdown and code so only prose is read aloud.
func Speakable(md string) string {
	s := reCodeBlock.ReplaceAllString(md, " (code omitted) ")
	return strings.TrimSpace(reMarkup.ReplaceAllString(s, ""))
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

// ---------- say ----------

type say struct {
	Voice  string `yaml:"voice"`
	FFmpeg string `yaml:"ffmpeg"`
}

// NewSay uses the macOS `say` command.
func NewSay(o Options) (Speaker, error) {
	s := &say{FFmpeg: "ffmpeg"}
	return s, o.Decode(s)
}

// Check verifies the local tools exist.
func (s *say) Check() error { return onPath("say", s.FFmpeg) }

func (s *say) Speak(ctx context.Context, text string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pocketagent-tts-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	aiff := filepath.Join(dir, "speech.aiff")
	args := []string{"-o", aiff}
	if s.Voice != "" {
		args = append(args, "-v", s.Voice)
	}
	if b, err := exec.CommandContext(ctx, "say", append(args, text)...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say: %v: %s", err, bytes.TrimSpace(b))
	}
	return toOpus(ctx, s.FFmpeg, aiff)
}

// ---------- HTTP ----------

type httpAPI struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
	Voice   string `yaml:"voice"`
}

// NewHTTP posts text to an /audio/speech endpoint, the de facto standard
// API served by many hosted and self-hosted speech services.
func NewHTTP(o Options) (Speaker, error) {
	h := &httpAPI{}
	if err := o.Decode(h); err != nil {
		return nil, err
	}
	if h.BaseURL == "" || h.Model == "" || h.Voice == "" {
		return nil, errors.New("base_url, model and voice are required")
	}
	h.BaseURL = strings.TrimRight(h.BaseURL, "/")
	return h, nil
}

func (h *httpAPI) Speak(ctx context.Context, text string) ([]byte, error) {
	body, _ := json.Marshal(map[string]string{
		"model": h.Model, "voice": h.Voice, "input": text, "response_format": "opus",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIKey)
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

// ---------- command ----------

type command struct {
	Command []string `yaml:"command"` // {text_file} in, {out} an audio file ffmpeg can read
	FFmpeg  string   `yaml:"ffmpeg"`
}

// NewCommand runs any program that writes speech to a file.
func NewCommand(o Options) (Speaker, error) {
	c := &command{FFmpeg: "ffmpeg"}
	if err := o.Decode(c); err != nil {
		return nil, err
	}
	if len(c.Command) == 0 {
		return nil, errors.New("command is required")
	}
	return c, nil
}

// Check verifies the local tools exist.
func (c *command) Check() error { return onPath(c.Command[0], c.FFmpeg) }

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
	line := argv.Expand(c.Command, map[string]string{"text_file": in, "out": out})
	if b, err := exec.CommandContext(ctx, line[0], line[1:]...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s: %v: %s", line[0], err, bytes.TrimSpace(b))
	}
	return toOpus(ctx, c.FFmpeg, out)
}
