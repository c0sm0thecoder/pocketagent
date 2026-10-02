package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// editModeKinds run without asking in edits mode.
var editModeKinds = []agent.Kind{agent.KindEdit, agent.KindRead, agent.KindSearch, agent.KindThink}

// autoDecision decides p without asking the user, if the policy allows it.
func autoDecision(mode agent.Mode, autoAllow []agent.Kind, always []string, p agent.Permission) (agent.Option, bool) {
	allowed := mode == agent.ModeFull ||
		(p.Key != "" && slices.Contains(always, p.Key)) ||
		(mode == agent.ModeEdits && slices.Contains(editModeKinds, p.Kind)) ||
		slices.Contains(autoAllow, p.Kind)
	if !allowed {
		return agent.Option{}, false
	}
	if o, ok := p.First(func(k agent.OptionKind) bool { return k == agent.AllowOnce }); ok {
		return o, true
	}
	return p.First(agent.OptionKind.Allows)
}

// pending is an approval waiting for the user.
type pending struct {
	conv ConvID
	p    agent.Permission
	ch   chan agent.Decision
}

func (c *Core) pendingFor(conv ConvID) *pending {
	for _, p := range c.approvals {
		if p.conv == conv {
			return p
		}
	}
	return nil
}

// Answer delivers the user's choice for an approval. It returns false if
// the request is no longer pending.
func (c *Core) Answer(id, optionID string) bool {
	c.mu.Lock()
	p := c.approvals[id]
	c.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case p.ch <- agent.Decision{OptionID: optionID}:
	default:
	}
	return true
}

func reject(p agent.Permission) string {
	o, _ := p.First(func(k agent.OptionKind) bool { return !k.Allows() })
	return o.ID
}

// ask applies the policy and, if needed, waits for the user.
func (c *Core) ask(ctx context.Context, conv ConvID, mode agent.Mode, progress Progress, p agent.Permission) agent.Decision {
	if len(p.Options) == 0 {
		p.Options = agent.DefaultOptions()
	}
	always := c.Store.Get(string(conv)).AlwaysAllow
	if o, ok := autoDecision(mode, c.Config.Permissions.AutoAllow, always, p); ok {
		return agent.Decision{OptionID: o.ID}
	}

	id := randomID()
	pd := &pending{conv: conv, p: p, ch: make(chan agent.Decision, 1)}
	c.mu.Lock()
	c.approvals[id] = pd
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
	}()

	progress.Flush()
	view := c.ui.AskApproval(conv, id, p)

	var d agent.Decision
	outcome := "⌛ Timed out"
	select {
	case d = <-pd.ch:
		outcome = c.record(conv, p, d)
	case <-ctx.Done():
		outcome = "⏹ Cancelled"
	case <-time.After(c.Config.Permissions.Timeout.D()):
	}
	if d.OptionID == "" {
		d.OptionID = reject(p) // the agent must hear back to move on
	}
	view.Resolve(outcome)
	return d
}

// record remembers "always allow" choices and describes the outcome.
func (c *Core) record(conv ConvID, p agent.Permission, d agent.Decision) string {
	o, _ := p.Find(d.OptionID)
	if o.Kind == agent.AllowAlways && p.Key != "" {
		c.update(conv, func(st *store.Conversation) {
			if !slices.Contains(st.AlwaysAllow, p.Key) {
				st.AlwaysAllow = append(st.AlwaysAllow, p.Key)
			}
		})
	}
	switch {
	case o.Kind.Allows():
		return "✅ " + o.Label
	case d.Message != "":
		return "❌ Denied: " + d.Message
	}
	return "❌ Denied"
}

// denyWithReason answers a pending approval with the user's text. Agents
// that can't carry a reason get the text as their next message instead.
func (c *Core) denyWithReason(conv ConvID, p *pending, in Input) {
	select {
	case p.ch <- agent.Decision{OptionID: reject(p.p), Message: in.Text}:
	default:
	}
	if !c.Caps(conv).DenyMessage {
		c.mu.Lock()
		rt := c.runtimes[conv]
		rt.queue = append([]Input{in}, rt.queue...)
		c.mu.Unlock()
	}
}

func randomID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
