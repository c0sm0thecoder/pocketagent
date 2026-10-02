// Package core runs conversations: it chooses the agent, queues prompts,
// applies the permission policy, tracks cost and takes checkpoints.
//
// The core is independent of any chat platform and any agent vendor. It
// declares the small interfaces it needs (UI, Store, ToolHost,
// Checkpointer) and is wired to implementations by the caller.
package core

import (
	"context"
	"sync"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
	"github.com/c0sm0thecoder/pocketagent/internal/config"
	"github.com/c0sm0thecoder/pocketagent/internal/store"
	"github.com/c0sm0thecoder/pocketagent/internal/stt"
	"github.com/c0sm0thecoder/pocketagent/internal/tts"
)

// ConvID identifies a conversation. The frontend chooses the format (for
// example a chat plus a thread); the core treats it as opaque.
type ConvID string

// Input is one user message.
type Input struct {
	User   string // opaque user id, used for budgets
	Blocks []agent.Block
	Text   string // the text part, used for titles and "deny with reason"
}

// Messenger sends things to a conversation.
type Messenger interface {
	Reply(conv ConvID, markdown string) // agent output; long replies may become a file
	Notice(conv ConvID, text string)    // a short status line
	SendFile(ctx context.Context, conv ConvID, path, caption string) error
	SendVoice(ctx context.Context, conv ConvID, ogg []byte) error
}

// Progress shows what the agent is doing during one turn.
type Progress interface {
	ToolCall(title string)
	Flush() // called before agent text is sent, so tool lines stay above it
	Done(summary string)
}

// Approval is a permission request on screen.
type Approval interface {
	Resolve(outcome string)
}

// UI is implemented by the chat frontend.
type UI interface {
	Messenger
	StartProgress(conv ConvID) Progress
	// AskApproval presents p; the choice comes back via Core.Answer(id, optionID).
	AskApproval(conv ConvID, id string, p agent.Permission) Approval
}

// Store persists conversations and spend.
type Store interface {
	Get(key string) store.Conversation
	Update(key string, fn func(*store.Conversation)) error
	AddSpend(user string, usd float64) error
	Spend(user string) (today, month float64)
}

// ToolHost serves tools (send_file, approval hooks) to agent processes.
type ToolHost interface {
	Attach(conv string, h agent.Handler) (detach func())
	Server(agentName, conv string) *agent.ToolServer
}

// Checkpointer snapshots a working tree so a turn can be shown and undone.
type Checkpointer interface {
	Snapshot(ctx context.Context, dir string) (id string, err error)
	Diff(ctx context.Context, dir, from string) (string, error)
	Restore(ctx context.Context, dir, id string) error
}

// Deps are the core's collaborators. Transcriber, Speaker and Checkpointer
// may be nil to disable those features.
type Deps struct {
	Config       *config.Config
	Agents       map[string]agent.Agent
	Store        Store
	Tools        ToolHost
	Checkpointer Checkpointer
	Transcriber  stt.Transcriber
	Speaker      tts.Speaker
}

type Core struct {
	Deps
	ui UI

	mu        sync.Mutex
	runtimes  map[ConvID]*runtime
	approvals map[string]*pending
}

func New(d Deps) *Core {
	return &Core{Deps: d, runtimes: map[ConvID]*runtime{}, approvals: map[string]*pending{}}
}

// SetUI connects the frontend. It must be called before Submit.
func (c *Core) SetUI(ui UI) { c.ui = ui }
