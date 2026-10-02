package config

import (
	"strings"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
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
	if _, ok := c.Agents[c.Defaults.Agent]; !ok {
		t.Errorf("default agent %q not configured", c.Defaults.Agent)
	}
	if c.Permissions.Timeout.D() != 10*time.Minute {
		t.Errorf("timeout = %v", c.Permissions.Timeout.D())
	}
}

const base = "telegram: {token: x, allowed_users: [1]}\n"

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"telegram: {token: x}\nagents: {a: {type: t, command: c}}\n": "allowed_users",
		base:                                 "at least one agent",
		base + "agents: {a: {command: c}}\n": "type is required",
		base + "agents: {a: {type: t}}\n":    "command is required",
		base + "agents: {a: {type: t, command: c}}\ndefaults: {agent: b}\n":                 "not in agents",
		base + "agents: {a: {type: t, command: c}}\ndefaults: {mode: wild}\n":               "not one of",
		base + "agents: {a: {type: t, command: c}}\npermissions: {auto_allow: [execute]}\n": "too broad",
		base + "agents: {a: {type: t, command: c}}\nbogus: 1\n":                             "bogus",
	}
	for body, want := range cases {
		_, err := Parse([]byte(body), "/tmp/c.yaml")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("config %q: err = %v, want %q", body, err, want)
		}
	}
}

// Keys an agent's adapter owns are passed through as Options, untouched.
func TestAgentOptionsPassThrough(t *testing.T) {
	c, err := Parse([]byte(base+`agents:
  a:
    type: command
    command: "tool --flag"
    models: [m1]
    stdin: true
    image_args: ["{path}"]
`), "/tmp/c.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Agents["a"]
	if strings.Join(a.Command, " ") != "tool --flag" || a.Models[0] != "m1" {
		t.Errorf("common fields: %+v", a)
	}
	var opts struct {
		Stdin     bool     `yaml:"stdin"`
		ImageArgs []string `yaml:"image_args"`
	}
	if err := a.Options.Decode(&opts); err != nil || !opts.Stdin || opts.ImageArgs[0] != "{path}" {
		t.Errorf("options = %+v, %v", opts, err)
	}
	// Decoding is strict: an adapter that doesn't know a key rejects it.
	var narrow struct {
		Stdin bool `yaml:"stdin"`
	}
	if err := a.Options.Decode(&narrow); err == nil || !strings.Contains(err.Error(), "image_args") {
		t.Errorf("unknown option accepted: %v", err)
	}
	if c.Defaults.Agent != "a" || c.Defaults.Mode != agent.ModeAsk {
		t.Errorf("defaults = %+v", c.Defaults)
	}
}

func TestProviderOptions(t *testing.T) {
	c, err := Parse([]byte(base+"agents: {a: {type: t, command: c}}\ntranscriber: {type: http, base_url: u, model: m}\n"), "/tmp/c.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		BaseURL string `yaml:"base_url"`
		Model   string `yaml:"model"`
	}
	if c.Transcriber.Type != "http" || c.Transcriber.Options.Decode(&o) != nil || o.BaseURL != "u" {
		t.Errorf("transcriber = %+v %+v", c.Transcriber, o)
	}
	if c.TTS.Type != "none" {
		t.Errorf("tts default = %q", c.TTS.Type)
	}
}
