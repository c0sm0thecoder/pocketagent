package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// Settings is a conversation's effective configuration: conversation
// overrides, then the project, then the defaults.
type Settings struct {
	Agent, Model, Cwd, Project string
	Mode                       agent.Mode
	SessionID                  string
	Voice                      bool
	AlwaysAllow                []string
	TotalCost                  float64
}

func (c *Core) Settings(conv ConvID) Settings {
	st := c.Store.Get(string(conv))
	d := c.Config.Defaults
	s := Settings{
		Agent: d.Agent, Mode: d.Mode, Cwd: d.Cwd,
		Project: st.Project, SessionID: st.SessionID, Voice: st.Voice,
		AlwaysAllow: st.AlwaysAllow, TotalCost: st.TotalCost,
	}
	if p, ok := c.Config.Projects[st.Project]; ok {
		s.Cwd, s.Model = p.Cwd, p.Model
		s.Agent = cmpOr(p.Agent, s.Agent)
		s.Mode = cmpOr(p.Mode, s.Mode)
	}
	if _, ok := c.Agents[st.Agent]; ok {
		s.Agent = st.Agent
	}
	s.Model = cmpOr(st.Model, s.Model, c.Config.Agents[s.Agent].Model)
	s.Mode = cmpOr(agent.Mode(st.Mode), s.Mode)
	s.Cwd = cmpOr(st.Cwd, s.Cwd)
	return s
}

// cmpOr returns the first non-zero value.
func cmpOr[T comparable](vals ...T) T {
	var zero T
	for _, v := range vals {
		if v != zero {
			return v
		}
	}
	return zero
}

func (c *Core) update(conv ConvID, fn func(*store.Conversation)) {
	if err := c.Store.Update(string(conv), fn); err != nil {
		log.Printf("save state: %v", err)
	}
}

func resetSession(s *store.Conversation) {
	s.SessionID = ""
	s.AlwaysAllow = nil
}

func (c *Core) NewSession(conv ConvID) { c.update(conv, resetSession) }

func (c *Core) SetAgent(conv ConvID, name string) error {
	if _, ok := c.Agents[name]; !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	c.update(conv, func(s *store.Conversation) { s.Agent, s.Model = name, ""; resetSession(s) })
	return nil
}

func (c *Core) SetModel(conv ConvID, model string) {
	c.update(conv, func(s *store.Conversation) { s.Model = model })
}

func (c *Core) SetMode(conv ConvID, mode agent.Mode) error {
	if !slices.Contains(agent.Modes, mode) {
		return fmt.Errorf("mode must be one of %v", agent.Modes)
	}
	c.update(conv, func(s *store.Conversation) { s.Mode = string(mode) })
	return nil
}

func (c *Core) SetProject(conv ConvID, name string) error {
	if _, ok := c.Config.Projects[name]; !ok {
		return fmt.Errorf("unknown project %q", name)
	}
	c.update(conv, func(s *store.Conversation) {
		s.Project, s.Cwd, s.Agent, s.Model, s.Mode = name, "", "", "", ""
		resetSession(s)
	})
	return nil
}

func (c *Core) SetCwd(conv ConvID, dir string) (string, error) {
	dir = config.ExpandHome(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.Settings(conv).Cwd, dir)
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	// Agents keep sessions per directory, so a new cwd needs a new session.
	c.update(conv, func(s *store.Conversation) { s.Cwd = dir; resetSession(s) })
	return dir, nil
}

func (c *Core) ToggleVoice(conv ConvID) (bool, error) {
	if c.Speaker == nil {
		return false, fmt.Errorf("voice replies are off: configure tts")
	}
	var on bool
	c.update(conv, func(s *store.Conversation) { s.Voice = !s.Voice; on = s.Voice })
	return on, nil
}

// AgentNames lists the configured agents, sorted.
func (c *Core) AgentNames() []string { return c.Config.AgentNames() }

// AgentType is the adapter type of a configured agent.
func (c *Core) AgentType(name string) string { return c.Config.Agents[name].Type }

// Models offers the configured models plus any the agent reports.
func (c *Core) Models(conv ConvID) []string {
	name := c.Settings(conv).Agent
	models := slices.Clone(c.Config.Agents[name].Models)
	if l, ok := c.Agents[name].(agent.ModelLister); ok {
		for _, m := range l.Models(string(conv)) {
			if !slices.Contains(models, m) {
				models = append(models, m)
			}
		}
	}
	return models
}

// Caps reports what the conversation's agent supports.
func (c *Core) Caps(conv ConvID) agent.Caps {
	if a := c.Agents[c.Settings(conv).Agent]; a != nil {
		return a.Caps()
	}
	return agent.Caps{}
}

func (c *Core) Sessions(conv ConvID) []store.SessionRecord { return c.Store.Get(string(conv)).Sessions }

func (c *Core) ResumeSession(conv ConvID, id string) (store.SessionRecord, error) {
	for _, r := range c.Sessions(conv) {
		if r.ID != id {
			continue
		}
		c.update(conv, func(s *store.Conversation) {
			s.SessionID, s.Cwd, s.AlwaysAllow = r.ID, r.Cwd, nil
			if _, ok := c.Agents[r.Agent]; ok {
				s.Agent = r.Agent
			}
		})
		return r, nil
	}
	return store.SessionRecord{}, fmt.Errorf("session not found")
}

// Usage is what a user spent today and over the last 30 days.
func (c *Core) Usage(user string) (today, month float64) { return c.Store.Spend(user) }
