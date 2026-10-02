package command

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type nopHandler struct{}

func (nopHandler) Session(string)              {}
func (nopHandler) Message(string)              {}
func (nopHandler) ToolCall(string, agent.Kind) {}
func (nopHandler) Permission(context.Context, agent.Permission) agent.Decision {
	return agent.Decision{}
}
func (nopHandler) SendFile(context.Context, string, string) error { return nil }

func newAgent(t *testing.T, command []string, opts map[string]any) agent.Agent {
	t.Helper()
	a, err := New(agent.Spec{Name: "test", Command: command, Options: config.OptionsFrom(opts)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRejectsUnknownOptions(t *testing.T) {
	if _, err := New(agent.Spec{Command: []string{"x"}, Options: config.OptionsFrom(map[string]any{"bogus": 1})}); err == nil {
		t.Error("unknown option accepted")
	}
}

func TestTemplate(t *testing.T) {
	a := newAgent(t, []string{"sh", "-c", `printf '\033[32mgot:\033[0m %s|%s|' "$0" "$*"; pwd`, "{prompt}"},
		map[string]any{"model_args": []string{"--model={model}"}, "image_args": []string{"--image", "{path}"}})
	dir := t.TempDir()
	res, err := a.Run(context.Background(), agent.Request{
		Cwd: dir, Model: "big",
		Prompt: []agent.Block{{Text: "hello world"}, {Image: []byte("x"), MimeType: "image/png"}},
	}, nopHandler{})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Final
	for _, want := range []string{"got: hello world|", "--model=big", "--image /", ".png", dir} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033") {
		t.Errorf("ANSI codes not stripped: %q", out)
	}
}

func TestStdinAndFailure(t *testing.T) {
	a := newAgent(t, []string{"sh", "-c", "cat; exit 3"}, map[string]any{"stdin": true})
	_, err := a.Run(context.Background(), agent.Request{Cwd: t.TempDir(), Prompt: []agent.Block{{Text: "from stdin"}}}, nopHandler{})
	if err == nil || !strings.Contains(err.Error(), "from stdin") {
		t.Errorf("err = %v", err)
	}
}

func TestCancelKillsProcessGroup(t *testing.T) {
	a := newAgent(t, []string{"sh", "-c", "sleep 30 & wait"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Run(ctx, agent.Request{Cwd: t.TempDir(), Prompt: []agent.Block{{Text: "x"}}}, nopHandler{})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancel took %v, err %v", time.Since(start), err)
	}
}
