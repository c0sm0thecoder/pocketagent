package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/gitcp"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// fakeAgent records prompts, optionally blocks, and asks for perm if set.
type fakeAgent struct {
	perm  *agent.Permission
	block chan struct{} // if set, Run waits on it
	caps  agent.Caps
	cost  float64

	mu      sync.Mutex
	prompts []string
	got     []agent.Decision
}

func (f *fakeAgent) Caps() agent.Caps       { return f.caps }
func (f *fakeAgent) Models(string) []string { return []string{"reported"} }

func (f *fakeAgent) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

func (f *fakeAgent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, req.Prompt[0].Text)
	f.mu.Unlock()
	h.Session("s1")
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	if f.perm != nil {
		d := h.Permission(ctx, *f.perm)
		f.mu.Lock()
		f.got = append(f.got, d)
		f.mu.Unlock()
	}
	h.Message("echo: " + req.Prompt[0].Text)
	return agent.Result{SessionID: "s1", CostUSD: f.cost}, nil
}

type fakeUI struct {
	mu        sync.Mutex
	notices   []string
	approvals chan string
	done      chan string
}

func (u *fakeUI) Reply(ConvID, string) {}
func (u *fakeUI) Notice(_ ConvID, t string) {
	u.mu.Lock()
	u.notices = append(u.notices, t)
	u.mu.Unlock()
}
func (u *fakeUI) lastNotice() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.notices) == 0 {
		return ""
	}
	return u.notices[len(u.notices)-1]
}
func (u *fakeUI) StartProgress(ConvID) Progress { return fakeProgress{u} }
func (u *fakeUI) AskApproval(_ ConvID, id string, _ agent.Permission) Approval {
	u.approvals <- id
	return fakeApproval{}
}
func (u *fakeUI) SendFile(context.Context, ConvID, string, string) error { return nil }
func (u *fakeUI) SendVoice(context.Context, ConvID, []byte) error        { return nil }

type fakeProgress struct{ u *fakeUI }

func (fakeProgress) ToolCall(string) {}
func (fakeProgress) Flush()          {}
func (p fakeProgress) Done(s string) { p.u.done <- s }

type fakeApproval struct{}

func (fakeApproval) Resolve(string) {}

// fakeTools is a ToolHost that serves nothing.
type fakeTools struct{}

func (fakeTools) Attach(string, agent.Handler) func()     { return func() {} }
func (fakeTools) Server(string, string) *agent.ToolServer { return nil }

