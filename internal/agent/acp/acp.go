// Package acp drives any agent that speaks the Agent Client Protocol
// (https://agentclientprotocol.com). One agent process is kept per
// conversation and reused across turns, then stopped after it has been idle.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/argv"
	"github.com/c0sm0thecoder/pocketagent/internal/term"
)

type options struct {
	// Modes maps pocketagent modes to the agent's mode ids, tried in order.
	// Entries replace the defaults for that mode.
	Modes map[agent.Mode][]string `yaml:"modes"`
	// IdleTimeout stops a conversation's agent process after inactivity.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

// defaultModeIDs are mode ids common across ACP agents, most specific first.
var defaultModeIDs = map[agent.Mode][]string{
	agent.ModeAsk:   {"default", "ask", "normal"},
	agent.ModeEdits: {"acceptEdits", "accept_edits", "auto_edit", "autoEdit", "auto-edit"},
	agent.ModePlan:  {"plan"},
	agent.ModeFull:  {"bypassPermissions", "full-access", "full_access", "yolo"},
}

type Agent struct {
	spec    agent.Spec
	modeIDs map[agent.Mode][]string
	idle    time.Duration

	mu        sync.Mutex
	procs     map[string]*proc
	stop      chan struct{}
	closeOnce sync.Once
}

var (
	_ agent.Agent       = (*Agent)(nil)
	_ agent.ModelLister = (*Agent)(nil)
	_ io.Closer         = (*Agent)(nil)
)

func New(spec agent.Spec) (agent.Agent, error) {
	var o options
	if err := spec.Options.Decode(&o); err != nil {
		return nil, err
	}
	a := &Agent{spec: spec, modeIDs: maps.Clone(defaultModeIDs), idle: 30 * time.Minute,
		procs: map[string]*proc{}, stop: make(chan struct{})}
	maps.Copy(a.modeIDs, o.Modes)
	if o.IdleTimeout > 0 {
		a.idle = o.IdleTimeout
	}
	go a.reap()
	return a, nil
}

func (a *Agent) Caps() agent.Caps {
	// Exact support depends on the agent; these are reported optimistically
	// and checked against the agent's own capabilities at run time.
	return agent.Caps{Images: true, Resume: true, Modes: agent.Modes}
}

// Models lists the models the agent offers in the conversation's session.
func (a *Agent) Models(conv string) []string {
	a.mu.Lock()
	p := a.procs[conv]
	a.mu.Unlock()
	if p == nil {
		return nil
	}
	var models []string
	for _, o := range p.selectOptions(sdk.SessionConfigOptionCategoryModel) {
		models = append(models, string(o.Value))
	}
	return models
}

// Close stops every agent process. It is safe to call more than once.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() { close(a.stop) })
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, p := range a.procs {
		p.kill()
		delete(a.procs, k)
	}
	return nil
}

func (a *Agent) reap() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		}
		a.mu.Lock()
		for k, p := range a.procs {
			if p.idleSince() > a.idle {
				p.kill()
				delete(a.procs, k)
			}
		}
		a.mu.Unlock()
	}
}

// ---------- process ----------

type proc struct {
	cmd    *exec.Cmd
	conn   *sdk.ClientSideConnection
	client *client
	cwd    string
	init   sdk.InitializeResponse
	stderr *tail

	mu        sync.Mutex
	sessionID string
	options   []sdk.SessionConfigOption
	modes     *sdk.SessionModeState
	lastUsed  time.Time
	busy      bool
}

func (p *proc) idleSince() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return 0
	}
	return time.Since(p.lastUsed)
}

func (p *proc) alive() bool {
	select {
	case <-p.conn.Done():
		return false
	default:
		return true
	}
}

