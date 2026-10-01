// Package stt turns voice notes into text.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type Transcriber interface {
	// Transcribe reads an audio file (Telegram voice notes are OGG/Opus).
	Transcribe(ctx context.Context, path string) (string, error)
}

// New returns nil when transcription is disabled.
func New(c config.Transcriber) (Transcriber, error) {
	switch c.Type {
	case "none", "":
		return nil, nil
	case "whisper-cpp":
		if c.Model == "" {
			return nil, fmt.Errorf("transcriber.model is required for whisper-cpp (path to a ggml model)")
		}
		bin := "whisper-cli"
		if len(c.Command) > 0 {
			bin = c.Command[0]
		}
		return &whisperCpp{bin: bin, model: c.Model, lang: c.Language, ffmpeg: c.FFmpeg}, nil
	case "openai":
		base := strings.TrimRight(c.BaseURL, "/")
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		model := c.Model
		if model == "" {
			model = "whisper-1"
		}
		return &openAI{base: base, key: c.APIKey, model: model, lang: c.Language}, nil
	case "command":
		if len(c.Command) == 0 {
			return nil, fmt.Errorf("transcriber.command is required for type command")
		}
		return &command{argv: c.Command}, nil
	}
	return nil, fmt.Errorf("unknown transcriber.type %q (whisper-cpp, openai, command, none)", c.Type)
}

// ToWav converts any audio ffmpeg understands to 16 kHz mono WAV.
func ToWav(ctx context.Context, ffmpeg, in, out string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-y",
		"-i", in, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(b))
	}
	return nil
}

type whisperCpp struct{ bin, model, lang, ffmpeg string }

func (w *whisperCpp) Transcribe(ctx context.Context, path string) (string, error) {
	dir, err := os.MkdirTemp("", "pocketagent-stt-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if err := ToWav(ctx, w.ffmpeg, path, wav); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, w.bin, "-m", w.model, "-f", wav, "-l", w.lang, "-nt", "-np")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", w.bin, err, lastLine(stderr.String()))
	}
	return clean(stdout.String())
}

type openAI struct{ base, key, model, lang string }

func (o *openAI) Transcribe(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("model", o.model)
	if o.lang != "" && o.lang != "auto" {
		mw.WriteField("language", o.lang)
	}
	// Telegram's .oga is OGG; most endpoints only accept the .ogg extension.
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".ogg"
	fw, _ := mw.CreateFormFile("file", name)
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if o.key != "" {
		req.Header.Set("Authorization", "Bearer "+o.key)
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

// command runs a user-supplied template; {file} is the audio path and
// stdout is the transcript.
type command struct{ argv []string }

func (c *command) Transcribe(ctx context.Context, path string) (string, error) {
	argv := config.Expand(c.argv, map[string]string{"file": path})
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", argv[0], err, lastLine(stderr.String()))
	}
	return clean(stdout.String())
}

func clean(s string) (string, error) {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	text := strings.Join(lines, " ")
	if text == "" || text == "[BLANK_AUDIO]" {
		return "", fmt.Errorf("couldn't hear anything in that voice message")
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
