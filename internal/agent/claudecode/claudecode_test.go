package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

// The test binary doubles as a fake `claude`: with POCKETAGENT_FAKE_CLAUDE=1
// it reads one stream-json message and answers with scripted events.
func TestMain(m *testing.M) {
	if os.Getenv("POCKETAGENT_FAKE_CLAUDE") == "1" {
		fakeClaude(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeClaude(args []string) {
	if i := slices.Index(args, "--resume"); i >= 0 && args[i+1] == "gone" {
		fmt.Fprintln(os.Stderr, "No conversation found with session ID: gone")
		os.Exit(1)
	}
	var msg struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
	_ = json.Unmarshal(line, &msg)
	var kinds, text []string
	for _, c := range msg.Message.Content {
		kinds = append(kinds, c.Type)
		text = append(text, c.Text)
	}
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": "sess-1"})
	prompt := strings.Join(text, " ")
	if strings.Contains(prompt, "crash") {
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(2)
	}
	if strings.Contains(prompt, "silent") { // only a result, no assistant text
		emit(map[string]any{"type": "result", "subtype": "success", "session_id": "sess-1", "result": "final answer", "total_cost_usd": 0.5})
		return
	}
	emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "ls -la"}},
		map[string]any{"type": "text", "text": "args=" + strings.Join(args, " ") + " blocks=" + strings.Join(kinds, ",")},
	}}})
	emit(map[string]any{"type": "result", "subtype": "success", "session_id": "sess-1", "result": "ignored", "total_cost_usd": 0.25})
}

type recorder struct {
	sessions, messages, tools []string
	decision                  agent.Decision
	asked                     []agent.Permission
}

func (r *recorder) Session(id string)                              { r.sessions = append(r.sessions, id) }
func (r *recorder) Message(s string)                               { r.messages = append(r.messages, s) }
func (r *recorder) ToolCall(t string, k agent.Kind)                { r.tools = append(r.tools, string(k)+" "+t) }
func (r *recorder) SendFile(context.Context, string, string) error { return nil }
func (r *recorder) Permission(_ context.Context, p agent.Permission) agent.Decision {
	r.asked = append(r.asked, p)
	return r.decision
}