func setup(t *testing.T, fa *fakeAgent, mutate func(*config.Config)) (*Core, *fakeUI) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		Home:        home,
		Defaults:    config.Defaults{Agent: "fake", Cwd: home, Mode: agent.ModeAsk},
		Permissions: config.Permissions{AutoAllow: []agent.Kind{agent.KindRead}, Timeout: config.Duration(5 * time.Second)},
		Agents:      map[string]config.Agent{"fake": {Type: "fake", Models: []string{"configured"}}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	st, err := store.Open(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := New(Deps{
		Config: cfg, Agents: map[string]agent.Agent{"fake": fa}, Store: st,
		Tools: fakeTools{}, Checkpointer: gitcp.Git{},
	})
	ui := &fakeUI{approvals: make(chan string, 10), done: make(chan string, 10)}
	c.SetUI(ui)
	return c, ui
}

func waitDone(t *testing.T, ui *fakeUI) string {
	t.Helper()
	select {
	case s := <-ui.done:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish")
		return ""
	}
}

const conv ConvID = "c1"

func text(s string) Input { return Input{User: "u7", Blocks: []agent.Block{{Text: s}}, Text: s} }

func TestQueueRunsInOrder(t *testing.T) {
	fa := &fakeAgent{block: make(chan struct{})}
	c, ui := setup(t, fa, nil)
	c.Submit(conv, text("first"))
	time.Sleep(50 * time.Millisecond)
	c.Submit(conv, text("second"))
	c.Submit(conv, text("third"))
	if _, q := c.Running(conv); q != 2 {
		t.Fatalf("queued = %d, want 2", q)
	}
	close(fa.block)
	for range 3 {
		waitDone(t, ui)
	}
	if got := strings.Join(fa.prompts, ","); got != "first,second,third" {
		t.Errorf("order = %s", got)
	}
}

func TestStopDropsQueue(t *testing.T) {
	fa := &fakeAgent{block: make(chan struct{})}
	c, ui := setup(t, fa, nil)
	c.Submit(conv, text("a"))
	time.Sleep(50 * time.Millisecond)
	c.Submit(conv, text("b"))
	if stopped, dropped := c.Stop(conv); !stopped || dropped != 1 {
		t.Fatalf("stop = %v, %d", stopped, dropped)
	}
	if s := waitDone(t, ui); !strings.Contains(s, "Stopped") {
		t.Errorf("summary = %q", s)
	}
	if fa.count() != 1 {
		t.Errorf("queued prompt ran after stop: %v", fa.prompts)
	}
}

// /stop right after a message is accepted must still cancel the run.
func TestStopImmediatelyAfterSubmit(t *testing.T) {
	for range 50 {
		fa := &fakeAgent{block: make(chan struct{})} // never released
		c, ui := setup(t, fa, nil)
		c.Submit(conv, text("a"))
		if stopped, _ := c.Stop(conv); !stopped {
			t.Fatal("not stopped")
		}
		if s := waitDone(t, ui); !strings.Contains(s, "Stopped") {
			t.Fatalf("summary = %q", s)
		}
	}
}

func TestPermissionFlow(t *testing.T) {
	cases := []struct {
		mode  agent.Mode
		kind  agent.Kind
		asked bool
	}{
		{agent.ModeAsk, agent.KindRead, false}, // auto_allow
		{agent.ModeAsk, agent.KindExecute, true},
		{agent.ModeEdits, agent.KindEdit, false},
		{agent.ModeFull, agent.KindExecute, false},
	}
	for _, tc := range cases {
		fa := &fakeAgent{perm: &agent.Permission{Tool: "T", Kind: tc.kind}}
		c, ui := setup(t, fa, func(cfg *config.Config) { cfg.Defaults.Mode = tc.mode })
		c.Submit(conv, text("go"))
		asked := false
		select {
		case id := <-ui.approvals:
			asked = true
			c.Answer(id, "allow")
			waitDone(t, ui)
		case <-ui.done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s/%s: stuck", tc.mode, tc.kind)
		}
		if asked != tc.asked {
			t.Errorf("%s/%s: asked = %v, want %v", tc.mode, tc.kind, asked, tc.asked)
		}
		if fa.got[0].OptionID != "allow" {
			t.Errorf("%s/%s: decision = %+v", tc.mode, tc.kind, fa.got)
		}
	}
}

func TestAlwaysAllowIsRemembered(t *testing.T) {
	fa := &fakeAgent{perm: &agent.Permission{Tool: "Bash", Key: "Bash", Kind: agent.KindExecute}}
	c, ui := setup(t, fa, nil)
	c.Submit(conv, text("one"))
	c.Answer(<-ui.approvals, "always")
	waitDone(t, ui)
	c.Submit(conv, text("two"))
	select {
	case <-ui.approvals:
		t.Fatal("asked again after always allow")
	case <-ui.done:
	}
	c.NewSession(conv)
	c.Submit(conv, text("three"))
	select {
	case id := <-ui.approvals:
		c.Answer(id, "deny")
		waitDone(t, ui)
	case <-ui.done:
		t.Fatal("always allow survived /new")
	}
}

func TestTextReplyDeniesWithReason(t *testing.T) {
	fa := &fakeAgent{perm: &agent.Permission{Tool: "Bash", Kind: agent.KindExecute}, caps: agent.Caps{DenyMessage: true}}
	c, ui := setup(t, fa, nil)
	c.Submit(conv, text("do it"))
	<-ui.approvals
	c.Submit(conv, text("use make instead"))
	waitDone(t, ui)
	if len(fa.got) != 1 || fa.got[0].OptionID != "deny" || fa.got[0].Message != "use make instead" {
		t.Errorf("decision = %+v", fa.got)
	}
	if fa.count() != 1 {
		t.Errorf("reason should not be queued for agents that carry it: %v", fa.prompts)
	}
}

func TestTextReplyQueuedWhenAgentCannotCarryReason(t *testing.T) {
	fa := &fakeAgent{perm: &agent.Permission{Tool: "Bash", Kind: agent.KindExecute}}
	c, ui := setup(t, fa, nil)
	c.Submit(conv, text("do it"))
	<-ui.approvals
	c.Submit(conv, text("use make instead"))
	waitDone(t, ui)
	c.Answer(<-ui.approvals, "deny") // the follow-up turn asks again
	waitDone(t, ui)
	if len(fa.prompts) != 2 || fa.prompts[1] != "use make instead" {
		t.Errorf("prompts = %v", fa.prompts)
	}
}

func TestBudget(t *testing.T) {
	fa := &fakeAgent{cost: 0.6}
	c, ui := setup(t, fa, func(cfg *config.Config) { cfg.Budget.DailyUSD = 1 })
	c.Submit(conv, text("a"))
	waitDone(t, ui)
	c.Submit(conv, text("b"))
	waitDone(t, ui)
	c.Submit(conv, text("c")) // over budget: refused
	time.Sleep(100 * time.Millisecond)
	if fa.count() != 2 {
		t.Errorf("ran %d turns, want 2", fa.count())
	}
	if today, _ := c.Usage("u7"); today < 1.19 || today > 1.21 {
		t.Errorf("spend today = %v", today)
	}
	if !strings.Contains(c.ui.(*fakeUI).lastNotice(), "budget") {
		t.Errorf("last notice = %q", ui.lastNotice())
	}
}

func TestSettingsPrecedence(t *testing.T) {
	c, _ := setup(t, &fakeAgent{}, func(cfg *config.Config) {
		cfg.Projects = map[string]config.Project{"p": {Cwd: "/proj", Model: "pm", Mode: agent.ModeEdits}}
	})
	if err := c.SetProject(conv, "p"); err != nil {
		t.Fatal(err)
	}
	if s := c.Settings(conv); s.Cwd != "/proj" || s.Model != "pm" || s.Mode != agent.ModeEdits {
		t.Errorf("project not applied: %+v", s)
	}
	c.SetModel(conv, "override")
	if s := c.Settings(conv); s.Model != "override" {
		t.Errorf("conversation override lost: %+v", s)
	}
}

// The model picker merges configured models with those the agent reports.
func TestModelsMerge(t *testing.T) {
	c, _ := setup(t, &fakeAgent{}, nil)
	if got := strings.Join(c.Models(conv), ","); got != "configured,reported" {
		t.Errorf("models = %s", got)
	}
}

func TestUndoRestoresLastTurn(t *testing.T) {
	fa := &fakeAgent{block: make(chan struct{})}
	c, ui := setup(t, fa, nil)
	dir := c.Settings(conv).Cwd
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("before\n"), 0o644)
	git("add", ".")
	git("commit", "-qm", "init")

	c.Submit(conv, text("change it"))
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("after\n"), 0o644) // the agent's edit
	close(fa.block)
	waitDone(t, ui)

	if d, err := c.Diff(context.Background(), conv, false); err != nil || !strings.Contains(d, "+after") {
		t.Fatalf("diff = %q, %v", d, err)
	}
	cp, err := c.Undo(context.Background(), conv)
	if err != nil || cp.Prompt != "change it" {
		t.Fatalf("undo: %+v %v", cp, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(b) != "before\n" {
		t.Errorf("file = %q", b)
	}
	if _, err := c.Undo(context.Background(), conv); err == nil {
		t.Error("second undo should have nothing to undo")
	}
}
