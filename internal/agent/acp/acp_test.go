package acp

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
)

type recorder struct {
	mu       sync.Mutex
	decide   string // option id to choose
	sessions []string
	messages []string
	tools    []string
	asked    []agent.Permission
}

func (r *recorder) Session(id string) {
	r.mu.Lock()
	r.sessions = append(r.sessions, id)
	r.mu.Unlock()
}
func (r *recorder) Message(s string) { r.mu.Lock(); r.messages = append(r.messages, s); r.mu.Unlock() }
func (r *recorder) ToolCall(t string, k agent.Kind) {
	r.mu.Lock()
	r.tools = append(r.tools, string(k)+" "+t)
	r.mu.Unlock()
}
func (r *recorder) SendFile(context.Context, string, string) error { return nil }
func (r *recorder) Permission(_ context.Context, p agent.Permission) agent.Decision {
	r.mu.Lock()
	r.asked = append(r.asked, p)
	r.mu.Unlock()
	return agent.Decision{OptionID: r.decide}
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.messages, "|")
}

func newFake(t *testing.T) *Agent {
	t.Helper()
	a, err := New(agent.Spec{Name: "fake", Command: []string{os.Args[0]}, Env: []string{"POCKETAGENT_FAKE_ACP=1"}, Options: agent.NoOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.(io.Closer).Close() })
	return a.(*Agent)
}

