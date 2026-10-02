package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/registry"
)

// checker is implemented by components with local prerequisites
// (executables, model files) that doctor can verify.
type checker interface {
	Check() error
}

type report struct{ failed bool }

func (r *report) check(err error, what, hint string) {
	if err == nil {
		fmt.Println("✓", what)
		return
	}
	r.failed = true
	fmt.Println("✗", what+":", err)
	if hint != "" {
		fmt.Println("   →", hint)
	}
}

func (r *report) warn(what, hint string) {
	fmt.Println("!", what)
	if hint != "" {
		fmt.Println("   →", hint)
	}
}

func doctor(cfgPath string) error {
	r := &report{}
	cfg, err := config.Load(cfgPath)
	r.check(err, "config "+cfgPath, "run `pocketagent init`")
	if err != nil {
		return errors.New("doctor found problems")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := getMe(ctx, cfg.Telegram.Token)
	r.check(err, "telegram bot @"+me.Username, "check telegram.token")
	fmt.Printf("✓ allowlist: %v\n", cfg.Telegram.AllowedUsers)

	for _, name := range cfg.AgentNames() {
		r.checkAgent(name, cfg.Agents[name])
	}
	r.checkProvider("voice messages", cfg.Transcriber, func(p config.Provider) (any, error) { return registry.NewTranscriber(p) })
	r.checkProvider("voice replies", cfg.TTS, func(p config.Provider) (any, error) { return registry.NewSpeaker(p) })

	if cfg.CheckpointsEnabled() {
		r.check(lookPath("git"), "git (for /diff and /undo)", "install git, or set checkpoints: false")
	}
	if fi, err := os.Stat(cfg.Defaults.Cwd); err != nil || !fi.IsDir() {
		r.check(fmt.Errorf("not a directory"), "default cwd "+cfg.Defaults.Cwd, "create it or change defaults.cwd")
	}
	for _, name := range cfg.ProjectNames() {
		if fi, err := os.Stat(cfg.Projects[name].Cwd); err != nil || !fi.IsDir() {
			r.warn("project "+name+": "+cfg.Projects[name].Cwd+" does not exist", "")
		}
	}

	if pid := runningPID(cfg.Home); pid != 0 {
		fmt.Printf("✓ running (pid %d)\n", pid)
	} else {
		r.warn("not running", "pocketagent start")
	}
	if serviceInstalled() {
		fmt.Println("✓ service installed:", serviceFile())
	}
	if r.failed {
		return errors.New("doctor found problems")
	}
	return nil
}

func (r *report) checkAgent(name string, a config.Agent) {
	label := fmt.Sprintf("agent %s (%s)", name, a.Type)
	if _, err := registry.NewAgent(name, a); err != nil {
		r.check(err, label, "")
		return
	}
	bin := a.Command[0]
	if len(a.Wrap) > 0 {
		bin = a.Wrap[0]
	}
	hint := "install it, or fix agents." + name + ".command"
	if pr, ok := registry.FindPreset(name); ok {
		hint += "; then log in once with `" + pr.Login + "`"
	}
	r.check(lookPath(bin), label, hint)
}

func (r *report) checkProvider(what string, p config.Provider, build func(config.Provider) (any, error)) {
	if p.Type == registry.None {
		r.warn(what+" are off", "")
		return
	}
	impl, err := build(p)
	if err == nil {
		if c, ok := impl.(checker); ok {
			err = c.Check()
		}
	}
	r.check(err, what+" ("+p.Type+")", "")
}

func lookPath(bin string) error {
	if !onPath(bin) {
		return fmt.Errorf("%s not found on PATH", bin)
	}
	return nil
}
