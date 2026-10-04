package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/core"
)

type fakeTranscriber struct {
	text string
	err  error
}

func (f fakeTranscriber) Transcribe(context.Context, string) (string, error) { return f.text, f.err }

type fakeSpeaker struct{}

func (fakeSpeaker) Speak(context.Context, string) ([]byte, error) { return []byte("OggS"), nil }

func withSpeech(tr fakeTranscriber) func(*core.Deps) {
	return func(d *core.Deps) { d.Transcriber, d.Speaker = tr, fakeSpeaker{} }
}

func buttons(t *testing.T, c call) [][]models.InlineKeyboardButton {
	t.Helper()
	var m models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(c.params["reply_markup"]), &m); err != nil {
		t.Fatalf("reply_markup %q: %v", c.params["reply_markup"], err)
	}
	return m.InlineKeyboard
}

// button finds the button whose label starts with prefix.
func button(t *testing.T, rows [][]models.InlineKeyboardButton, prefix string) models.InlineKeyboardButton {
	t.Helper()
	for _, r := range rows {
		for _, btn := range r {
			if strings.HasPrefix(btn.Text, prefix) || strings.HasPrefix(btn.Text, "✓ "+prefix) {
				return btn
			}
		}
	}
	t.Fatalf("no button %q in %+v", prefix, rows)
	return models.InlineKeyboardButton{}
}

func send(b *Bot, text string) { b.handleUpdate(context.Background(), nil, message(owner, 0, text)) }

func TestSimpleCommands(t *testing.T) {
	b, api, _ := setup(t)
	cases := []struct{ cmd, want string }{
		{"/help", "your coding agent in your pocket"},
		{"/start", "Recommended: one topic per project"},
		{"/new", "New session"},
		{"/stop", "Nothing is running"},
		{"/cwd", b.cfg.Defaults.Cwd},
		{"/cwd /no/such/dir", "not a directory"},
		{"/diff", "checkpoints are off"},
		{"/undo", "checkpoints are off"},
		{"/voice", "voice replies are off"},
		{"/usage", "Today: $0.0000"},
		{"/status", "Agent: <b>alpha</b>"},
		{"/sessions", "Nothing to choose from"},
		{"/mode wild", "mode must be one of"},
		{"/agent nope", "unknown agent"},
	}
	for _, tc := range cases {
		send(b, tc.cmd)
		api.find(t, "sendMessage", tc.want)
	}
}

func TestCwdChange(t *testing.T) {
	b, api, _ := setup(t)
	dir := t.TempDir()
	send(b, "/cwd "+dir)
	api.find(t, "sendMessage", "Now in")
	send(b, "/status")
	api.find(t, "sendMessage", dir)
}

func TestUnknownSlashGoesToAgent(t *testing.T) {
	b, api, _ := setup(t)
	send(b, "/compact please")
	api.find(t, "sendMessage", "alpha got: /compact please")
}

func TestModelAndModePickers(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()

	send(b, "/model")
	rows := buttons(t, api.find(t, "sendMessage", "Model for alpha"))
	if len(rows) != 3 || !strings.Contains(rows[0][0].Text, "✓ default") || rows[2][0].Text != "m2" {
		t.Fatalf("model picker = %+v", rows)
	}
	b.handleUpdate(ctx, nil, callback(rows[2][0].CallbackData, 0))
	api.find(t, "editMessageText", "Model: <b>m2</b>")

	send(b, "/mode")
	rows = buttons(t, api.find(t, "sendMessage", "Permission mode"))
	// The scripted agent supports ask and full only.
	if len(rows) != 2 || !strings.HasPrefix(rows[1][0].Text, "full") {
		t.Fatalf("mode picker = %+v", rows)
	}
	b.handleUpdate(ctx, nil, callback(rows[1][0].CallbackData, 0))
	api.find(t, "editMessageText", "Mode: <b>full</b>")

	send(b, "/status")
	api.find(t, "sendMessage", "Model: m2 · Mode: full")
}

