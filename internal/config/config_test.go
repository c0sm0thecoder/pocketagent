package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	c, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.Token != "123:abc" {
		t.Errorf("env not expanded: %q", c.Telegram.Token)
	}
	if c.Defaults.Agent != "claude" || c.Agents["claude"].Command[0] != "claude" {
		t.Errorf("agents: %+v", c.Agents)
	}
	if c.ApprovalTimeout.D().Minutes() != 10 {
		t.Errorf("timeout = %v", c.ApprovalTimeout.D())
	}
	if !strings.HasPrefix(c.Projects["pocketagent"].Cwd, "/") {
		t.Errorf("project cwd not expanded: %q", c.Projects["pocketagent"].Cwd)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"telegram:\n  token: x\n": "allowed_users",
		"telegram:\n  token: x\n  allowed_users: [1]\nagents:\n  a: {type: nope}\n":  "type must be",
		"telegram:\n  token: x\n  allowed_users: [1]\ndefaults:\n  agent: missing\n": "not in agents",
		"telegram:\n  token: x\n  allowed_users: [1]\nbogus: 1\n":                    "bogus",
	}
	for body, want := range cases {
		p := filepath.Join(t.TempDir(), "c.yaml")
		os.WriteFile(p, []byte(body), 0o600)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("config %q: err = %v, want %q", body, err, want)
		}
	}
}

func TestCommandAcceptsStringOrList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(`telegram: {token: x, allowed_users: [1]}
agents:
  a: {type: acp, command: "gemini --acp"}
  b: {type: acp, command: [npx, -y, "@zed-industries/codex-acp"]}
`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Agents["a"].Command, " ") != "gemini --acp" || len(c.Agents["b"].Command) != 3 {
		t.Errorf("commands: %v %v", c.Agents["a"].Command, c.Agents["b"].Command)
	}
}
