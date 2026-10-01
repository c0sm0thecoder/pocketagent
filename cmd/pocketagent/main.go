// Command pocketagent drives coding agents (Claude Code, Codex, Gemini,
// Kiro, Aider and anything else) from Telegram.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/acp"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/claude"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/command"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
	"github.com/c0sm0thecoder/pocketagent/internal/stt"
	"github.com/c0sm0thecoder/pocketagent/internal/telegram"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// Set by goreleaser.
var version = "dev"

func main() {
	fs := flag.NewFlagSet("pocketagent", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	fs.Usage = usage

	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	fs.Parse(args)

	var err error
	switch cmd {
	case "run":
		err = run(*cfgPath)
	case "version", "--version":
		fmt.Println("pocketagent", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pocketagent: drive coding agents from Telegram

Usage:
  pocketagent run        run in the foreground (default)
  pocketagent version

Flags:
  --config PATH          config file (default ~/.pocketagent/config.yaml)
`)
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.Home, "state.json"))
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}

	agents := map[string]agent.Agent{}
	for name, ac := range cfg.Agents {
		a, err := newAgent(ac)
		if err != nil {
			return fmt.Errorf("agents.%s: %w", name, err)
		}
		agents[name] = a
	}
	defer func() {
		for _, a := range agents {
			a.Close()
		}
	}()

	tr, err := stt.New(cfg.Transcriber)
	if err != nil {
		return err
	}
	sp, err := tts.New(cfg.TTS)
	if err != nil {
		return err
	}
	br, err := bridge.Start(os.Getenv("POCKETAGENT_BRIDGE_HOST"))
	if err != nil {
		return fmt.Errorf("start mcp bridge: %w", err)
	}

	c := core.New(cfg, st, agents, br, tr, sp)
	b, err := telegram.New(cfg, c)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("pocketagent %s running: agents %v, default %s, cwd %s", version, cfg.AgentNames(), cfg.Defaults.Agent, cfg.Defaults.Cwd)
	b.Run(ctx)
	return nil
}

func newAgent(ac config.Agent) (agent.Agent, error) {
	switch ac.Type {
	case "claude":
		return claude.New(ac), nil
	case "acp":
		return acp.New(ac), nil
	case "command":
		return command.New(ac), nil
	}
	return nil, fmt.Errorf("unknown agent type %q", ac.Type)
}