func TestProjectsAndSessions(t *testing.T) {
	b, api, agents := setup(t)
	dir := t.TempDir()
	b.cfg.Projects = map[string]config.Project{"site": {Cwd: dir, Agent: "beta"}}
	agents["beta"].run = func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		h.Session("sess-42")
		h.Message("beta in " + req.Cwd)
		return agent.Result{}, nil
	}

	send(b, "/project")
	rows := buttons(t, api.find(t, "sendMessage", "Choose a project"))
	b.handleUpdate(context.Background(), nil, callback(button(t, rows, "site · ").CallbackData, 0))
	api.find(t, "editMessageText", "Project <b>site</b>: beta in")

	send(b, "fix the header")
	api.find(t, "sendMessage", "beta in "+dir)

	send(b, "/new")
	api.find(t, "sendMessage", "New session")
	send(b, "/sessions")
	rows = buttons(t, api.find(t, "sendMessage", "Resume a session"))
	if !strings.Contains(rows[0][0].Text, "fix the header") {
		t.Fatalf("sessions = %+v", rows)
	}
	b.handleUpdate(context.Background(), nil, callback(rows[0][0].CallbackData, 0))
	api.find(t, "editMessageText", "Resumed: <i>fix the header</i>")
}

func TestStaleCallbacks(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()
	b.handleUpdate(ctx, nil, callback("ap|gone|0", 0))
	if c := api.wait(t, "answerCallbackQuery"); !strings.Contains(c.params["text"], "expired") {
		t.Errorf("answer = %q", c.params["text"])
	}
	b.handleUpdate(ctx, nil, callback("pk|agent|99", 0))
	if c := api.wait(t, "answerCallbackQuery"); !strings.Contains(c.params["text"], "list changed") {
		t.Errorf("answer = %q", c.params["text"])
	}
	stranger := callback("pk|agent|0", 0)
	stranger.CallbackQuery.From.ID = 666
	b.handleUpdate(ctx, nil, stranger)
	if c := api.wait(t, "answerCallbackQuery"); c.params["text"] != "Not authorized" {
		t.Errorf("answer = %q", c.params["text"])
	}
}

func TestVoiceMessage(t *testing.T) {
	b, api, _ := setup(t, withSpeech(fakeTranscriber{text: "list the files"}))
	m := message(owner, 0, "")
	m.Message.Voice = &models.Voice{FileID: "v1.oga"}
	b.handleUpdate(context.Background(), nil, m)
	api.find(t, "editMessageText", "🎙 <i>list the files</i>")
	api.find(t, "sendMessage", "alpha got: list the files")
}

func TestVoiceFailures(t *testing.T) {
	b, api, _ := setup(t)
	m := message(owner, 0, "")
	m.Message.Voice = &models.Voice{FileID: "v1.oga"}
	b.handleUpdate(context.Background(), nil, m)
	api.find(t, "sendMessage", "Voice messages are off")

	b, api, _ = setup(t, withSpeech(fakeTranscriber{err: errors.New("couldn't hear anything")}))
	b.handleUpdate(context.Background(), nil, m)
	api.find(t, "editMessageText", "hear anything")
}

func TestVoiceReplies(t *testing.T) {
	b, api, _ := setup(t, withSpeech(fakeTranscriber{}))
	send(b, "/voice")
	api.find(t, "sendMessage", "Voice replies on")
	send(b, "talk to me")
	api.wait(t, "sendVoice")
}

func TestPhotoBecomesImageBlock(t *testing.T) {
	b, api, agents := setup(t)
	got := make(chan []agent.Block, 1)
	agents["alpha"].run = func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		got <- req.Prompt
		h.Message("seen")
		return agent.Result{}, nil
	}
	m := message(owner, 0, "")
	m.Message.Photo = []models.PhotoSize{{FileID: "small.png"}, {FileID: "large.png"}}
	m.Message.Caption = "what is wrong here?"
	b.handleUpdate(context.Background(), nil, m)
	blocks := <-got
	if len(blocks) != 2 || blocks[0].MimeType != "image/png" || blocks[1].Text != "what is wrong here?" {
		t.Errorf("blocks = %+v", blocks)
	}
	api.find(t, "sendMessage", "seen")
}

