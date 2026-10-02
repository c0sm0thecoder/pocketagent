// Package command wraps any CLI that takes a prompt and prints a reply,
// configured as an argv template. It covers agents that don't speak ACP.
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/argv"
	"github.com/c0sm0thecoder/pocketagent/internal/term"
)

// options are the command adapter's config keys. The command itself may
// use {prompt}, {cwd} and {model}.
type options struct {
	ModelArgs []string      `yaml:"model_args"` // appended when a model is chosen; supports {model}
	ImageArgs []string      `yaml:"image_args"` // appended once per image; supports {path}
	Stdin     bool          `yaml:"stdin"`      // send the prompt on stdin instead of {prompt}
	Timeout   time.Duration `yaml:"timeout"`
}

type Agent struct {
	spec agent.Spec
	opts options
}

var _ agent.Agent = (*Agent)(nil)

func New(spec agent.Spec) (agent.Agent, error) {
	a := &Agent{spec: spec}
	if err := spec.Options.Decode(&a.opts); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Agent) Caps() agent.Caps {
	// Approvals are the CLI's own business (for example aider --yes-always),
	// so pocketagent's modes can't be enforced here.
	return agent.Caps{Images: len(a.opts.ImageArgs) > 0, Modes: []agent.Mode{agent.ModeAsk}}
}

func (a *Agent) cmdline(req agent.Request, prompt string, images []string) []string {
	vars := map[string]string{"prompt": prompt, "cwd": req.Cwd, "model": req.Model}
	line := argv.Join(a.spec.Wrap, a.spec.Command, vars)
	if req.Model != "" {
		line = append(line, argv.Expand(a.opts.ModelArgs, vars)...)
	}
	for _, p := range images {
		line = append(line, argv.Expand(a.opts.ImageArgs, map[string]string{"path": p})...)
	}
	return line
}

func (a *Agent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	var texts, images []string
	for _, b := range req.Prompt {
		if !b.IsImage() {
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
			continue
		}
		path, err := saveImage(b)
		if err != nil {
			return agent.Result{}, err
		}
		defer os.Remove(path)
		images = append(images, path)
	}
	prompt := strings.Join(texts, "\n\n")
	if len(a.opts.ImageArgs) == 0 {
		for _, p := range images {
			prompt += "\n[Image attached at " + p + "]"
		}
		images = nil
	}

	if a.opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.opts.Timeout)
		defer cancel()
	}
	line := a.cmdline(req, prompt, images)
	cmd := exec.CommandContext(ctx, line[0], line[1:]...)
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), a.spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	if a.opts.Stdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	h.ToolCall("$ "+strings.Join(line[:min(len(line), 3)], " ")+" …", agent.KindExecute)
	err := cmd.Run()
	if ctx.Err() != nil {
		return agent.Result{}, ctx.Err()
	}
	text := strings.TrimSpace(term.StripANSI(out.String()))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return agent.Result{}, fmt.Errorf("%s failed: %s", a.spec.Name, lastLines(text, 15))
	}
	if text == "" {
		text = "_(no output)_"
	}
	// CLI output is plain text; keep its layout intact.
	if !strings.Contains(text, "```") {
		text = "```\n" + text + "\n```"
	}
	return agent.Result{Final: text}, nil
}

func saveImage(b agent.Block) (string, error) {
	ext := map[string]string{"image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[b.MimeType]
	if ext == "" {
		ext = ".png"
	}
	f, err := os.CreateTemp("", "pocketagent-image-*"+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(b.Image)
	return f.Name(), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
