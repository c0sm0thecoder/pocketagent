package core

import (
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

func TestAutoDecision(t *testing.T) {
	auto := []agent.Kind{agent.KindRead, agent.KindSearch}
	perm := func(kind agent.Kind, key string) agent.Permission {
		return agent.Permission{Kind: kind, Key: key, Options: agent.DefaultOptions()}
	}
	cases := []struct {
		name   string
		mode   agent.Mode
		always []string
		p      agent.Permission
		want   string // option id, or "" for "ask the user"
	}{
		{"auto-allowed kind", agent.ModeAsk, nil, perm(agent.KindRead, ""), "allow"},
		{"command asks", agent.ModeAsk, nil, perm(agent.KindExecute, ""), ""},
		{"edit asks in ask mode", agent.ModeAsk, nil, perm(agent.KindEdit, ""), ""},
		{"edit allowed in edits mode", agent.ModeEdits, nil, perm(agent.KindEdit, ""), "allow"},
		{"command asks in edits mode", agent.ModeEdits, nil, perm(agent.KindExecute, ""), ""},
		{"plan mode still asks", agent.ModePlan, nil, perm(agent.KindEdit, ""), ""},
		{"full allows everything", agent.ModeFull, nil, perm(agent.KindDelete, ""), "allow"},
		{"always-allowed key", agent.ModeAsk, []string{"Bash"}, perm(agent.KindExecute, "Bash"), "allow"},
		{"other key still asks", agent.ModeAsk, []string{"Bash"}, perm(agent.KindExecute, "Write"), ""},
		{"no key never matches", agent.ModeAsk, []string{""}, perm(agent.KindExecute, ""), ""},
	}
	for _, tc := range cases {
		o, ok := autoDecision(tc.mode, auto, tc.always, tc.p)
		got := ""
		if ok {
			got = o.ID
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// When an agent offers only "allow always" style options, auto-allow still
// picks an allowing option rather than none.
func TestAutoDecisionAgentOptions(t *testing.T) {
	p := agent.Permission{Kind: agent.KindRead, Options: []agent.Option{
		{ID: "r", Kind: agent.RejectOnce}, {ID: "aa", Kind: agent.AllowAlways},
	}}
	if o, ok := autoDecision(agent.ModeAsk, []agent.Kind{agent.KindRead}, nil, p); !ok || o.ID != "aa" {
		t.Errorf("got %+v %v", o, ok)
	}
}