func (p *proc) kill() {
	if p.cmd.Process != nil {
		// Kill the whole process group: npx-launched agents fork children.
		// An error means the group is already gone.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (p *proc) selectOptions(cat sdk.SessionConfigOptionCategory) []sdk.SessionConfigSelectOption {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.options {
		if o.Select == nil || o.Select.Category == nil || *o.Select.Category != cat {
			continue
		}
		var out []sdk.SessionConfigSelectOption
		if u := o.Select.Options.Ungrouped; u != nil {
			out = append(out, *u...)
		}
		if g := o.Select.Options.Grouped; g != nil {
			for _, grp := range *g {
				out = append(out, grp.Options...)
			}
		}
		return out
	}
	return nil
}

func (p *proc) configID(cat sdk.SessionConfigOptionCategory) (sdk.SessionConfigId, sdk.SessionConfigValueId, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.options {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == cat {
			return o.Select.Id, o.Select.CurrentValue, true
		}
	}
	return "", "", false
}

// tail keeps the last few KB of the agent's stderr for error messages.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

func (a *Agent) proc(ctx context.Context, conv, cwd string) (*proc, error) {
	a.mu.Lock()
	p := a.procs[conv]
	if p != nil && (p.cwd != cwd || !p.alive()) {
		p.kill()
		delete(a.procs, conv)
		p = nil
	}
	a.mu.Unlock()
	if p != nil {
		return p, nil
	}

	cmdline := argv.Join(a.spec.Wrap, a.spec.Command, map[string]string{"cwd": cwd})
	// The process outlives this call; proc.kill and the idle reaper end it.
	cmd := exec.CommandContext(context.Background(), cmdline[0], cmdline[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), a.spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	errTail := &tail{}
	cmd.Stderr = errTail
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cmdline[0], err)
	}
	go func() { _ = cmd.Wait() }() // reap; exits surface through conn.Done

	cl := &client{}
	p = &proc{cmd: cmd, client: cl, cwd: cwd, stderr: errTail, lastUsed: time.Now()}
	cl.proc = p
	p.conn = sdk.NewClientSideConnection(cl, stdin, stdout)

	initCtx, cancel := context.WithTimeout(ctx, 2*time.Minute) // npx may need to download the adapter
	defer cancel()
	p.init, err = p.conn.Initialize(initCtx, sdk.InitializeRequest{
		ProtocolVersion: sdk.ProtocolVersionNumber,
		ClientInfo:      &sdk.Implementation{Name: "pocketagent", Version: "1"},
	})
	if err != nil {
		p.kill()
		return nil, a.explain("initialize", err, p)
	}

	a.mu.Lock()
	a.procs[conv] = p
	a.mu.Unlock()
	return p, nil
}

func (a *Agent) explain(step string, err error, p *proc) error {
	var re *sdk.RequestError
	msg := err.Error()
	if errors.As(err, &re) {
		data, _ := json.Marshal(re)
		msg = string(data)
	}
	if s := term.StripANSI(p.stderr.String()); s != "" {
		if i := strings.LastIndex(s, "\n"); i >= 0 && len(s)-i < 400 {
			s = s[i+1:]
		}
		msg += "\n" + s
	}
	if len(p.init.AuthMethods) > 0 && strings.Contains(strings.ToLower(msg), "auth") {
		msg += "\nThe agent needs you to log in: run it once in a terminal on this machine."
	}
	return fmt.Errorf("%s %s: %s", a.spec.Name, step, msg)
}

// ---------- turn ----------

