// Package command wraps any CLI that takes a prompt and prints a reply,
// configured as an argv template. It is the escape hatch for agents that
// don't speak ACP (Aider, custom scripts, ...).
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type Agent struct{ cfg config.Agent }

func New(cfg config.Agent) *Agent { return &Agent{cfg: cfg} }

func (a *Agent) Caps() agent.Caps {
	// Approvals are the CLI's own business (e.g. aider --yes-always), so only
	// the yolo-equivalent "ask" mode is meaningful here.
	return agent.Caps{Images: len(a.cfg.ImageArgs) > 0, Modes: []agent.Mode{agent.ModeAsk}}
}

func (a *Agent) Models(string) []string { return a.cfg.Models }
func (a *Agent) Close()                 {}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// StripANSI removes terminal color and cursor codes.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

func (a *Agent) argv(req agent.Request, prompt string, images []string) []string {
	vars := map[string]string{"prompt": prompt, "cwd": req.Cwd, "model": req.Model}
	argv := config.Expand(a.cfg.Wrap, vars)
	argv = append(argv, config.Expand(a.cfg.Command, vars)...)
	if req.Model != "" {
		argv = append(argv, config.Expand(a.cfg.ModelArgs, vars)...)
	}
	for _, p := range images {
		argv = append(argv, config.Expand(a.cfg.ImageArgs, map[string]string{"path": p})...)
	}
	return argv
}

func (a *Agent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	if req.Model == "" {
		req.Model = a.cfg.Model
	}
	var texts, images []string
	for _, b := range req.Prompt {
		if b.IsImage() {
			f, err := os.CreateTemp("", "pocketagent-image-*"+ext(b.MimeType))
			if err != nil {
				return agent.Result{}, err
			}
			f.Write(b.Image)
			f.Close()
			defer os.Remove(f.Name())
			images = append(images, f.Name())
		} else if b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	prompt := strings.Join(texts, "\n\n")
	if len(a.cfg.ImageArgs) == 0 {
		for _, p := range images {
			prompt += "\n[Image attached at " + p + "]"
		}
		images = nil
	}

	argv := a.argv(req, prompt, images)
	if timeout := a.cfg.Timeout.D(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), a.cfg.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	if a.cfg.Stdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	h.Tool("$ "+strings.Join(argv[:min(len(argv), 3)], " ")+" …", agent.KindExecute)
	err := cmd.Run()
	if ctx.Err() != nil {
		return agent.Result{}, ctx.Err()
	}
	text := strings.TrimSpace(StripANSI(out.String()))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return agent.Result{}, fmt.Errorf("%s failed: %s", argv[0], lastLines(text, 15))
	}
	if text == "" {
		text = "_(no output)_"
	}
	// CLI output is plain text; keep its formatting intact.
	if !strings.Contains(text, "```") {
		text = "```\n" + text + "\n```"
	}
	return agent.Result{FinalText: text}, nil
}

func ext(mime string) string {
	switch {
	case strings.Contains(mime, "jpeg"):
		return ".jpg"
	case strings.Contains(mime, "gif"):
		return ".gif"
	case strings.Contains(mime, "webp"):
		return ".webp"
	}
	return ".png"
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