func fake(t *testing.T) agent.Agent {
	t.Helper()
	a, err := New(agent.Spec{Name: "cc", Command: []string{os.Args[0]}, Env: []string{"POCKETAGENT_FAKE_CLAUDE=1"}, Options: agent.NoOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRunStreamsEvents(t *testing.T) {
	h := &recorder{}
	res, err := fake(t).Run(context.Background(), agent.Request{
		Cwd: t.TempDir(), SessionID: "prev", Model: "m", Mode: agent.ModeEdits, AlwaysAllow: []string{"Bash"},
		Tools:  &agent.ToolServer{Name: "pa", URL: "http://x/mcp/cc", Headers: map[string]string{"H": "v"}},
		Prompt: []agent.Block{{Image: []byte("png"), MimeType: "image/png"}, {Text: "hi"}},
	}, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "sess-1" || res.CostUSD != 0.25 || res.Final != "" {
		t.Errorf("result = %+v", res)
	}
	if !slices.Equal(h.sessions, []string{"sess-1"}) || !slices.Equal(h.tools, []string{"execute Bash: ls -la"}) {
		t.Errorf("sessions %v tools %v", h.sessions, h.tools)
	}
	out := strings.Join(h.messages, "")
	for _, want := range []string{
		"--resume prev", "--model m", "--permission-mode acceptEdits",
		"--permission-prompt-tool mcp__pa__approve", "--allowedTools Bash,mcp__pa__send_file",
		`"url":"http://x/mcp/cc"`, "blocks=image,text",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestFinalTextWhenNothingStreamed(t *testing.T) {
	h := &recorder{}
	res, err := fake(t).Run(context.Background(), agent.Request{Cwd: t.TempDir(), Prompt: []agent.Block{{Text: "silent"}}}, h)
	if err != nil || res.Final != "final answer" {
		t.Errorf("res %+v err %v", res, err)
	}
}

func TestMissingSessionStartsFresh(t *testing.T) {
	h := &recorder{}
	res, err := fake(t).Run(context.Background(), agent.Request{Cwd: t.TempDir(), SessionID: "gone", Prompt: []agent.Block{{Text: "hi"}}}, h)
	if err != nil || res.SessionID != "sess-1" {
		t.Fatalf("res %+v err %v", res, err)
	}
	if !strings.Contains(h.messages[0], "starting a new one") || strings.Contains(strings.Join(h.messages, ""), "--resume") {
		t.Errorf("messages = %v", h.messages)
	}
}

func TestCrashReportsStderr(t *testing.T) {
	_, err := fake(t).Run(context.Background(), agent.Request{Cwd: t.TempDir(), Prompt: []agent.Block{{Text: "crash"}}}, &recorder{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestApproveTool(t *testing.T) {
	tool := fake(t).(agent.ToolProvider).Tools()[0]
	args := json.RawMessage(`{"tool_name":"Bash","input":{"command":"rm -rf build"}}`)
	cases := []struct {
		d    agent.Decision
		want string
	}{
		{agent.Decision{OptionID: "allow"}, `"behavior":"allow"`},
		{agent.Decision{OptionID: "always"}, `"behavior":"allow"`},
		{agent.Decision{OptionID: "deny"}, `"The user denied this action."`},
		{agent.Decision{OptionID: "deny", Message: "use make clean"}, `said: use make clean`},
		{agent.Decision{}, `"behavior":"deny"`},
	}
	for _, tc := range cases {
		h := &recorder{decision: tc.d}
		out, err := tool.Call(context.Background(), h, args)
		if err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("%+v: %s %v", tc.d, out, err)
		}
		if h.asked[0].Detail != "rm -rf build" || h.asked[0].Key != "Bash" {
			t.Errorf("permission = %+v", h.asked[0])
		}
	}
	if _, err := tool.Call(context.Background(), &recorder{}, json.RawMessage(`not json`)); err == nil {
		t.Error("bad arguments accepted")
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		kind  agent.Kind
		title string
	}{
		{"Read", map[string]any{"file_path": "/a"}, agent.KindRead, "Read: /a"},
		{"Grep", map[string]any{"pattern": "x"}, agent.KindSearch, "Grep: x"},
		{"Edit", map[string]any{"file_path": "/a", "old_string": "o", "new_string": "n"}, agent.KindEdit, "Edit: /a"},
		{"Write", map[string]any{"file_path": "/a", "content": "c"}, agent.KindEdit, "Write: /a"},
		{"WebFetch", map[string]any{"url": "https://x"}, agent.KindFetch, "WebFetch: https://x"},
		{"Task", map[string]any{"description": "plan"}, agent.KindThink, "Task: plan"},
		{"Unknown", map[string]any{}, agent.KindOther, "Unknown"},
	}
	for _, tc := range cases {
		p := Describe(tc.tool, tc.input)
		if p.Kind != tc.kind || p.Title != tc.title || len(p.Options) != 3 {
			t.Errorf("%s: %+v", tc.tool, p)
		}
	}
	long := Describe("Bash", map[string]any{"command": strings.Repeat("x", 300), "description": "d"})
	if len(long.Title) > 130 || !strings.HasPrefix(long.Detail, "# d\n") {
		t.Errorf("bash: %q / %q", long.Title, long.Detail[:10])
	}
}

func TestApprovalToolHints(t *testing.T) {
	tool := fake(t).(agent.ToolProvider).Tools()[0]
	if h := tool.Hints; h.Title == "" || !h.ReadOnly || h.Destructive || h.OpenWorld {
		t.Errorf("hints = %+v", h)
	}
}
