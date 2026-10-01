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
	"runtime/debug"
	"strings"
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

// Set by goreleaser; `go install` builds fall back to the module version.
var version = "dev"

func main() {
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			version = bi.Main.Version
		}
	}

	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if cmd == "service" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("pocketagent", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	force := fs.Bool("force", false, "init: overwrite an existing config")
	fs.Usage = usage
	fs.Parse(args)
	home := filepath.Dir(*cfgPath)

	var err error
	switch cmd {
	case "run":
		err = run(*cfgPath)
	case "init":
		err = initConfig(*cfgPath, *force)
	case "doctor":
		err = doctor(*cfgPath)
	case "start":
		err = start(*cfgPath, home)
	case "stop":
		err = stop(home)
	case "restart":
		if err = stop(home); err == nil {
			err = start(*cfgPath, home)
		}
	case "status":
		err = status(home)
	case "logs":
		err = logs(home)
	case "service":
		switch sub {
		case "install":
			err = serviceInstall(*cfgPath, home)
		case "uninstall":
			err = serviceUninstall()
		default:
			err = fmt.Errorf("usage: pocketagent service install|uninstall")
		}
	case "version":
		fmt.Println("pocketagent", version)
	case "help":
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

Setup:
  pocketagent init               interactive setup: bot token, your user id, agents, voice
  pocketagent doctor             check config, agents, voice and the bot connection

Run:
  pocketagent start              run in the background
  pocketagent stop               stop it
  pocketagent restart
  pocketagent status
  pocketagent logs               follow the log
  pocketagent run                run in the foreground
  pocketagent service install    start at login and restart on crashes (launchd/systemd)
  pocketagent service uninstall

  pocketagent version

Flags:
  --config PATH                  config file (default ~/.pocketagent/config.yaml,
                                 or $POCKETAGENT_HOME/config.yaml)
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
	lock, err := lockInstance(cfg.Home)
	if err != nil {
		return err
	}
	defer lock.Close()
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
	br, err := bridge.Start(cfg.BridgeHost)
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
