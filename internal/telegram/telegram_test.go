package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/bridge"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// call is one Bot API request the frontend made.
type call struct {
	method string
	params map[string]string
}

// fakeAPI is a minimal Telegram Bot API that records calls.
type fakeAPI struct {
	mu    sync.Mutex
	calls []call
	next  int
	ch    chan call
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	params := map[string]string{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
		for k, v := range r.MultipartForm.Value {
			params[k] = v[0]
		}
		for k := range r.MultipartForm.File {
			params[k] = "<file>"
		}
	} else {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for k, v := range body {
			if s, ok := v.(string); ok {
				params[k] = s
			} else {
				b, _ := json.Marshal(v)
				params[k] = string(b)
			}
		}
	}
	f.mu.Lock()
	f.next++
	id := f.next
	c := call{method, params}
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if method != "sendChatAction" {
		f.ch <- c
	}
	var result any = true
	if strings.HasPrefix(method, "send") || method == "editMessageText" {
		result = map[string]any{"message_id": id, "date": 0, "chat": map[string]any{"id": 1, "type": "private"}}
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// next waits for the next call with the given method.
func (f *fakeAPI) wait(t *testing.T, method string) call {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case c := <-f.ch:
			if c.method == method {
				return c
			}
		case <-timeout:
			t.Fatalf("no %s call", method)
		}
	}
}

// find waits for a call with the given method whose text contains substr.
func (f *fakeAPI) find(t *testing.T, method, substr string) call {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case c := <-f.ch:
			if c.method == method && strings.Contains(c.params["text"], substr) {
				return c
			}
		case <-timeout:
			t.Fatalf("no %s call containing %q", method, substr)
		}
	}
}

// scripted is an agent whose behaviour each test sets.
type scripted struct {
	mu     sync.Mutex
	run    func(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error)
	models []string
}

func (s *scripted) Caps() agent.Caps {
	return agent.Caps{Images: true, Modes: []agent.Mode{agent.ModeAsk, agent.ModeYolo}}
}
func (s *scripted) Models(string) []string { return s.models }
func (s *scripted) Close()                 {}
func (s *scripted) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return run(ctx, req, h)
}

const owner = 42

func setup(t *testing.T) (*Bot, *fakeAPI, map[string]*scripted) {
	t.Helper()
	api := &fakeAPI{ch: make(chan call, 100)}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	cfg := &config.Config{
		Home:            home,
		Telegram:        config.Telegram{Token: "1:x", AllowedUsers: []int64{owner}},
		Defaults:        config.Defaults{Agent: "alpha", Cwd: home, Mode: "ask"},
		AutoAllow:       []string{"read"},
		ApprovalTimeout: config.Duration(5 * time.Second),
		Agents:          map[string]config.Agent{"alpha": {Type: "command"}, "beta": {Type: "acp"}},
		Output:          config.Output{FileThreshold: 200},
	}
	agents := map[string]*scripted{"alpha": {models: []string{"m1", "m2"}}, "beta": {}}
	echo := func(name string) func(context.Context, agent.Request, agent.Handler) (agent.Result, error) {
		return func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
			h.Text(name + " got: " + req.Prompt[0].Text)
			return agent.Result{}, nil
		}
	}
	agents["alpha"].run, agents["beta"].run = echo("alpha"), echo("beta")

	st, _ := store.Open(filepath.Join(home, "state.json"))
	br, err := bridge.Start("")
	if err != nil {
		t.Fatal(err)
	}
	as := map[string]agent.Agent{}
	for k, v := range agents {
		as[k] = v
	}
	c := core.New(cfg, st, as, br, nil, nil)
	b, err := New(cfg, c, bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	return b, api, agents
}

func message(from int64, thread int, text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 1, From: &models.User{ID: from}, Chat: models.Chat{ID: 1, Type: "supergroup"},
		Text: text, MessageThreadID: thread, IsTopicMessage: thread != 0,
	}}
}

func callback(data string, thread int) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: models.User{ID: owner}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 99, Chat: models.Chat{ID: 1}, MessageThreadID: thread, IsTopicMessage: thread != 0,
		}},
	}}
}

func TestTextReplyInForumTopic(t *testing.T) {
	b, api, _ := setup(t)
	b.handleUpdate(context.Background(), nil, message(owner, 7, "hello"))
	c := api.wait(t, "sendMessage")
	if !strings.Contains(c.params["text"], "alpha got: hello") || c.params["message_thread_id"] != "7" {
		t.Errorf("reply = %+v", c.params)
	}
}