func (a *Agent) Run(ctx context.Context, req agent.Request, h agent.Handler) (agent.Result, error) {
	p, err := a.proc(ctx, req.Conv, req.Cwd)
	if err != nil {
		return agent.Result{}, err
	}
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.busy, p.lastUsed = false, time.Now()
		p.mu.Unlock()
	}()

	cl := p.client
	cl.begin(ctx, h)
	defer cl.end()

	sid, err := a.session(ctx, p, req, h)
	if err != nil {
		return agent.Result{}, err
	}
	// Agents report a running session total; measure this turn's share.
	costAtStart := cl.cost()
	if err := a.applyMode(ctx, p, sid, req.Mode); err != nil {
		h.Message("_" + err.Error() + "_")
	}
	if err := a.applyModel(ctx, p, sid, req.Model); err != nil {
		h.Message("_" + err.Error() + "_")
	}

	var prompt []sdk.ContentBlock
	images := p.init.AgentCapabilities.PromptCapabilities.Image
	for _, b := range req.Prompt {
		switch {
		case b.IsImage() && images:
			prompt = append(prompt, sdk.ImageBlock(encode(b.Image), b.MimeType))
		case b.IsImage():
			path, err := saveTemp(b)
			if err == nil {
				prompt = append(prompt, sdk.TextBlock("[The user attached an image, saved at "+path+"]"))
			}
		case b.Text != "":
			prompt = append(prompt, sdk.TextBlock(b.Text))
		}
	}

	type promptResult struct {
		resp sdk.PromptResponse
		err  error
	}
	done := make(chan promptResult, 1)
	go func() { //nolint:gosec // cancelled via the ACP cancel notification, not ctx
		resp, err := p.conn.Prompt(context.Background(), sdk.PromptRequest{SessionId: sdk.SessionId(sid), Prompt: prompt})
		done <- promptResult{resp, err}
	}()

	var r promptResult
	select {
	case r = <-done:
	case <-p.conn.Done():
		return agent.Result{SessionID: sid}, a.explain("crashed", errors.New("agent exited"), p)
	case <-ctx.Done():
		// If the cancel can't be delivered, the timeout below kills the agent.
		_ = p.conn.Cancel(context.Background(), sdk.CancelNotification{SessionId: sdk.SessionId(sid)})
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			p.kill() // the agent ignored the cancel
		}
		cl.flush()
		return agent.Result{SessionID: sid}, ctx.Err()
	}
	cl.flush()
	res := agent.Result{SessionID: sid, CostUSD: max(cl.cost()-costAtStart, 0)}
	if r.err != nil {
		return res, a.explain("prompt", r.err, p)
	}
	res.StopReason = string(r.resp.StopReason)
	return res, nil
}

func (a *Agent) mcpServers(p *proc, req agent.Request) []sdk.McpServer {
	if req.Tools == nil || !p.init.AgentCapabilities.McpCapabilities.Http {
		return []sdk.McpServer{}
	}
	var headers []sdk.HttpHeader
	for k, v := range req.Tools.Headers {
		headers = append(headers, sdk.HttpHeader{Name: k, Value: v})
	}
	return []sdk.McpServer{{Http: &sdk.McpServerHttpInline{Name: req.Tools.Name, Type: "http", Url: req.Tools.URL, Headers: headers}}}
}

