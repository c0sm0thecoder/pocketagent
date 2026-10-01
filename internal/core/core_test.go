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
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

// fakeAgent asks for one permission per turn if perm is set, then echoes.
type fakeAgent struct {
	perm    *agent.Permission
	block   chan struct{} // if set, Run waits on it
	caps    agent.Caps
	mu      sync.Mutex
	prompts []string
	got     []agent.Decision
	cost    float64
}

func (f *fakeAgent) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prompts)
}

func (f *fakeAgent) Caps() agent.Caps       { return f.caps }
func (f *fakeAgent) Models(string) []string { return []string{"m1"} }
func (f *fakeAgent) Close()                 {}
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
	h.Text("echo: " + req.Prompt[0].Text)
	return agent.Result{SessionID: "s1", CostUSD: f.cost}, nil
}

type fakeUI struct {
	mu        sync.Mutex
	replies   []string
	notices   []string
	approvals chan string
	done      chan string
}

func newFakeUI() *fakeUI {
	return &fakeUI{approvals: make(chan string, 10), done: make(chan string, 10)}
}
func (u *fakeUI) Reply(_ Conv, md string) {
	u.mu.Lock()
	u.replies = append(u.replies, md)
	u.mu.Unlock()
}
func (u *fakeUI) Notice(_ Conv, t string) {
	u.mu.Lock()
	u.notices = append(u.notices, t)
	u.mu.Unlock()
}
func (u *fakeUI) Progress(Conv) Progress { return fakeProgress{u} }
func (u *fakeUI) ShowApproval(_ Conv, id string, _ agent.Permission) ApprovalView {
	u.approvals <- id
	return fakeView{}
}
func (u *fakeUI) SendFile(context.Context, Conv, string, string) error { return nil }
func (u *fakeUI) SendVoice(context.Context, Conv, []byte) error        { return nil }

type fakeProgress struct{ u *fakeUI }

func (fakeProgress) Tool(string)     {}
func (fakeProgress) Flush()          {}
func (p fakeProgress) Done(s string) { p.u.done <- s }

type fakeView struct{}

func (fakeView) Resolve(string) {}

func setup(t *testing.T, fa *fakeAgent, mutate func(*config.Config)) (*Core, *fakeUI) {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		Home:            home,
		Defaults:        config.Defaults{Agent: "fake", Cwd: home, Mode: "ask"},
		AutoAllow:       []string{"read"},
		ApprovalTimeout: config.Duration(5 * time.Second),
		Agents:          map[string]config.Agent{"fake": {Type: "command"}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	st, err := store.Open(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	br, err := bridge.Start("")
	if err != nil {
		t.Fatal(err)
	}
	c := New(cfg, st, map[string]agent.Agent{"fake": fa}, br, nil, nil)
	ui := newFakeUI()
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

var conv = Conv{ChatID: 1}

func text(s string) Input { return Input{User: 7, Blocks: []agent.Block{{Text: s}}, Text: s} }

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
	stopped, dropped := c.Stop(conv)
	if !stopped || dropped != 1 {
		t.Fatalf("stop = %v, %d", stopped, dropped)
	}
	if s := waitDone(t, ui); !strings.Contains(s, "Stopped") {
		t.Errorf("summary = %q", s)
	}
	if fa.count() != 1 {
		t.Errorf("queued prompt ran after stop: %v", fa.prompts)
	}
}

func TestPermissionPolicy(t *testing.T) {
	cases := []struct {
		mode  string
		kind  agent.Kind
		asked bool
	}{
		{"ask", agent.KindRead, false}, // auto_allow
		{"ask", agent.KindEdit, true},
		{"ask", agent.KindExecute, true},
		{"edits", agent.KindEdit, false},
		{"edits", agent.KindExecute, true},
		{"yolo", agent.KindExecute, false},
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
		case <-ui.done:
			if len(fa.got) != 1 || fa.got[0].OptionID != "allow" {
				t.Errorf("%s/%s: auto decision = %+v", tc.mode, tc.kind, fa.got)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s/%s: stuck", tc.mode, tc.kind)
		}
		if asked != tc.asked {
			t.Errorf("%s/%s: asked = %v, want %v", tc.mode, tc.kind, asked, tc.asked)
		}
		if asked {
			waitDone(t, ui)
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
	if len(fa.prompts) != 1 {
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
	// The follow-up turn asks again; deny it.
	c.Answer(<-ui.approvals, "deny")
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
		t.Errorf("ran %d turns, want 2", len(fa.prompts))
	}
	if today, _ := c.Usage(7); today < 1.19 || today > 1.21 {
		t.Errorf("spend today = %v", today)
	}
	ui.mu.Lock()
	last := ui.notices[len(ui.notices)-1]
	ui.mu.Unlock()
	if n := last; !strings.Contains(n, "budget") {
		t.Errorf("last notice = %q", n)
	}
}

func TestSettingsPrecedence(t *testing.T) {
	fa := &fakeAgent{}
	c, _ := setup(t, fa, func(cfg *config.Config) {
		cfg.Projects = map[string]config.Project{"p": {Cwd: "/proj", Model: "pm", Mode: "edits"}}
	})
	if err := c.SetProject(conv, "p"); err != nil {
		t.Fatal(err)
	}
	s := c.Settings(conv)
	if s.Cwd != "/proj" || s.Model != "pm" || s.Mode != "edits" {
		t.Errorf("project not applied: %+v", s)
	}
	c.SetModel(conv, "override")
	if s := c.Settings(conv); s.Model != "override" {
		t.Errorf("conversation override lost: %+v", s)
	}
}

func TestUndoRestoresLastTurn(t *testing.T) {
	fa := &fakeAgent{}
	c, ui := setup(t, fa, nil)
	dir := c.Settings(conv).Cwd
	gitRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitRun("init", "-q")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("before\n"), 0o644)
	gitRun("add", ".")
	gitRun("commit", "-qm", "init")

	// The agent edits the file during its turn.
	fa.block = make(chan struct{})
	c.Submit(conv, text("change it"))
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("after\n"), 0o644)
	close(fa.block)
	waitDone(t, ui)

	d, err := c.Diff(context.Background(), conv, false)
	if err != nil || !strings.Contains(d, "+after") {
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
