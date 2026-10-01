//go:build integration

package command

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

// The agent runs inside a container that only sees the project directory.
func TestDockerWrap(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker not running")
	}
	dir := t.TempDir()
	a := New(config.Agent{
		Wrap:    config.Command{"docker", "run", "--rm", "-i", "-v", "{cwd}:{cwd}", "-w", "{cwd}", "alpine:3"},
		Command: config.Command{"sh", "-c", `echo "$0" > note.txt; pwd; ls /root 2>&1 | head -1; cat /etc/alpine-release`, "{prompt}"},
	})
	res, err := a.Run(context.Background(), agent.Request{Cwd: dir, Prompt: []agent.Block{{Text: "sandboxed hello"}}}, nopHandler{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s", res.FinalText)
	if !strings.Contains(res.FinalText, dir) {
		t.Errorf("cwd not mounted: %s", res.FinalText)
	}
	out, _ := exec.Command("cat", dir+"/note.txt").Output()
	if strings.TrimSpace(string(out)) != "sandboxed hello" {
		t.Errorf("file written in container not visible on host: %q", out)
	}
}