// session makes sure the process has the requested session open, resuming
// or loading it when possible and starting a new one otherwise.
func (a *Agent) session(ctx context.Context, p *proc, req agent.Request, h agent.Handler) (string, error) {
	p.mu.Lock()
	current := p.sessionID
	p.mu.Unlock()
	if current != "" && current == req.SessionID {
		return current, nil
	}
	caps := p.init.AgentCapabilities
	mcp := a.mcpServers(p, req)

	if req.SessionID != "" {
		var err error
		switch {
		case caps.SessionCapabilities.Resume != nil:
			var resp sdk.ResumeSessionResponse
			resp, err = p.conn.ResumeSession(ctx, sdk.ResumeSessionRequest{SessionId: sdk.SessionId(req.SessionID), Cwd: req.Cwd, McpServers: mcp})
			if err == nil {
				p.setSession(req.SessionID, resp.ConfigOptions, resp.Modes)
				return req.SessionID, nil
			}
		case caps.LoadSession:
			// session/load replays the history as updates; don't resend it.
			p.client.setSuppress(true)
			var resp sdk.LoadSessionResponse
			resp, err = p.conn.LoadSession(ctx, sdk.LoadSessionRequest{SessionId: sdk.SessionId(req.SessionID), Cwd: req.Cwd, McpServers: mcp})
			p.client.setSuppress(false)
			if err == nil {
				p.setSession(req.SessionID, resp.ConfigOptions, resp.Modes)
				return req.SessionID, nil
			}
		default:
			err = errors.New("this agent can't resume sessions")
		}
		h.Message("_Couldn't resume the previous session (" + shortErr(err) + "), starting a new one._")
	}

	resp, err := p.conn.NewSession(ctx, sdk.NewSessionRequest{Cwd: req.Cwd, McpServers: mcp})
	if err != nil {
		return "", a.explain("new session", err, p)
	}
	id := string(resp.SessionId)
	p.setSession(id, resp.ConfigOptions, resp.Modes)
	p.client.resetCost() // the process may have served another session before
	h.Session(id)
	return id, nil
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

func (p *proc) setSession(id string, opts []sdk.SessionConfigOption, modes *sdk.SessionModeState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessionID, p.options, p.modes = id, opts, modes
}

// applyMode switches the session to the agent's equivalent of mode. Only
// plan mode is an error when the agent has no equivalent: the other modes
// are also enforced by pocketagent's own approval policy.
func (a *Agent) applyMode(ctx context.Context, p *proc, sid string, mode agent.Mode) error {
	want := a.modeIDs[mode]
	p.mu.Lock()
	modes := p.modes
	p.mu.Unlock()
	if modes != nil {
		for _, id := range want {
			for _, m := range modes.AvailableModes {
				if !strings.EqualFold(string(m.Id), id) {
					continue
				}
				if m.Id == modes.CurrentModeId {
					return nil
				}
				if _, err := p.conn.SetSessionMode(ctx, sdk.SetSessionModeRequest{SessionId: sdk.SessionId(sid), ModeId: m.Id}); err != nil {
					return fmt.Errorf("switch to %s mode: %w", mode, err)
				}
				p.mu.Lock()
				p.modes.CurrentModeId = m.Id
				p.mu.Unlock()
				return nil
			}
		}
	} else if cfgID, current, ok := p.configID(sdk.SessionConfigOptionCategoryMode); ok {
		// Some agents expose modes as a config option instead.
		for _, id := range want {
			for _, o := range p.selectOptions(sdk.SessionConfigOptionCategoryMode) {
				if !strings.EqualFold(string(o.Value), id) {
					continue
				}
				if o.Value == current {
					return nil
				}
				if err := a.setOption(ctx, p, sid, cfgID, o.Value); err != nil {
					return fmt.Errorf("switch to %s mode: %w", mode, err)
				}
				return nil
			}
		}
	}
	if mode == agent.ModePlan {
		return errors.New("this agent has no plan mode; approvals still apply")
	}
	return nil
}

func (a *Agent) applyModel(ctx context.Context, p *proc, sid, model string) error {
	if model == "" {
		return nil
	}
	cfgID, current, ok := p.configID(sdk.SessionConfigOptionCategoryModel)
	if !ok {
		return fmt.Errorf("this agent doesn't let clients choose a model; set it in the agent's own config")
	}
	for _, o := range p.selectOptions(sdk.SessionConfigOptionCategoryModel) {
		if strings.EqualFold(string(o.Value), model) || strings.EqualFold(o.Name, model) {
			if o.Value != current {
				return a.setOption(ctx, p, sid, cfgID, o.Value)
			}
			return nil
		}
	}
	return fmt.Errorf("model %q isn't offered by this agent; pick one with /model", model)
}

func (a *Agent) setOption(ctx context.Context, p *proc, sid string, id sdk.SessionConfigId, v sdk.SessionConfigValueId) error {
	resp, err := p.conn.SetSessionConfigOption(ctx, sdk.SetSessionConfigOptionRequest{ValueId: &sdk.SetSessionConfigOptionValueId{
		SessionId: sdk.SessionId(sid), ConfigId: id, Value: v,
	}})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.options = resp.ConfigOptions
	p.mu.Unlock()
	return nil
}
