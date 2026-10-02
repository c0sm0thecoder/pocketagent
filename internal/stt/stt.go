// Package stt turns voice messages into text.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/argv"
)

// Transcriber turns an audio file into text.
type Transcriber interface {
	Transcribe(ctx context.Context, path string) (string, error)
}

// Options decodes a provider's config keys.
type Options interface {
	Decode(v any) error
}

// Factory builds a Transcriber from its options.
type Factory func(Options) (Transcriber, error)

func onPath(bins ...string) error {
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			return fmt.Errorf("%s not found on PATH", b)
		}
	}
	return nil
}

// ---------- whisper.cpp ----------

type whisperCpp struct {
	Binary   string `yaml:"binary"`
	Model    string `yaml:"model"`    // path to a ggml model
	Language string `yaml:"language"` // "auto" or an ISO code
	FFmpeg   string `yaml:"ffmpeg"`
}

// NewWhisperCpp transcribes locally with whisper.cpp.
func NewWhisperCpp(o Options) (Transcriber, error) {
	w := &whisperCpp{Binary: "whisper-cli", Language: "auto", FFmpeg: "ffmpeg"}
	if err := o.Decode(w); err != nil {
		return nil, err
	}
	if w.Model == "" {
		return nil, errors.New("model is required (path to a ggml model file)")
	}
	w.Model = expandHome(w.Model)
	return w, nil
}

// Check verifies whisper.cpp, ffmpeg and the model are present.
func (w *whisperCpp) Check() error {
	if err := onPath(w.Binary, w.FFmpeg); err != nil {
		return err
	}
	if _, err := os.Stat(w.Model); err != nil {
		return fmt.Errorf("model %s: %w", w.Model, err)
	}
	return nil
}

func (w *whisperCpp) Transcribe(ctx context.Context, path string) (string, error) {
	dir, err := os.MkdirTemp("", "pocketagent-stt-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if err := ToWav(ctx, w.FFmpeg, path, wav); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, w.Binary, "-m", w.Model, "-f", wav, "-l", w.Language, "-nt", "-np")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", w.Binary, err, lastLine(stderr.String()))
	}
	return clean(stdout.String())
}

// ToWav converts any audio ffmpeg reads to 16 kHz mono WAV.
func ToWav(ctx context.Context, ffmpeg, in, out string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-y",
		"-i", in, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(b))
	}
	return nil
}

// ---------- HTTP ----------

type httpAPI struct {
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
	Language string `yaml:"language"`
}

// NewHTTP posts audio to a /audio/transcriptions endpoint, the de facto
// standard API served by many hosted and self-hosted speech services.
func NewHTTP(o Options) (Transcriber, error) {
	h := &httpAPI{}
	if err := o.Decode(h); err != nil {
		return nil, err
	}
	if h.BaseURL == "" || h.Model == "" {
		return nil, errors.New("base_url and model are required")
	}
	h.BaseURL = strings.TrimRight(h.BaseURL, "/")
	return h, nil
}

func (h *httpAPI) Transcribe(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("model", h.Model)
	if h.Language != "" && h.Language != "auto" {
		mw.WriteField("language", h.Language)
	}
	// Many endpoints accept OGG only under the .ogg extension, not .oga.
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".ogg"
	fw, _ := mw.CreateFormFile("file", name)
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if h.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("transcription API: %s: %s", resp.Status, bytes.TrimSpace(data))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("transcription API: %w", err)
	}
	return clean(out.Text)
}

// ---------- command ----------

type command struct {
	Command []string `yaml:"command"` // {file} is the audio path; stdout is the transcript
}

// NewCommand runs any program that prints a transcript.
func NewCommand(o Options) (Transcriber, error) {
	c := &command{}
	if err := o.Decode(c); err != nil {
		return nil, err
	}
	if len(c.Command) == 0 {
		return nil, errors.New("command is required")
	}
	return c, nil
}

// Check verifies the command exists.
func (c *command) Check() error { return onPath(c.Command[0]) }

func (c *command) Transcribe(ctx context.Context, path string) (string, error) {
	line := argv.Expand(c.Command, map[string]string{"file": path})
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, line[0], line[1:]...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", line[0], err, lastLine(stderr.String()))
	}
	return clean(stdout.String())
}

// ---------- helpers ----------

func clean(s string) (string, error) {
	text := strings.Join(strings.Fields(s), " ")
	if text == "" || text == "[BLANK_AUDIO]" {
		return "", errors.New("couldn't hear anything in that voice message")
	}
	return text, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
