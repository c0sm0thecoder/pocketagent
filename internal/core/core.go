// Package core runs conversations: it picks the agent, queues prompts,
// applies the permission policy, tracks cost and takes git checkpoints.
// It knows nothing about Telegram; the frontend implements UI.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/gitcp"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
	"github.com/c0sm0thecoder/pocketagent/internal/stt"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// Conv identifies a conversation: a chat, or a forum topic inside one.
type Conv struct {
	ChatID   int64
	ThreadID int
}

func (c Conv) Key() string { return strconv.FormatInt(c.ChatID, 10) + ":" + strconv.Itoa(c.ThreadID) }

// UI is implemented by the chat frontend.
type UI interface {
	// Reply sends agent Markdown. Long replies may become a file.
	Reply(conv Conv, md string)
	// Notice sends a short plain-text status line.
	Notice(conv Conv, text string)
	// Progress starts a compact "working" indicator for one turn.
	Progress(conv Conv) Progress
	// ShowApproval presents a permission request; the user's choice comes
	// back through Core.Answer(id, optionID).
	ShowApproval(conv Conv, id string, p agent.Permission) ApprovalView
	SendFile(ctx context.Context, conv Conv, path, caption string) error
	SendVoice(ctx context.Context, conv Conv, ogg []byte) error
}

type Progress interface {
	Tool(title string)
	// Flush is called before agent text is sent, so tool lines stay above it.
	Flush()
	Done(summary string)
}

type ApprovalView interface {
	Resolve(outcome string)
}

// Input is one user message.
type Input struct {
	User   int64
	Blocks []agent.Block
	Text   string // the text part, used for titles and "deny with reason"
}

type pending struct {
	conv Conv
	p    agent.Permission
	ch   chan agent.Decision
}

type runtime struct {
	running bool
	cancel  context.CancelFunc // the current turn's; nil between turns
	stopReq bool               // /stop arrived before the turn registered cancel
	queue   []Input
}

type Core struct {
	cfg    *config.Config
	store  *store.Store
	agents map[string]agent.Agent
	bridge *bridge.Bridge
	STT    stt.Transcriber
	TTS    tts.Speaker
	ui     UI

	mu        sync.Mutex
	runtimes  map[string]*runtime
	approvals map[string]*pending
}

func New(cfg *config.Config, st *store.Store, agents map[string]agent.Agent, br *bridge.Bridge, tr stt.Transcriber, sp tts.Speaker) *Core {
	return &Core{
		cfg: cfg, store: st, agents: agents, bridge: br, STT: tr, TTS: sp,
		runtimes:  map[string]*runtime{},
		approvals: map[string]*pending{},
	}
}

// SetUI must be called before Submit.
func (c *Core) SetUI(ui UI) { c.ui = ui }

func (c *Core) Config() *config.Config { return c.cfg }

// ---------- settings ----------

// Settings is a conversation's effective configuration.
type Settings struct {
	Agent, Model, Mode, Cwd, Project string
	SessionID                        string
	Voice                            bool
	AlwaysAllow                      []string
	TotalCost                        float64
}

func (c *Core) Settings(conv Conv) Settings {
	st := c.store.Get(conv.Key())
	s := Settings{
		Agent: c.cfg.Defaults.Agent, Mode: c.cfg.Defaults.Mode, Cwd: c.cfg.Defaults.Cwd,
		Project: st.Project, SessionID: st.SessionID, Voice: st.Voice,
		AlwaysAllow: st.AlwaysAllow, TotalCost: st.TotalCost,
	}
	if p, ok := c.cfg.Projects[st.Project]; ok {
		s.Cwd = p.Cwd
		if p.Agent != "" {
			s.Agent = p.Agent
		}
		if p.Mode != "" {
			s.Mode = p.Mode
		}
		s.Model = p.Model
	}
	if st.Agent != "" {
		if _, ok := c.agents[st.Agent]; ok {
			s.Agent = st.Agent
		}
	}
	if st.Model != "" {
		s.Model = st.Model
	}
	if st.Mode != "" {
		s.Mode = st.Mode
	}
	if st.Cwd != "" {
		s.Cwd = st.Cwd
	}
	if s.Model == "" {
		s.Model = c.cfg.Agents[s.Agent].Model
	}
	return s
}

