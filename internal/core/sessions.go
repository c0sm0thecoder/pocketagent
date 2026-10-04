package core

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// SessionChoice is a session the conversation can continue.
type SessionChoice struct {
	ID, Agent, Cwd, Title string
	Updated               time.Time
	// Elsewhere marks sessions started outside this conversation, such as
	// in a terminal on the laptop.
	Elsewhere bool
}

// maxSessionChoices bounds the /sessions list.
const maxSessionChoices = 15

// Sessions lists sessions to continue: those started in this conversation,
// plus the agent's own sessions in the current folder (for agents that can
// list them), newest first.
func (c *Core) Sessions(ctx context.Context, conv ConvID) []SessionChoice {
	s := c.Settings(conv)
	var out []SessionChoice
	seen := map[string]bool{}
	for _, r := range c.Store.Get(string(conv)).Sessions {
		seen[r.ID] = true
		out = append(out, SessionChoice{ID: r.ID, Agent: r.Agent, Cwd: r.Cwd, Title: r.Title, Updated: r.Updated})
	}
	if l, ok := c.Agents[s.Agent].(agent.SessionLister); ok {
		listed, err := l.Sessions(ctx, string(conv), s.Cwd)
		if err == nil {
			for _, x := range listed {
				if seen[x.ID] {
					continue
				}
				out = append(out, SessionChoice{ID: x.ID, Agent: s.Agent, Cwd: s.Cwd, Title: x.Title, Updated: x.Updated, Elsewhere: true})
			}
		}
	}
	slices.SortFunc(out, func(a, b SessionChoice) int { return b.Updated.Compare(a.Updated) })
	if len(out) > maxSessionChoices {
		out = out[:maxSessionChoices]
	}
	return out
}

// ResumeSession continues a session from Sessions in this conversation.
func (c *Core) ResumeSession(ctx context.Context, conv ConvID, id string) (SessionChoice, error) {
	for _, r := range c.Sessions(ctx, conv) {
		if r.ID != id {
			continue
		}
		c.update(conv, func(s *store.Conversation) {
			s.SessionID, s.Cwd, s.AlwaysAllow = r.ID, r.Cwd, nil
			if _, ok := c.Agents[r.Agent]; ok {
				s.Agent = r.Agent
			}
			title := r.Title
			if title == "" {
				title = "(untitled)"
			}
			s.RecordSession(store.SessionRecord{ID: r.ID, Agent: r.Agent, Cwd: r.Cwd, Title: title, Updated: time.Now()})
		})
		return r, nil
	}
	return SessionChoice{}, fmt.Errorf("session not found")
}

// ResumeHint is the terminal command that continues the conversation's
// current session, if the agent can provide one.
func (c *Core) ResumeHint(conv ConvID) string {
	s := c.Settings(conv)
	h, ok := c.Agents[s.Agent].(agent.ResumeHinter)
	if !ok || s.SessionID == "" {
		return ""
	}
	return h.ResumeCommand(s.Cwd, s.SessionID)
}