func TestDocumentIsSavedForTheAgent(t *testing.T) {
	b, api, agents := setup(t)
	got := make(chan string, 1)
	agents["alpha"].run = func(_ context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
		got <- req.Prompt[0].Text
		return agent.Result{}, nil
	}
	m := message(owner, 0, "")
	m.Message.Document = &models.Document{FileID: "report.txt", FileName: "../../etc/report.txt"}
	b.handleUpdate(context.Background(), nil, m)
	text := <-got
	path := strings.TrimSuffix(text[strings.Index(text, "/"):], "]")
	if filepath.Dir(path) != filepath.Join(b.cfg.Home, "uploads") || !strings.HasSuffix(path, "-report.txt") {
		t.Errorf("saved outside uploads: %q", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "plain file bytes" {
		t.Errorf("content = %q", data)
	}
	api.wait(t, "getFile")
}

func TestAgentCanSendFiles(t *testing.T) {
	b, api, agents := setup(t)
	img := filepath.Join(t.TempDir(), "chart.png")
	os.WriteFile(img, []byte(pngHeader), 0o600)
	txt := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(txt, []byte("hi"), 0o600)
	agents["alpha"].run = func(ctx context.Context, _ agent.Request, h agent.Handler) (agent.Result, error) {
		if err := h.SendFile(ctx, img, "chart"); err != nil {
			return agent.Result{}, err
		}
		return agent.Result{}, h.SendFile(ctx, txt, "")
	}
	send(b, "show me")
	if c := api.wait(t, "sendPhoto"); c.params["caption"] != "chart" {
		t.Errorf("photo = %+v", c.params)
	}
	api.wait(t, "sendDocument")
}

func TestToolProgressAndSummary(t *testing.T) {
	b, api, agents := setup(t)
	agents["alpha"].run = func(_ context.Context, _ agent.Request, h agent.Handler) (agent.Result, error) {
		h.ToolCall("Read: main.go", agent.KindRead)
		h.Message("done reading")
		return agent.Result{CostUSD: 0.0123}, nil
	}
	send(b, "go")
	api.find(t, "sendMessage", "🔧 <code>Read: main.go</code>")
	api.find(t, "sendMessage", "done reading")
	api.find(t, "sendMessage", "$0.0123 · 1 tool calls · alpha")
}

func TestAgentErrorIsReported(t *testing.T) {
	b, api, agents := setup(t)
	agents["alpha"].run = func(context.Context, agent.Request, agent.Handler) (agent.Result, error) {
		return agent.Result{}, errors.New("agent exploded")
	}
	send(b, "go")
	api.find(t, "sendMessage", "agent exploded")
}

func TestProjectAddFromChat(t *testing.T) {
	b, api, _ := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	send(b, "/cwd "+dir)
	api.find(t, "sendMessage", "Now in")

	// With no projects, the picker offers to add the current folder.
	send(b, "/project")
	rows := buttons(t, api.find(t, "sendMessage", "Choose a project"))
	b.handleUpdate(ctx, nil, callback(button(t, rows, "➕ Add this folder ("+filepath.Base(dir)+")").CallbackData, 0))
	api.find(t, "editMessageText", "Added project <b>"+filepath.Base(dir)+"</b>")

	// Now it is listed with its path, and the add button is gone.
	send(b, "/project")
	rows = buttons(t, api.find(t, "sendMessage", "Choose a project"))
	button(t, rows, filepath.Base(dir)+" · ")
	for _, r := range rows {
		if strings.HasPrefix(r[0].Text, "➕ Add this folder") {
			t.Fatalf("add button still shown for a project folder: %+v", rows)
		}
	}

	other := t.TempDir()
	send(b, "/project add other "+other)
	api.find(t, "sendMessage", "Added project <b>other</b>")
	send(b, "/project add other "+other)
	api.find(t, "sendMessage", "already exists")
	send(b, "/project other")
	api.find(t, "sendMessage", "Project <b>other</b>")
	send(b, "/status")
	api.find(t, "sendMessage", "Project: other")

	send(b, "/project remove other")
	api.find(t, "sendMessage", "Removed project <b>other</b>")
	send(b, "/project remove")
	api.find(t, "sendMessage", "Usage: /project remove")
	send(b, "/project add bad/name")
	api.find(t, "sendMessage", "project names use")
}

func TestProjectFromConfigCannotBeRemoved(t *testing.T) {
	b, api, _ := setup(t)
	b.cfg.Projects = map[string]config.Project{"site": {Cwd: t.TempDir()}}
	b.cfg.Path = "/home/me/.pocketagent/config.yaml"
	send(b, "/project remove site")
	api.find(t, "sendMessage", "defined in /home/me/.pocketagent/config.yaml")
}