func (c *Core) update(conv Conv, fn func(*store.Conversation)) {
	if err := c.store.Update(conv.Key(), fn); err != nil {
		log.Printf("save state: %v", err)
	}
}

func resetSession(s *store.Conversation) {
	s.SessionID = ""
	s.SessionCost = 0
	s.AlwaysAllow = nil
}

func (c *Core) NewSession(conv Conv) { c.update(conv, resetSession) }

func (c *Core) SetAgent(conv Conv, name string) error {
	if _, ok := c.agents[name]; !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	c.update(conv, func(s *store.Conversation) { s.Agent = name; s.Model = ""; resetSession(s) })
	return nil
}

func (c *Core) SetModel(conv Conv, model string) {
	c.update(conv, func(s *store.Conversation) { s.Model = model })
}

func (c *Core) SetMode(conv Conv, mode string) error {
	if !slices.Contains(config.Modes, mode) {
		return fmt.Errorf("mode must be one of %s", strings.Join(config.Modes, ", "))
	}
	c.update(conv, func(s *store.Conversation) { s.Mode = mode })
	return nil
}

func (c *Core) SetProject(conv Conv, name string) error {
	if _, ok := c.cfg.Projects[name]; !ok {
		return fmt.Errorf("unknown project %q", name)
	}
	c.update(conv, func(s *store.Conversation) {
		s.Project, s.Cwd, s.Agent, s.Model, s.Mode = name, "", "", "", ""
		resetSession(s)
	})
	return nil
}

func (c *Core) SetCwd(conv Conv, dir string) (string, error) {
	dir = config.ExpandHome(dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.Settings(conv).Cwd, dir)
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	// Agents store sessions per directory, so a new cwd needs a new session.
	c.update(conv, func(s *store.Conversation) { s.Cwd = dir; resetSession(s) })
	return dir, nil
}

func (c *Core) ToggleVoice(conv Conv) (bool, error) {
	if c.TTS == nil {
		return false, fmt.Errorf("voice replies are off: set tts in the config")
	}
	var on bool
	c.update(conv, func(s *store.Conversation) { s.Voice = !s.Voice; on = s.Voice })
	return on, nil
}

func (c *Core) Agents() []string { return c.cfg.AgentNames() }

func (c *Core) Models(conv Conv) []string {
	s := c.Settings(conv)
	if a := c.agents[s.Agent]; a != nil {
		return a.Models(conv.Key())
	}
	return nil
}

func (c *Core) Caps(conv Conv) agent.Caps {
	if a := c.agents[c.Settings(conv).Agent]; a != nil {
		return a.Caps()
	}
	return agent.Caps{}
}

func (c *Core) Sessions(conv Conv) []store.SessionRecord { return c.store.Get(conv.Key()).Sessions }

func (c *Core) ResumeSession(conv Conv, id string) (store.SessionRecord, error) {
	for _, r := range c.store.Get(conv.Key()).Sessions {
		if r.ID == id {
			c.update(conv, func(s *store.Conversation) {
				s.SessionID, s.Cwd, s.SessionCost, s.AlwaysAllow = r.ID, r.Cwd, 0, nil
				if _, ok := c.agents[r.Agent]; ok {
					s.Agent = r.Agent
				}
			})
			return r, nil
		}
	}
	return store.SessionRecord{}, fmt.Errorf("session not found")
}

func (c *Core) Usage(user int64) (today, month float64) {
	return c.store.Spend(strconv.FormatInt(user, 10))
}

func (c *Core) Running(conv Conv) (running bool, queued int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt := c.runtimes[conv.Key()]; rt != nil {
		return rt.running, len(rt.queue)
	}
	return false, 0
}

// ---------- checkpoints ----------

// Diff shows changes since the last checkpoint, or since HEAD when all is true.
func (c *Core) Diff(ctx context.Context, conv Conv, all bool) (string, error) {
	s := c.Settings(conv)
	from := "HEAD"
	if !all {
		cps := c.store.Get(conv.Key()).Checkpoints
		if len(cps) == 0 {
			return "", fmt.Errorf("no checkpoint yet; use /diff all to compare with HEAD")
		}
		cp := cps[len(cps)-1]
		from, s.Cwd = cp.Commit, cp.Cwd
	}
	return gitcp.Diff(ctx, s.Cwd, from)
}