func run(t *testing.T, a *Agent, req agent.Request, h *recorder) agent.Result {
	t.Helper()
	if req.Conv == "" {
		req.Conv = "c"
	}
	if req.Cwd == "" {
		req.Cwd = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := a.Run(ctx, req, h)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func prompt(s string) []agent.Block { return []agent.Block{{Text: s}} }

func TestChunksAreJoinedAndCostIsPerTurn(t *testing.T) {
	a := newFake(t)
	dir := t.TempDir()
	h := &recorder{}
	res := run(t, a, agent.Request{Cwd: dir, Prompt: prompt("hello")}, h)
	if got := h.text(); got != "Hello [mode=default model=m1]" {
		t.Errorf("message = %q", got)
	}
	if len(h.sessions) != 1 || res.SessionID != h.sessions[0] || res.StopReason != "end_turn" {
		t.Errorf("session %v, result %+v", h.sessions, res)
	}
	if res.CostUSD < 0.0099 || res.CostUSD > 0.0101 {
		t.Errorf("first turn cost = %v", res.CostUSD)
	}

	// Same session, same process: cost is the turn's share of the running total.
	h2 := &recorder{}
	res2 := run(t, a, agent.Request{Cwd: dir, SessionID: res.SessionID, Prompt: prompt("hello")}, h2)
	if res2.SessionID != res.SessionID || len(h2.sessions) != 0 {
		t.Errorf("session changed: %s -> %s", res.SessionID, res2.SessionID)
	}
	if res2.CostUSD < 0.0099 || res2.CostUSD > 0.0101 {
		t.Errorf("second turn cost = %v (should not include the first)", res2.CostUSD)
	}
}

func TestPermissionRoundTrip(t *testing.T) {
	a := newFake(t)
	for _, choice := range []string{"yes", "no"} {
		h := &recorder{decide: choice}
		run(t, a, agent.Request{Prompt: prompt("tool")}, h)
		if len(h.asked) != 1 {
			t.Fatalf("asked %d times", len(h.asked))
		}
		p := h.asked[0]
		if p.Kind != agent.KindExecute || p.Detail != "make test" || p.Tool != "Run make test" || len(p.Options) != 2 {
			t.Errorf("permission = %+v", p)
		}
		if !strings.Contains(h.text(), "chose "+choice) {
			t.Errorf("agent saw %q, want chose %s", h.text(), choice)
		}
		if len(h.tools) != 1 || h.tools[0] != "execute Run make test" {
			t.Errorf("tools = %v", h.tools)
		}
	}
}

// An empty decision (timeout or cancel) reaches the agent as "cancelled".
func TestPermissionCancelled(t *testing.T) {
	h := &recorder{decide: ""}
	run(t, newFake(t), agent.Request{Prompt: prompt("tool")}, h)
	if !strings.Contains(h.text(), "cancelled") {
		t.Errorf("agent saw %q", h.text())
	}
}

func TestModeAndModel(t *testing.T) {
	a := newFake(t)
	h := &recorder{}
	run(t, a, agent.Request{Mode: agent.ModePlan, Model: "Model Two", Prompt: prompt("hello")}, h)
	if !strings.Contains(h.text(), "[mode=plan model=m2]") {
		t.Errorf("mode/model not applied: %q", h.text())
	}
	if got := strings.Join(a.Models("c"), ","); got != "m1,m2" {
		t.Errorf("models = %s", got)
	}

	h = &recorder{}
	run(t, a, agent.Request{Model: "nope", Mode: agent.ModeFull, Prompt: prompt("hello")}, h)
	if !strings.Contains(h.text(), "isn't offered") || !strings.Contains(h.text(), "mode=bypassPermissions") {
		t.Errorf("got %q", h.text())
	}
}

func TestCustomModeIDs(t *testing.T) {
	a, err := New(agent.Spec{Name: "fake", Command: []string{os.Args[0]}, Env: []string{"POCKETAGENT_FAKE_ACP=1"},
		Options: config.OptionsFrom(map[string]any{"modes": map[string][]string{"edits": {"plan"}}})})
	if err != nil {
		t.Fatal(err)
	}
	defer a.(io.Closer).Close()
	h := &recorder{}
	run(t, a.(*Agent), agent.Request{Mode: agent.ModeEdits, Prompt: prompt("hello")}, h)
	if !strings.Contains(h.text(), "mode=plan") {
		t.Errorf("custom mapping ignored: %q", h.text())
	}
}

// A new process loads the previous session without resending its history.
func TestLoadSessionSuppressesReplay(t *testing.T) {
	dir := t.TempDir()
	first := newFake(t)
	res := run(t, first, agent.Request{Cwd: dir, Prompt: prompt("hello")}, &recorder{})
	first.Close()

	h := &recorder{}
	second := newFake(t)
	run(t, second, agent.Request{Cwd: dir, SessionID: res.SessionID, Prompt: prompt("hello")}, h)
	if strings.Contains(h.text(), "OLD HISTORY") {
		t.Errorf("history was replayed to the user: %q", h.text())
	}
	if len(h.sessions) != 0 {
		t.Errorf("a new session was created: %v", h.sessions)
	}
}

func TestUnknownSessionStartsFresh(t *testing.T) {
	h := &recorder{}
	res := run(t, newFake(t), agent.Request{SessionID: "missing-1", Prompt: prompt("hello")}, h)
	if res.SessionID == "missing-1" || !strings.Contains(h.text(), "starting a new one") {
		t.Errorf("session %s, messages %q", res.SessionID, h.text())
	}
}

func TestCancel(t *testing.T) {
	a := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := a.Run(ctx, agent.Request{Conv: "c", Cwd: t.TempDir(), Prompt: prompt("slow")}, &recorder{})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancel: err %v after %v", err, time.Since(start))
	}
	// The process survives a cancelled turn and serves the next one.
	h := &recorder{}
	run(t, a, agent.Request{Prompt: prompt("hello")}, h)
	if !strings.Contains(h.text(), "Hello") {
		t.Errorf("after cancel: %q", h.text())
	}
}

func TestImagesAndToolServer(t *testing.T) {
	h := &recorder{}
	res := run(t, newFake(t), agent.Request{
		Prompt: []agent.Block{{Image: []byte("png"), MimeType: "image/png"}, {Text: "image"}},
		Tools:  &agent.ToolServer{Name: "pa", URL: "http://127.0.0.1:1/mcp/fake", Headers: map[string]string{"A": "b"}},
	}, h)
	if !strings.Contains(h.text(), "1 images") {
		t.Errorf("image not passed: %q", h.text())
	}
	if !strings.HasSuffix(res.SessionID, "-mcp1") {
		t.Errorf("tool server not offered: session %s", res.SessionID)
	}
}

// A new working directory needs a new process (agents are bound to a cwd).
func TestCwdChangeRestartsProcess(t *testing.T) {
	a := newFake(t)
	run(t, a, agent.Request{Cwd: t.TempDir(), Prompt: prompt("hello")}, &recorder{})
	first := a.procs["c"]
	run(t, a, agent.Request{Cwd: t.TempDir(), Prompt: prompt("hello")}, &recorder{})
	if a.procs["c"] == first {
		t.Error("process reused across directories")
	}
}

func TestStartFailure(t *testing.T) {
	a, _ := New(agent.Spec{Name: "missing", Command: []string{"/nonexistent/agent"}, Options: agent.NoOptions{}})
	defer a.(io.Closer).Close()
	_, err := a.Run(context.Background(), agent.Request{Conv: "c", Cwd: t.TempDir(), Prompt: prompt("x")}, &recorder{})
	if err == nil || !strings.Contains(err.Error(), "start") {
		t.Errorf("err = %v", err)
	}
}

func TestRejectsUnknownOptions(t *testing.T) {
	if _, err := New(agent.Spec{Command: []string{"x"}, Options: config.OptionsFrom(map[string]any{"bogus": true})}); err == nil {
		t.Error("unknown option accepted")
	}
}

func TestListSessions(t *testing.T) {
	a := newFake(t)
	dir := t.TempDir()
	res := run(t, a, agent.Request{Cwd: dir, Prompt: prompt("hello")}, &recorder{})
	ss, err := a.Sessions(context.Background(), "c", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != res.SessionID || ss[1].ID != "terminal-1" || ss[1].Title != "started in a terminal" {
		t.Fatalf("sessions = %+v", ss)
	}
	// A session started elsewhere can be continued here (session/load).
	h := &recorder{}
	run(t, a, agent.Request{Cwd: dir, SessionID: "terminal-1", Prompt: prompt("hello")}, h)
	if len(h.sessions) != 0 {
		t.Errorf("started a new session instead of loading: %v", h.sessions)
	}
}
