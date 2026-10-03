package acp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	sdk "github.com/coder/acp-go-sdk"
)

// The test binary doubles as a scripted ACP agent: run with
// POCKETAGENT_FAKE_ACP=1 it speaks the protocol on stdin/stdout.
func TestMain(m *testing.M) {
	if os.Getenv("POCKETAGENT_FAKE_ACP") == "1" {
		runFakeAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runFakeAgent() {
	f := &fakeAgent{sessions: map[string]*fakeSession{}}
	f.conn = sdk.NewAgentSideConnection(f, os.Stdout, os.Stdin)
	<-f.conn.Done()
}

type fakeSession struct {
	mode, model string
	cost        float64
}

// fakeAgent behaves according to keywords in the prompt:
//
//	hello  replies in two chunks and reports a running cost
//	tool   announces a tool call and asks permission for it
//	slow   runs until cancelled
//	image  reports how many images it received
//
// Every reply ends with the session's current mode and model.
type fakeAgent struct {
	conn     *sdk.AgentSideConnection
	mu       sync.Mutex
	sessions map[string]*fakeSession
	next     int
}

var _ sdk.AgentLoader = (*fakeAgent)(nil)

func ptr[T any](v T) *T { return &v }

func fakeModes(current string) *sdk.SessionModeState {
	return &sdk.SessionModeState{CurrentModeId: sdk.SessionModeId(current), AvailableModes: []sdk.SessionMode{
		{Id: "default", Name: "Default"}, {Id: "plan", Name: "Plan"}, {Id: "bypassPermissions", Name: "Bypass"},
	}}
}

func fakeOptions(model string) []sdk.SessionConfigOption {
	opts := sdk.SessionConfigSelectOptionsUngrouped{{Value: "m1", Name: "Model One"}, {Value: "m2", Name: "Model Two"}}
	return []sdk.SessionConfigOption{{Select: &sdk.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Type: "select", Category: ptr(sdk.SessionConfigOptionCategoryModel),
		CurrentValue: sdk.SessionConfigValueId(model), Options: sdk.SessionConfigSelectOptions{Ungrouped: &opts},
	}}}
}

func (f *fakeAgent) session(id sdk.SessionId) *fakeSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[string(id)]
	if !ok {
		s = &fakeSession{mode: "default", model: "m1"}
		f.sessions[string(id)] = s
	}
	return s
}

func (f *fakeAgent) Initialize(context.Context, sdk.InitializeRequest) (sdk.InitializeResponse, error) {
	return sdk.InitializeResponse{ProtocolVersion: sdk.ProtocolVersionNumber, AuthMethods: []sdk.AuthMethod{},
		AgentCapabilities: sdk.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: sdk.PromptCapabilities{Image: true},
			McpCapabilities:    sdk.McpCapabilities{Http: true},
		}}, nil
}

func (f *fakeAgent) NewSession(_ context.Context, req sdk.NewSessionRequest) (sdk.NewSessionResponse, error) {
	f.mu.Lock()
	f.next++
	id := sdk.SessionId(fmt.Sprintf("s%d-mcp%d", f.next, len(req.McpServers)))
	f.mu.Unlock()
	f.session(id)
	return sdk.NewSessionResponse{SessionId: id, Modes: fakeModes("default"), ConfigOptions: fakeOptions("m1")}, nil
}

func (f *fakeAgent) LoadSession(ctx context.Context, req sdk.LoadSessionRequest) (sdk.LoadSessionResponse, error) {
	if strings.HasPrefix(string(req.SessionId), "missing") {
		return sdk.LoadSessionResponse{}, fmt.Errorf("no such session")
	}
	// Replaying history is part of session/load; the client must not resend it.
	_ = f.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: req.SessionId, Update: sdk.SessionUpdate{
		AgentMessageChunk: &sdk.SessionUpdateAgentMessageChunk{Content: sdk.TextBlock("OLD HISTORY"), SessionUpdate: "agent_message_chunk"},
	}})
	s := f.session(req.SessionId)
	return sdk.LoadSessionResponse{Modes: fakeModes(s.mode), ConfigOptions: fakeOptions(s.model)}, nil
}

func (f *fakeAgent) SetSessionMode(_ context.Context, req sdk.SetSessionModeRequest) (sdk.SetSessionModeResponse, error) {
	f.session(req.SessionId).mode = string(req.ModeId)
	return sdk.SetSessionModeResponse{}, nil
}

