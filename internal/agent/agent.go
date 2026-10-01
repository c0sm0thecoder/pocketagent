// Package agent defines the interface every coding agent backend implements.
package agent

import "context"

// Mode is the permission preset for a conversation.
type Mode string

const (
	ModeAsk   Mode = "ask"   // ask before anything not in auto_allow
	ModeEdits Mode = "edits" // file edits allowed, commands still ask
	ModePlan  Mode = "plan"  // read-only planning, if the agent supports it
	ModeYolo  Mode = "yolo"  // allow everything
)

// Kind classifies a tool call. The values match ACP's ToolKind.
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

// MCPServer is an HTTP MCP server the agent should connect to. pocketagent
// uses it to give agents a send_file tool (and, for Claude, approvals).
type MCPServer struct {
	Name    string
	URL     string
	Headers map[string]string
}

type Request struct {
	Conv        string // conversation key; stable per chat/topic
	SessionID   string // resume this session if set
	Cwd         string
	Model       string
	Mode        Mode
	Prompt      []Block
	AlwaysAllow []string // tool keys the user chose "always allow" for
	MCP         *MCPServer
}

type OptionKind string

const (
	AllowOnce    OptionKind = "allow_once"
	AllowAlways  OptionKind = "allow_always"
	RejectOnce   OptionKind = "reject_once"
	RejectAlways OptionKind = "reject_always"
)

func (k OptionKind) Allows() bool { return k == AllowOnce || k == AllowAlways }

type Option struct {
	ID    string
	Label string
	Kind  OptionKind
}

// Permission is a request to run a tool call.
type Permission struct {
	Tool    string // display name, e.g. "Bash"
	Key     string // "always allow" memory key; empty when the agent remembers itself
	Kind    Kind
	Title   string
	Detail  string // plain text: the command, diff or arguments
	Options []Option
}

// DefaultOptions is used when an agent does not offer its own choices.
func DefaultOptions() []Option {
	return []Option{
		{ID: "allow", Label: "Allow", Kind: AllowOnce},
		{ID: "always", Label: "Always allow", Kind: AllowAlways},
		{ID: "deny", Label: "Deny", Kind: RejectOnce},
	}
}

// Decision answers a Permission. Message is the user's reason for denying,
// if they typed one.
type Decision struct {
	OptionID string
	Message  string
}

// Handler receives what the agent does during a turn.
type Handler interface {
	Session(id string)
	Text(text string) // a complete block of assistant text (Markdown)
	Tool(title string, kind Kind)
	Permission(ctx context.Context, p Permission) Decision
}

type Result struct {
	SessionID      string
	CostUSD        float64 // cost of this turn, if the agent reports it
	SessionCostUSD float64 // cumulative session cost, for agents that report that instead
	StopReason     string
	FinalText      string // the last reply, if not already sent via Handler.Text
}

type Caps struct {
	Images      bool // accepts image blocks
	Resume      bool // can continue a previous session
	DenyMessage bool // a deny can carry the user's reason back to the agent
	Modes       []Mode
}

type Agent interface {
	Caps() Caps
	// Models lists models to offer in the picker for this conversation.
	Models(conv string) []string
	// Run executes one user turn. Cancelling ctx stops it.
	Run(ctx context.Context, req Request, h Handler) (Result, error)
	// Close stops any background processes.
	Close()
}
