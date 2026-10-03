package core

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// runtime is a conversation's in-memory run state.
type runtime struct {
	running bool
	cancel  context.CancelFunc // the current turn's; nil between turns
	stopReq bool               // /stop arrived before the turn registered cancel
	queue   []Input
}

// Running reports whether the conversation is busy and how much is queued.
func (c *Core) Running(conv ConvID) (running bool, queued int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rt := c.runtimes[conv]; rt != nil {
		return rt.running, len(rt.queue)
	}
	return false, 0
}

// Submit runs the input now, or queues it if the conversation is busy.
// While an approval is pending, text input denies it with that text as the reason.
func (c *Core) Submit(conv ConvID, in Input) {
	c.mu.Lock()
	rt := c.runtimes[conv]
	if rt == nil {
		rt = &runtime{}
		c.runtimes[conv] = rt
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

// Stop cancels the current run and drops the queue.
func (c *Core) Stop(conv ConvID) (stopped bool, dropped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rt := c.runtimes[conv]
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

func (c *Core) loop(conv ConvID, rt *runtime, in Input) {
	for {
		c.turn(conv, rt, in)
		c.mu.Lock()
		if len(rt.queue) == 0 {
			rt.running, rt.cancel = false, nil
			c.mu.Unlock()
			return
		}
		in, rt.queue, rt.cancel = rt.queue[0], rt.queue[1:], nil
		c.mu.Unlock()
	}
}

func (c *Core) turn(conv ConvID, rt *runtime, in Input) {
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
	if limit := c.Config.Budget.DailyUSD; limit > 0 {
		if today, _ := c.Store.Spend(in.User); today >= limit {
			c.ui.Notice(conv, fmt.Sprintf("💸 Daily budget reached ($%.2f of $%.2f). It resets at midnight.", today, limit))
			return
		}
	}
	ag := c.Agents[s.Agent]
	caps := ag.Caps()
	mode := s.Mode
	if !caps.SupportsMode(mode) {
		c.ui.Notice(conv, fmt.Sprintf("%s can't enforce %s mode; its own permission settings apply.", s.Agent, mode))
	}

	if c.Checkpointer != nil && mode != agent.ModePlan {
		if id, err := c.Checkpointer.Snapshot(ctx, s.Cwd); err == nil {
			c.update(conv, func(st *store.Conversation) {
				st.PushCheckpoint(store.Checkpoint{Cwd: s.Cwd, Commit: id, Prompt: title(in.Text), At: time.Now()})
			})
		}
	}

	blocks := in.Blocks
	if !caps.Images && hasImage(blocks) {
		blocks = c.imagesToFiles(blocks)
	}

	start := time.Now()
	progress := c.ui.StartProgress(conv)
	h := &turnHandler{core: c, conv: conv, progress: progress, settings: s, title: title(in.Text)}
	detach := c.Tools.Attach(string(conv), h)
	defer detach()

	res, err := ag.Run(ctx, agent.Request{
		Conv:        string(conv),
		SessionID:   s.SessionID,
		Cwd:         s.Cwd,
		Model:       s.Model,
		Mode:        mode,
		Prompt:      blocks,
		AlwaysAllow: s.AlwaysAllow,
		Tools:       c.Tools.Server(s.Agent, string(conv)),
	}, h)

	if res.Final != "" {
		h.Message(res.Final)
	}
	if res.SessionID != "" && res.SessionID != h.session() {
		h.Session(res.SessionID)
	}
	if res.CostUSD > 0 {
		c.update(conv, func(st *store.Conversation) { st.TotalCost += res.CostUSD })
		if err := c.Store.AddSpend(in.User, res.CostUSD); err != nil {
			log.Printf("record spend: %v", err)
		}
	}

	switch {
	case ctx.Err() != nil:
		progress.Done("⏹ Stopped")
		return
	case err != nil:
		progress.Done("⚠️ " + err.Error())
		return
	}
	progress.Done(summary(time.Since(start), res.CostUSD, h.toolCount(), s.Agent))
	c.speak(conv, s, h.last())
}

func summary(d time.Duration, cost float64, tools int, agentName string) string {
	s := "✓ " + d.Round(time.Second).String()
	if cost > 0 {
		s += fmt.Sprintf(" · $%.4f", cost)
	}
	if tools > 0 {
		s += fmt.Sprintf(" · %d tool calls", tools)
	}
	return s + " · " + agentName
}

// speak sends the last reply as a voice message when voice replies are on.
func (c *Core) speak(conv ConvID, s Settings, text string) {
	if !s.Voice || c.Speaker == nil {
		return
	}
	if text = tts.Speakable(text); text == "" {
		return
	}
	ogg, err := c.Speaker.Speak(context.Background(), text)
	if err != nil {
		c.ui.Notice(conv, "🔇 Voice reply failed: "+err.Error())
		return
	}
	if err := c.ui.SendVoice(context.Background(), conv, ogg); err != nil {
		log.Printf("send voice: %v", err)
	}
}

// imagesToFiles saves images for agents that can't take them inline and
// points the agent at the files instead.
func (c *Core) imagesToFiles(blocks []agent.Block) []agent.Block {
	dir := filepath.Join(c.Config.Home, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("uploads dir: %v", err)
	}
	out := make([]agent.Block, 0, len(blocks))
	for i, b := range blocks {
		if !b.IsImage() {
			out = append(out, b)
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("%s-%d%s", time.Now().Format("20060102-150405"), i, imageExt(b.MimeType)))
		if err := os.WriteFile(path, b.Image, 0o600); err == nil {
			out = append(out, agent.Block{Text: "[The user attached an image, saved at " + path + "]"})
		}
	}
	return out
}

func imageExt(mime string) string {
	if ext := map[string]string{"image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mime]; ext != "" {
		return ext
	}
	return ".png"
}

func hasImage(bs []agent.Block) bool { return slices.ContainsFunc(bs, agent.Block.IsImage) }

func title(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 60 {
		text = text[:57] + "..."
	}
	if text == "" {
		return "(attachment)"
	}
	return text
}

// turnHandler is the agent.Handler for one turn.
type turnHandler struct {
	core     *Core
	conv     ConvID
	progress Progress
	settings Settings
	title    string

	mu        sync.Mutex
	sessionID string
	tools     int
	lastText  string
}

var _ agent.Handler = (*turnHandler)(nil)

func (h *turnHandler) session() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessionID
}

func (h *turnHandler) toolCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tools
}

func (h *turnHandler) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastText
}

func (h *turnHandler) Session(id string) {
	h.mu.Lock()
	h.sessionID = id
	h.mu.Unlock()
	s := h.settings
	h.core.update(h.conv, func(st *store.Conversation) {
		st.SessionID = id
		st.RecordSession(store.SessionRecord{ID: id, Agent: s.Agent, Cwd: s.Cwd, Title: h.title, Updated: time.Now()})
	})
}

func (h *turnHandler) Message(md string) {
	h.mu.Lock()
	h.lastText = md
	h.mu.Unlock()
	h.progress.Flush()
	h.core.ui.Reply(h.conv, md)
}

func (h *turnHandler) ToolCall(title string, _ agent.Kind) {
	h.mu.Lock()
	h.tools++
	h.mu.Unlock()
	h.progress.ToolCall(title)
}

func (h *turnHandler) Permission(ctx context.Context, p agent.Permission) agent.Decision {
	return h.core.ask(ctx, h.conv, h.settings.Mode, h.progress, p)
}

func (h *turnHandler) SendFile(ctx context.Context, path, caption string) error {
	return h.core.ui.SendFile(ctx, h.conv, path, caption)
}
