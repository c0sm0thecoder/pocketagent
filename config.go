package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken        string
	AllowedUsers    map[int64]bool
	DefaultCwd      string
	ClaudeBin       string
	ClaudeModel     string
	AutoAllowTools  []string
	WhisperBin      string
	WhisperModel    string
	WhisperLang     string
	FFmpegBin       string
	DataDir         string
	ApprovalTimeout time.Duration
}

func loadConfig() (*Config, error) {
	loadDotEnv(".env")

	home, _ := os.UserHomeDir()
	c := &Config{
		BotToken:     os.Getenv("TELEGRAM_BOT_TOKEN"),
		AllowedUsers: map[int64]bool{},
		DefaultCwd:   envOr("DEFAULT_CWD", home),
		ClaudeBin:    envOr("CLAUDE_BIN", "claude"),
		ClaudeModel:  os.Getenv("CLAUDE_MODEL"),
		WhisperBin:   envOr("WHISPER_BIN", "whisper-cli"),
		WhisperModel: os.Getenv("WHISPER_MODEL"),
		WhisperLang:  envOr("WHISPER_LANG", "auto"),
		FFmpegBin:    envOr("FFMPEG_BIN", "ffmpeg"),
		DataDir:      expandHome(envOr("DATA_DIR", filepath.Join(home, ".claude-telegram"))),
	}

	if c.BotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	for _, s := range splitList(os.Getenv("ALLOWED_USER_IDS")) {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad ALLOWED_USER_IDS entry %q: %w", s, err)
		}
		c.AllowedUsers[id] = true
	}
	// The bot runs code on this machine, so refuse to start without an allowlist.
	if len(c.AllowedUsers) == 0 {
		return nil, fmt.Errorf("ALLOWED_USER_IDS is required (your numeric Telegram user id; message @userinfobot to get it)")
	}

	c.AutoAllowTools = splitList(envOr("AUTO_ALLOW_TOOLS", "Read,Glob,Grep,TodoWrite,mcp__tg__send_file"))
	c.DefaultCwd = expandHome(c.DefaultCwd)
	c.WhisperModel = expandHome(c.WhisperModel)

	timeout, err := time.ParseDuration(envOr("APPROVAL_TIMEOUT", "10m"))
	if err != nil {
		return nil, fmt.Errorf("bad APPROVAL_TIMEOUT: %w", err)
	}
	c.ApprovalTimeout = timeout

	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, err
	}
	return c, nil
}

// loadDotEnv sets KEY=VALUE pairs from path without overriding variables
// that are already set in the environment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