func (f *fakeAgent) SetSessionConfigOption(_ context.Context, req sdk.SetSessionConfigOptionRequest) (sdk.SetSessionConfigOptionResponse, error) {
	v := req.ValueId
	s := f.session(v.SessionId)
	s.model = string(v.Value)
	return sdk.SetSessionConfigOptionResponse{ConfigOptions: fakeOptions(s.model)}, nil
}

func (f *fakeAgent) say(ctx context.Context, sid sdk.SessionId, text string) {
	_ = f.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: sid, Update: sdk.SessionUpdate{
		AgentMessageChunk: &sdk.SessionUpdateAgentMessageChunk{Content: sdk.TextBlock(text), SessionUpdate: "agent_message_chunk"},
	}})
}

func (f *fakeAgent) Prompt(ctx context.Context, req sdk.PromptRequest) (sdk.PromptResponse, error) {
	s := f.session(req.SessionId)
	var text strings.Builder
	images := 0
	for _, b := range req.Prompt {
		if b.Text != nil {
			text.WriteString(b.Text.Text)
		}
		if b.Image != nil {
			images++
		}
	}
	p := text.String()
	switch {
	case strings.Contains(p, "slow"):
		<-ctx.Done()
		return sdk.PromptResponse{StopReason: sdk.StopReasonCancelled}, nil
	case strings.Contains(p, "tool"):
		_ = f.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: req.SessionId, Update: sdk.SessionUpdate{
			ToolCall: &sdk.SessionUpdateToolCall{ToolCallId: "t1", Title: "Run make test", Kind: sdk.ToolKindExecute, Status: sdk.ToolCallStatusPending, SessionUpdate: "tool_call"},
		}})
		resp, err := f.conn.RequestPermission(ctx, sdk.RequestPermissionRequest{
			SessionId: req.SessionId,
			ToolCall: sdk.ToolCallUpdate{ToolCallId: "t1", Title: ptr("Run make test"), Kind: ptr(sdk.ToolKindExecute),
				RawInput: map[string]any{"command": []any{"make", "test"}}},
			Options: []sdk.PermissionOption{
				{OptionId: "yes", Name: "Allow", Kind: sdk.PermissionOptionKindAllowOnce},
				{OptionId: "no", Name: "Reject", Kind: sdk.PermissionOptionKindRejectOnce},
			},
		})
		switch {
		case err != nil:
			f.say(ctx, req.SessionId, "permission error")
		case resp.Outcome.Selected != nil:
			f.say(ctx, req.SessionId, "chose "+string(resp.Outcome.Selected.OptionId))
		default:
			f.say(ctx, req.SessionId, "cancelled")
		}
	case strings.Contains(p, "image"):
		f.say(ctx, req.SessionId, fmt.Sprintf("%d images", images))
	default:
		f.say(ctx, req.SessionId, "Hel")
		f.say(ctx, req.SessionId, "lo")
	}
	s.cost += 0.01
	_ = f.conn.SessionUpdate(ctx, sdk.SessionNotification{SessionId: req.SessionId, Update: sdk.SessionUpdate{
		UsageUpdate: &sdk.SessionUsageUpdate{Cost: &sdk.Cost{Amount: s.cost, Currency: "USD"}, SessionUpdate: "usage_update"},
	}})
	f.say(ctx, req.SessionId, fmt.Sprintf(" [mode=%s model=%s]", s.mode, s.model))
	return sdk.PromptResponse{StopReason: sdk.StopReasonEndTurn}, nil
}

func (f *fakeAgent) Authenticate(context.Context, sdk.AuthenticateRequest) (sdk.AuthenticateResponse, error) {
	return sdk.AuthenticateResponse{}, nil
}
func (f *fakeAgent) Logout(context.Context, sdk.LogoutRequest) (sdk.LogoutResponse, error) {
	return sdk.LogoutResponse{}, nil
}
func (f *fakeAgent) Cancel(context.Context, sdk.CancelNotification) error { return nil }
func (f *fakeAgent) CloseSession(context.Context, sdk.CloseSessionRequest) (sdk.CloseSessionResponse, error) {
	return sdk.CloseSessionResponse{}, nil
}
func (f *fakeAgent) ListSessions(context.Context, sdk.ListSessionsRequest) (sdk.ListSessionsResponse, error) {
	return sdk.ListSessionsResponse{}, nil
}
func (f *fakeAgent) ResumeSession(context.Context, sdk.ResumeSessionRequest) (sdk.ResumeSessionResponse, error) {
	return sdk.ResumeSessionResponse{}, fmt.Errorf("not supported")
}
