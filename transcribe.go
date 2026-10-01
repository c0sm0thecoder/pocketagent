package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// transcribe converts any audio file ffmpeg understands (Telegram voice
// notes are OGG/Opus) to 16 kHz mono WAV and runs whisper.cpp on it.
func transcribe(ctx context.Context, cfg *Config, audioPath string) (string, error) {
	if cfg.WhisperModel == "" {
		return "", fmt.Errorf("WHISPER_MODEL is not set, so voice messages are disabled")
	}
	dir, err := os.MkdirTemp("", "tg-voice-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	wav := filepath.Join(dir, "audio.wav")
	ff := exec.CommandContext(ctx, cfg.FFmpegBin, "-nostdin", "-loglevel", "error",
		"-i", audioPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	if out, err := ff.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg: %v: %s", err, bytes.TrimSpace(out))
	}

	var stdout, stderr bytes.Buffer
	w := exec.CommandContext(ctx, cfg.WhisperBin,
		"-m", cfg.WhisperModel,
		"-f", wav,
		"-l", cfg.WhisperLang,
		"-nt", // no timestamps
		"-np", // no progress/info prints
	)
	w.Stdout, w.Stderr = &stdout, &stderr
	if err := w.Run(); err != nil {
		return "", fmt.Errorf("whisper: %v: %s", err, lastLine(stderr.String()))
	}

	var lines []string
	for _, l := range strings.Split(stdout.String(), "\n") {
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
