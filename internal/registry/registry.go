// Package registry maps config type names to implementations. It is the
// only package that knows the concrete agents and speech providers; adding
// one means adding a line here.
package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/acp"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/claudecode"
	"github.com/c0sm0thecoder/pocketagent/internal/agent/command"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/stt"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// Agents are the agent adapters, by config type.
var Agents = map[string]agent.Factory{
	"acp":         acp.New,
	"claude-code": claudecode.New,
	"command":     command.New,
}

// Transcribers are the speech-to-text providers, by config type.
var Transcribers = map[string]stt.Factory{
	"command":     stt.NewCommand,
	"http":        stt.NewHTTP,
	"whisper-cpp": stt.NewWhisperCpp,
}

// Speakers are the text-to-speech providers, by config type.
var Speakers = map[string]tts.Factory{
	"command": tts.NewCommand,
	"http":    tts.NewHTTP,
	"say":     tts.NewSay,
}

// None disables a speech provider.
const None = "none"

// NewAgent builds the agent configured under name.
func NewAgent(name string, c config.Agent) (agent.Agent, error) {
	f, err := lookup(Agents, c.Type)
	if err != nil {
		return nil, fmt.Errorf("agents.%s: %w", name, err)
	}
	a, err := f(c.Spec(name))
	if err != nil {
		return nil, fmt.Errorf("agents.%s: %w", name, err)
	}
	return a, nil
}

// NewTranscriber returns nil when transcription is off.
func NewTranscriber(p config.Provider) (stt.Transcriber, error) {
	if p.Type == None {
		return nil, nil
	}
	f, err := lookup(Transcribers, p.Type)
	if err != nil {
		return nil, fmt.Errorf("transcriber: %w", err)
	}
	t, err := f(p.Options)
	if err != nil {
		return nil, fmt.Errorf("transcriber (%s): %w", p.Type, err)
	}
	return t, nil
}

// NewSpeaker returns nil when voice replies are off.
func NewSpeaker(p config.Provider) (tts.Speaker, error) {
	if p.Type == None {
		return nil, nil
	}
	f, err := lookup(Speakers, p.Type)
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	s, err := f(p.Options)
	if err != nil {
		return nil, fmt.Errorf("tts (%s): %w", p.Type, err)
	}
	return s, nil
}

func lookup[F any](m map[string]F, typ string) (F, error) {
	f, ok := m[typ]
	if !ok {
		var zero F
		return zero, fmt.Errorf("unknown type %q (one of: %s)", typ, strings.Join(Names(m), ", "))
	}
	return f, nil
}

// Names returns the keys of a registry map, sorted.
func Names[F any](m map[string]F) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}
