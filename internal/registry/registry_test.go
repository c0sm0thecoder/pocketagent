package registry_test

import (
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/registry"
)

// The example config builds with the real adapters and providers, whose
// option decoding is strict, so the documentation can't drift from the code.
func TestExampleConfigBuilds(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "1:x")
	cfg, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cfg.AgentNames() {
		a, err := registry.NewAgent(name, cfg.Agents[name])
		if err != nil {
			t.Errorf("agent %s: %v", name, err)
			continue
		}
		if c, ok := a.(io.Closer); ok {
			c.Close()
		}
	}
	if _, err := registry.NewTranscriber(cfg.Transcriber); err != nil {
		t.Errorf("transcriber: %v", err)
	}
	if _, err := registry.NewSpeaker(cfg.TTS); err != nil {
		t.Errorf("tts: %v", err)
	}
}

// Each preset is valid config, and the example documents it identically.
func TestPresetsMatchExample(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "1:x")
	example, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range registry.Presets {
		var a config.Agent
		if err := yaml.Unmarshal([]byte(p.Config), &a); err != nil {
			t.Fatalf("preset %s: %v", p.Name, err)
		}
		if _, err := registry.NewAgent(p.Name, a); err != nil {
			t.Errorf("preset %s doesn't build: %v", p.Name, err)
		}
		ex, ok := example.Agents[p.Name]
		if !ok {
			t.Errorf("preset %s is missing from config.example.yaml", p.Name)
			continue
		}
		if ex.Type != a.Type || strings.Join(ex.Command, " ") != strings.Join(a.Command, " ") {
			t.Errorf("preset %s differs from the example: %v %v vs %v %v", p.Name, a.Type, a.Command, ex.Type, ex.Command)
		}
	}
}

func TestPresetsAreSortedAndNeutral(t *testing.T) {
	for i := 1; i < len(registry.Presets); i++ {
		if registry.Presets[i-1].Name >= registry.Presets[i].Name {
			t.Errorf("presets not sorted at %s", registry.Presets[i].Name)
		}
	}
}

func TestUnknownTypes(t *testing.T) {
	_, err := registry.NewAgent("x", config.Agent{Type: "nope", Command: []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "acp, claude-code, command") {
		t.Errorf("err = %v", err)
	}
	if _, err := registry.NewTranscriber(config.Provider{Type: "nope"}); err == nil {
		t.Error("unknown transcriber accepted")
	}
	if s, err := registry.NewSpeaker(config.Provider{Type: registry.None}); s != nil || err != nil {
		t.Errorf("none: %v %v", s, err)
	}
}