// Undo restores the working tree to the state before the last turn.
func (c *Core) Undo(ctx context.Context, conv Conv) (store.Checkpoint, error) {
	if running, _ := c.Running(conv); running {
		return store.Checkpoint{}, fmt.Errorf("wait for the current run to finish (or /stop it)")
	}
	cps := c.store.Get(conv.Key()).Checkpoints
	if len(cps) == 0 {
		return store.Checkpoint{}, fmt.Errorf("nothing to undo")
	}
	cp := cps[len(cps)-1]
	if err := gitcp.Restore(ctx, cp.Cwd, cp.Commit); err != nil {
		return cp, err
	}
	c.update(conv, func(s *store.Conversation) {
		if n := len(s.Checkpoints); n > 0 {
			s.Checkpoints = s.Checkpoints[:n-1]
		}
	})
	return cp, nil
}

// ---------- running ----------

// Submit runs the input now, or queues it if the conversation is busy.
// While an approval is pending, text input denies it with that text as the reason.
func (c *Core) Submit(conv Conv, in Input) {
	c.mu.Lock()
	rt := c.runtimes[conv.Key()]
	if rt == nil {
		rt = &runtime{}
		c.runtimes[conv.Key()] = rt
	}
	if rt.running {
		if p := c.pendingFor(conv); p != nil && in.Text != "" && !hasImage(in.Blocks) {
			c.mu.Unlock()
			c.denyWithReason(conv, p, in)
			return
		}
		rt.queue = append(rt.queue, in)
		n := len(rt.queue)
		c.mu.Unlock()
		c.ui.Notice(conv, fmt.Sprintf("📥 Queued (%d waiting). /stop cancels the current run and the queue.", n))
		return
	}
	rt.running = true
	c.mu.Unlock()
	go c.loop(conv, rt, in)
}

func (c *Core) denyWithReason(conv Conv, p *pending, in Input) {
	opt := ""
	for _, o := range p.p.Options {
		if !o.Kind.Allows() {
			opt = o.ID
			break
		}
	}
	select {
	case p.ch <- agent.Decision{OptionID: opt, Message: in.Text}:
	default:
	}
	// Agents that cannot carry a reason get it as the next message instead.
	if !c.Caps(conv).DenyMessage {
		c.mu.Lock()
		rt := c.runtimes[conv.Key()]
		rt.queue = append([]Input{in}, rt.queue...)
		c.mu.Unlock()
	}
}

func hasImage(bs []agent.Block) bool {
	return slices.ContainsFunc(bs, func(b agent.Block) bool { return b.IsImage() })
}

func (c *Core) loop(conv Conv, rt *runtime, in Input) {
	for {
		c.turn(conv, rt, in)
		c.mu.Lock()
		if len(rt.queue) == 0 {
			rt.running = false
			rt.cancel = nil
			c.mu.Unlock()
			return
		}
		in = rt.queue[0]
		rt.queue = rt.queue[1:]
		rt.cancel = nil
		c.mu.Unlock()
	}
}

// Stop cancels the current run and drops the queue.
func (c *Core) Stop(conv Conv) (stopped bool, dropped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.runtimes[conv.Key()]
	if rt == nil || !rt.running {
		return false, 0
	}
	dropped = len(rt.queue)
	rt.queue = nil
	if rt.cancel != nil {
		rt.cancel()
	} else {
		rt.stopReq = true // the turn is starting; it cancels itself on registration
	}
	return true, dropped
}

