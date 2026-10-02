package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
	"github.com/c0sm0thecoder/pocketagent/internal/frontend/telegram"
	"github.com/c0sm0thecoder/pocketagent/internal/gitcp"
	"github.com/c0sm0thecoder/pocketagent/internal/registry"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// run is the composition root: it builds every component from the config
// and connects them.
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
	tools, err := bridge.Start(cfg.BridgeHost, bridge.SendFile)
	if err != nil {
		return fmt.Errorf("start tool server: %w", err)
	}
	agents, err := buildAgents(cfg, tools)
	if err != nil {
		return err
	}
	defer closeAll(agents)

	transcriber, err := registry.NewTranscriber(cfg.Transcriber)
	if err != nil {
		return err
	}
	speaker, err := registry.NewSpeaker(cfg.TTS)
	if err != nil {
		return err
	}
	deps := core.Deps{
		Config: cfg, Agents: agents, Store: st, Tools: tools,
		Transcriber: transcriber, Speaker: speaker,
	}
	if cfg.CheckpointsEnabled() {
		deps.Checkpointer = gitcp.Git{}
	}
	c := core.New(deps)

	bot, err := telegram.New(cfg, c)
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("pocketagent %s running: agents %v, default %s, cwd %s", version, cfg.AgentNames(), cfg.Defaults.Agent, cfg.Defaults.Cwd)
	bot.Run(ctx)
	return nil
}

// buildAgents creates every configured agent and gives each its tool
// namespace on the tool server.
func buildAgents(cfg *config.Config, tools *bridge.Bridge) (map[string]agent.Agent, error) {
	agents := map[string]agent.Agent{}
	for _, name := range cfg.AgentNames() {
		a, err := registry.NewAgent(name, cfg.Agents[name])
		if err != nil {
			closeAll(agents)
			return nil, err
		}
		var extra []agent.Tool
		if tp, ok := a.(agent.ToolProvider); ok {
			extra = tp.Tools()
		}
		tools.AddAgent(name, extra)
		agents[name] = a
	}
	return agents, nil
}

func closeAll(agents map[string]agent.Agent) {
	for _, a := range agents {
		if c, ok := a.(io.Closer); ok {
			c.Close()
		}
	}
}
