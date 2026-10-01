// Command claude-telegram lets you talk to Claude Code from a Telegram bot,
// with text, voice notes (transcribed locally by whisper.cpp) and images.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		log.Fatalf("open state: %v", err)
	}

	app := &App{
		cfg:       cfg,
		store:     store,
		running:   map[int64]context.CancelFunc{},
		approvals: map[string]*pendingApproval{},
		albums:    map[string]*album{},
	}

	app.bridge, err = startMCPBridge(app)
	if err != nil {
		log.Fatalf("start mcp bridge: %v", err)
	}

	app.tg, err = bot.New(cfg.BotToken, bot.WithDefaultHandler(app.handleUpdate))
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "new", Description: "Start a fresh session"},
		{Command: "stop", Description: "Cancel the current run"},
		{Command: "cwd", Description: "Show or change working directory"},
		{Command: "model", Description: "Show or change model"},
		{Command: "status", Description: "Session info"},
		{Command: "help", Description: "How to use this bot"},
	}})

	if cfg.WhisperModel == "" {
		log.Printf("WHISPER_MODEL not set: voice messages are disabled")
	}
	log.Printf("bot running; default cwd %s; allowed users %v", cfg.DefaultCwd, keys(cfg.AllowedUsers))
	app.tg.Start(ctx)
}

func keys(m map[int64]bool) []int64 {
	var out []int64
	for k := range m {
		out = append(out, k)
	}
	return out
}
