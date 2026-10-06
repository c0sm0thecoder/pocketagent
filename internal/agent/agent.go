// Package agent defines the contract between pocketagent and a coding agent
// backend. Adapters live in subpackages and depend only on this package.
package agent

import (
	"context"
	"encoding/json"
	"time"
)

// Agent runs turns of a conversation with one coding agent.
//
// Optional behaviour is expressed as separate interfaces that an Agent may
// also implement: ModelLister, SessionLister, ResumeHinter, ToolProvider
// and io.Closer.
type Agent interface {
	// Caps reports what the agent supports, so callers can adapt instead of
	// failing (for example by saving images to files).
	Caps() Caps
	// Run executes one user turn. Cancelling ctx stops it.
	Run(ctx context.Context, req Request, h Handler) (Result, error)
}

// ModelLister is implemented by agents that can name models for a picker.
type ModelLister interface {
	Models(conv string) []string
}

// SessionLister is implemented by agents that can list their sessions in a
// folder, including ones started outside pocketagent (in a terminal), so
// they can be continued from chat.
type SessionLister interface {
	Sessions(ctx context.Context, conv, cwd string) ([]SessionInfo, error)
}

// SessionInfo describes a session an agent can resume.
type SessionInfo struct {
	ID      string
	Title   string
	Updated time.Time
}

// ResumeHinter is implemented by agents that can tell the user how to
// continue a session in their own terminal.
type ResumeHinter interface {
	ResumeCommand(cwd, sessionID string) string
}

// ToolProvider is implemented by agents that need extra tools on the tool
// server (for example a permission-prompt hook).
type ToolProvider interface {
	Tools() []Tool
}

// Tool is an MCP tool served to agents. Call runs with the Handler of the
// conversation whose agent invoked it.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema of the arguments
	Hints       ToolHints
	Call        func(ctx context.Context, h Handler, args json.RawMessage) (string, error)
}

// ToolHints describe a tool's behaviour so clients can decide what needs
// confirmation. They follow MCP's tool annotations and are always sent in
// full; Destructive and Idempotent only matter when ReadOnly is false.
type ToolHints struct {
	Title       string
	ReadOnly    bool // doesn't change its environment
	Destructive bool // may delete or overwrite things
	Idempotent  bool // repeating a call with the same arguments has no further effect
	OpenWorld   bool // reaches outside the machine (network, other people)
}

// Factory builds an Agent from its configuration.
type Factory func(Spec) (Agent, error)

// Spec is an agent's configuration, independent of the config file format.
type Spec struct {
	Name    string
	Command []string // the agent's executable and arguments
	Wrap    []string // prefix for the command, e.g. a container runner; supports {cwd}
	Env     []string // extra KEY=VALUE environment
	Options Options  // adapter-specific settings
}

// Options decodes adapter-specific settings into a struct, rejecting
// unknown keys.
type Options interface {
	Decode(v any) error
}

// NoOptions is an empty Options, for tests and adapters built in code.
type NoOptions struct{}

func (NoOptions) Decode(any) error { return nil }

// Mode is the permission preset for a conversation.
type Mode string

const (
	ModeAsk   Mode = "ask"   // ask before anything not auto-allowed
	ModeEdits Mode = "edits" // file edits allowed, commands still ask
	ModePlan  Mode = "plan"  // read-only planning, if the agent supports it
	ModeFull  Mode = "full"  // allow everything
)

// Modes lists every mode in order of increasing trust.
var Modes = []Mode{ModeAsk, ModeEdits, ModePlan, ModeFull}

// Kind classifies a tool call. The values follow the Agent Client Protocol.
type Kind string

const (
	KindRead    Kind = "read"
	KindEdit    Kind = "edit"
	KindDelete  Kind = "delete"
	KindMove    Kind = "move"
	KindSearch  Kind = "search"
	KindExecute Kind = "execute"
	KindThink   Kind = "think"
	KindFetch   Kind = "fetch"
	KindOther   Kind = "other"
)

// Block is one piece of the user's prompt: text or an image.
type Block struct {
	Text     string
	Image    []byte
	MimeType string
}

func (b Block) IsImage() bool { return len(b.Image) > 0 }

// ToolServer is an HTTP MCP server the agent should connect to for the
// duration of the turn.
type ToolServer struct {
	Name    string
	URL     string
	Headers map[string]string
}

type Request struct {
	Conv        string // stable conversation key
	SessionID   string // resume this session if set
	Cwd         string
	Model       string // empty means the agent's default
	Mode        Mode
	Prompt      []Block
	AlwaysAllow []string // permission keys the user chose "always allow" for
	Tools       *ToolServer
}

type OptionKind string

const (
	AllowOnce    OptionKind = "allow_once"
	AllowAlways  OptionKind = "allow_always"
	RejectOnce   OptionKind = "reject_once"
	RejectAlways OptionKind = "reject_always"
)

func (k OptionKind) Allows() bool { return k == AllowOnce || k == AllowAlways }

// Option is one choice offered for a Permission.
type Option struct {
	ID    string
	Label string
	Kind  OptionKind
}

// Permission asks the user whether a tool call may run.
type Permission struct {
	Tool    string // short display name, e.g. "Bash"
	Key     string // key for "always allow"; empty when the agent remembers itself
	Kind    Kind
	Title   string
	Detail  string // plain text: the command, diff or arguments
	Options []Option
}

// DefaultOptions is offered when an agent does not supply its own choices.
func DefaultOptions() []Option {
	return []Option{
		{ID: "allow", Label: "Allow", Kind: AllowOnce},
		{ID: "always", Label: "Always allow", Kind: AllowAlways},
		{ID: "deny", Label: "Deny", Kind: RejectOnce},
	}
}

// Find returns the option with the given id.
func (p Permission) Find(id string) (Option, bool) {
	for _, o := range p.Options {
		if o.ID == id {
			return o, true
		}
	}
	return Option{}, false
}

// First returns the first option for which match is true.
func (p Permission) First(match func(OptionKind) bool) (Option, bool) {
	for _, o := range p.Options {
		if match(o.Kind) {
			return o, true
		}
	}
	return Option{}, false
}

// Decision answers a Permission. An empty OptionID means cancelled. Message
// is the user's reason for denying, if they gave one.
type Decision struct {
	OptionID string
	Message  string
}

// Handler is the conversation as seen by the agent during a turn: it
// receives the agent's output and answers its requests.
type Handler interface {
	Session(id string)
	Message(markdown string) // a complete block of assistant text
	ToolCall(title string, kind Kind)
	Permission(ctx context.Context, p Permission) Decision
	SendFile(ctx context.Context, path, caption string) error
}

type Result struct {
	SessionID  string
	CostUSD    float64 // cost of this turn, if the agent reports it
	StopReason string
	Final      string // the reply, when it was not already sent through Handler.Message
}

type Caps struct {
	Images      bool   // accepts image blocks
	Resume      bool   // can continue a previous session
	DenyMessage bool   // a denial can carry the user's reason to the agent
	Modes       []Mode // modes the agent can honour
}

// SupportsMode reports whether m is in c.Modes.
func (c Caps) SupportsMode(m Mode) bool {
	for _, x := range c.Modes {
		if x == m {
			return true
		}
	}
	return false
}