func (c *Core) turn(conv Conv, rt *runtime, in Input) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.mu.Lock()
	rt.cancel = cancel
	if rt.stopReq {
		rt.stopReq = false
		cancel()
	}
	c.mu.Unlock()

	s := c.Settings(conv)
	user := strconv.FormatInt(in.User, 10)
	if limit := c.cfg.Budget.DailyUSD; limit > 0 {
		if today, _ := c.store.Spend(user); today >= limit {
			c.ui.Notice(conv, fmt.Sprintf("💸 Daily budget reached ($%.2f of $%.2f). It resets at midnight.", today, limit))
			return
		}
	}
	ag := c.agents[s.Agent]
	caps := ag.Caps()
	mode := agent.Mode(s.Mode)
	if mode == agent.ModePlan && !slices.Contains(caps.Modes, agent.ModePlan) {
		c.ui.Notice(conv, s.Agent+" has no plan mode; running in ask mode.")
		mode = agent.ModeAsk
	}

	if c.cfg.CheckpointsEnabled() && mode != agent.ModePlan {
		if commit, err := gitcp.Snapshot(ctx, s.Cwd); err == nil {
			c.update(conv, func(st *store.Conversation) {
				st.PushCheckpoint(store.Checkpoint{Cwd: s.Cwd, Commit: commit, Prompt: title(in.Text), At: time.Now()})
			})
		}
	}

	blocks := in.Blocks
	if !caps.Images && hasImage(blocks) {
		blocks = c.imagesToFiles(blocks)
	}

	start := time.Now()
	progress := c.ui.Progress(conv)
	h := &handler{core: c, conv: conv, progress: progress, settings: s, mode: mode, title: title(in.Text)}
	unregister := c.bridge.Register(conv.Key(), &bridge.Endpoint{
		Handler:  h,
		SendFile: func(ctx context.Context, path, caption string) error { return c.ui.SendFile(ctx, conv, path, caption) },
	})
	defer unregister()

	res, err := ag.Run(ctx, agent.Request{
		Conv:        conv.Key(),
		SessionID:   s.SessionID,
		Cwd:         s.Cwd,
		Model:       s.Model,
		Mode:        mode,
		Prompt:      blocks,
		AlwaysAllow: s.AlwaysAllow,
		MCP:         c.bridge.Server(conv.Key()),
	}, h)

	if res.FinalText != "" {
		h.Text(res.FinalText)
	}
	h.mu.Lock()
	known := h.sessionID
	h.mu.Unlock()
	if res.SessionID != "" && res.SessionID != known {
		h.Session(res.SessionID)
	}

	cost := res.CostUSD
	if cost == 0 && res.SessionCostUSD > 0 {
		prev := c.store.Get(conv.Key()).SessionCost
		cost = max(res.SessionCostUSD-prev, 0)
		c.update(conv, func(st *store.Conversation) { st.SessionCost = res.SessionCostUSD })
	}
	if cost > 0 {
		c.update(conv, func(st *store.Conversation) { st.TotalCost += cost })
		c.store.AddSpend(user, cost)
	}

	summary := "✓ " + time.Since(start).Round(time.Second).String()
	if cost > 0 {
		summary += fmt.Sprintf(" · $%.4f", cost)
	}
	if h.tools > 0 {
		summary += fmt.Sprintf(" · %d tool calls", h.tools)
	}
	summary += " · " + s.Agent

	switch {
	case ctx.Err() != nil:
		progress.Done("⏹ Stopped")
		return
	case err != nil:
		progress.Done("⚠️ " + err.Error())
		return
	}
	progress.Done(summary)

	if s.Voice && c.TTS != nil && h.lastText != "" {
		if text := tts.Speakable(h.lastText); text != "" {
			if ogg, err := c.TTS.Speak(context.Background(), text); err != nil {
				c.ui.Notice(conv, "🔇 Voice reply failed: "+err.Error())
			} else if err := c.ui.SendVoice(context.Background(), conv, ogg); err != nil {
				log.Printf("send voice: %v", err)
			}
		}
	}
}

// imagesToFiles saves images for agents that cannot take them inline and
// points the agent at the files instead.
func (c *Core) imagesToFiles(blocks []agent.Block) []agent.Block {
	dir := filepath.Join(c.cfg.Home, "uploads")
	os.MkdirAll(dir, 0o700)
	var out []agent.Block
	for i, b := range blocks {
		if !b.IsImage() {
			out = append(out, b)
			continue
		}
		ext := ".png"
		if strings.Contains(b.MimeType, "jpeg") {
			ext = ".jpg"
		}
		path := filepath.Join(dir, fmt.Sprintf("%s-%d%s", time.Now().Format("20060102-150405"), i, ext))
		if err := os.WriteFile(path, b.Image, 0o600); err == nil {
			out = append(out, agent.Block{Text: "[The user attached an image, saved at " + path + "]"})
		}
	}
	return out
}

func title(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 60 {
		text = text[:57] + "..."
	}
	if text == "" {
		text = "(attachment)"
	}
	return text
}

// ---------- approvals ----------