func TestUnauthorized(t *testing.T) {
	b, api, _ := setup(t)
	b.handleUpdate(context.Background(), nil, message(666, 0, "hi"))
	if c := api.wait(t, "sendMessage"); !strings.Contains(c.params["text"], "Not authorized") {
		t.Errorf("reply = %q", c.params["text"])
	}
}

func TestAgentPicker(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()
	b.handleUpdate(ctx, nil, message(owner, 0, "/agent"))
	c := api.wait(t, "sendMessage")
	if !strings.Contains(c.params["reply_markup"], "✓ alpha") || !strings.Contains(c.params["reply_markup"], "pk|agent|1") {
		t.Fatalf("picker = %s", c.params["reply_markup"])
	}
	b.handleUpdate(ctx, nil, callback("pk|agent|1", 0))
	if e := api.wait(t, "editMessageText"); !strings.Contains(e.params["text"], "beta") {
		t.Errorf("edit = %q", e.params["text"])
	}
	b.handleUpdate(ctx, nil, message(owner, 0, "now?"))
	api.find(t, "sendMessage", "beta got: now?")
}

func TestTopicsAreSeparateConversations(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()
	b.handleUpdate(ctx, nil, message(owner, 5, "/agent beta"))
	api.find(t, "sendMessage", "Agent")
	b.handleUpdate(ctx, nil, message(owner, 6, "x"))
	if c := api.find(t, "sendMessage", "got: x"); !strings.Contains(c.params["text"], "alpha") || c.params["message_thread_id"] != "6" {
		t.Errorf("topic 6 should still use alpha: %+v", c.params)
	}
	b.handleUpdate(ctx, nil, message(owner, 5, "y"))
	if c := api.find(t, "sendMessage", "got: y"); !strings.Contains(c.params["text"], "beta") || c.params["message_thread_id"] != "5" {
		t.Errorf("topic 5 should use beta: %+v", c.params)
	}
}

func TestApprovalButtons(t *testing.T) {
	b, api, agents := setup(t)
	got := make(chan agent.Decision, 1)
	agents["alpha"].run = func(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		got <- h.Permission(ctx, agent.Permission{Tool: "Bash", Kind: agent.KindExecute, Detail: "rm -rf build", Options: agent.DefaultOptions()})
		return agent.Result{}, nil
	}
	ctx := context.Background()
	b.handleUpdate(ctx, nil, message(owner, 0, "clean up"))
	c := api.wait(t, "sendMessage")
	if !strings.Contains(c.params["text"], "rm -rf build") {
		t.Fatalf("approval text = %q", c.params["text"])
	}
	var markup models.InlineKeyboardMarkup
	json.Unmarshal([]byte(c.params["reply_markup"]), &markup)
	deny := markup.InlineKeyboard[1][0] // Allow, Always / Deny
	if !strings.Contains(deny.Text, "Deny") {
		t.Fatalf("buttons = %+v", markup.InlineKeyboard)
	}
	b.handleUpdate(ctx, nil, callback(deny.CallbackData, 0))
	if d := <-got; d.OptionID != "deny" {
		t.Errorf("decision = %+v", d)
	}
	if e := api.wait(t, "editMessageText"); !strings.Contains(e.params["text"], "Denied") {
		t.Errorf("resolved text = %q", e.params["text"])
	}
}

func TestLongReplyBecomesFile(t *testing.T) {
	b, api, agents := setup(t)
	agents["alpha"].run = func(_ context.Context, _ agent.Request, h agent.Handler) (agent.Result, error) {
		h.Text(strings.Repeat("long line of output\n", 50))
		return agent.Result{}, nil
	}
	b.handleUpdate(context.Background(), nil, message(owner, 0, "go"))
	if c := api.wait(t, "sendDocument"); c.params["document"] != "<file>" {
		t.Errorf("document = %+v", c.params)
	}
}

func TestQueueNotice(t *testing.T) {
	b, api, agents := setup(t)
	release := make(chan struct{})
	agents["alpha"].run = func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		<-release
		h.Text("done " + req.Prompt[0].Text)
		return agent.Result{}, nil
	}
	ctx := context.Background()
	b.handleUpdate(ctx, nil, message(owner, 0, "one"))
	time.Sleep(50 * time.Millisecond)
	b.handleUpdate(ctx, nil, message(owner, 0, "two"))
	if c := api.wait(t, "sendMessage"); !strings.Contains(c.params["text"], "Queued (1 waiting)") {
		t.Errorf("notice = %q", c.params["text"])
	}
	close(release)
	api.find(t, "sendMessage", "done one")
	api.find(t, "sendMessage", "done two")
}