func (c *Core) pendingFor(conv Conv) *pending {
	for _, p := range c.approvals {
		if p.conv == conv {
			return p
		}
	}
	return nil
}

// Answer delivers the user's choice for approval id. It returns false if the
// request is no longer pending.
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

// ApprovalConv returns the conversation an approval belongs to.
func (c *Core) ApprovalConv(id string) (Conv, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.approvals[id]; p != nil {
		return p.conv, true
	}
	return Conv{}, false
}

func randomID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// handler connects one agent turn to the UI and the permission policy.
type handler struct {
	core     *Core
	conv     Conv
	progress Progress
	settings Settings
	mode     agent.Mode
	title    string

	mu        sync.Mutex
	sessionID string
	tools     int
	lastText  string
}

func (h *handler) Session(id string) {
	h.mu.Lock()
	h.sessionID = id
	h.mu.Unlock()
	s := h.settings
	h.core.update(h.conv, func(st *store.Conversation) {
		if st.SessionID != id {
			st.SessionCost = 0
		}
		st.SessionID = id
		st.RecordSession(store.SessionRecord{ID: id, Agent: s.Agent, Cwd: s.Cwd, Title: h.title, Updated: time.Now()})
	})
}

func (h *handler) Text(md string) {
	h.mu.Lock()
	h.lastText = md
	h.mu.Unlock()
	h.progress.Flush()
	h.core.ui.Reply(h.conv, md)
}

func (h *handler) Tool(title string, kind agent.Kind) {
	h.mu.Lock()
	h.tools++
	h.mu.Unlock()
	h.progress.Tool(title)
}

func firstAllow(opts []agent.Option) agent.Decision {
	for _, o := range opts {
		if o.Kind == agent.AllowOnce {
			return agent.Decision{OptionID: o.ID}
		}
	}
	for _, o := range opts {
		if o.Kind.Allows() {
			return agent.Decision{OptionID: o.ID}
		}
	}
	return agent.Decision{}
}

func (h *handler) autoAllowed(p agent.Permission) bool {
	switch {
	case h.mode == agent.ModeYolo:
		return true
	case p.Key != "" && slices.Contains(h.core.Settings(h.conv).AlwaysAllow, p.Key):
		return true
	case h.mode == agent.ModeEdits && slices.Contains([]agent.Kind{agent.KindEdit, agent.KindRead, agent.KindSearch, agent.KindThink}, p.Kind):
		return true
	}
	return slices.Contains(h.core.cfg.AutoAllow, string(p.Kind))
}

func (h *handler) Permission(ctx context.Context, p agent.Permission) agent.Decision {
	if len(p.Options) == 0 {
		p.Options = agent.DefaultOptions()
	}
	if h.autoAllowed(p) {
		return firstAllow(p.Options)
	}

	c := h.core
	id := randomID()
	pd := &pending{conv: h.conv, p: p, ch: make(chan agent.Decision, 1)}
	c.mu.Lock()
	c.approvals[id] = pd
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
	}()

	h.progress.Flush()
	view := c.ui.ShowApproval(h.conv, id, p)

	var d agent.Decision
	outcome := "⌛ Timed out"
	select {
	case d = <-pd.ch:
		outcome = "❌ Denied"
		if d.Message != "" {
			outcome = "❌ Denied: " + d.Message
		}
		for _, o := range p.Options {
			if o.ID == d.OptionID {
				if o.Kind.Allows() {
					outcome = "✅ " + o.Label
				}
				if o.Kind == agent.AllowAlways && p.Key != "" {
					c.update(h.conv, func(st *store.Conversation) {
						if !slices.Contains(st.AlwaysAllow, p.Key) {
							st.AlwaysAllow = append(st.AlwaysAllow, p.Key)
						}
					})
				}
			}
		}
	case <-ctx.Done():
		outcome = "⏹ Cancelled"
		d = agent.Decision{}
	case <-time.After(c.cfg.ApprovalTimeout.D()):
		d = agent.Decision{}
	}
	if d.OptionID == "" {
		// Timed out or cancelled: pick a reject option so the agent moves on.
		for _, o := range p.Options {
			if !o.Kind.Allows() {
				d.OptionID = o.ID
				break
			}
		}
	}
	view.Resolve(outcome)
	return d
}
