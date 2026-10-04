package acp

import (
	"context"
	"slices"
	"time"

	sdk "github.com/coder/acp-go-sdk"

	"github.com/c0sm0thecoder/pocketagent/internal/agent"
)

var _ agent.SessionLister = (*Agent)(nil)

// Sessions lists the agent's sessions in cwd through session/list, for
// agents that support it; others return none.
func (a *Agent) Sessions(ctx context.Context, conv, cwd string) ([]agent.SessionInfo, error) {
	p, err := a.proc(ctx, conv, cwd)
	if err != nil {
		return nil, err
	}
	if p.init.AgentCapabilities.SessionCapabilities.List == nil {
		return nil, nil
	}
	resp, err := p.conn.ListSessions(ctx, sdk.ListSessionsRequest{Cwd: &cwd})
	if err != nil {
		return nil, a.explain("list sessions", err, p)
	}
	out := make([]agent.SessionInfo, 0, len(resp.Sessions))
	for _, s := range resp.Sessions {
		info := agent.SessionInfo{ID: string(s.SessionId)}
		if s.Title != nil {
			info.Title = *s.Title
		}
		if s.UpdatedAt != nil {
			info.Updated, _ = time.Parse(time.RFC3339, *s.UpdatedAt)
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(x, y agent.SessionInfo) int { return y.Updated.Compare(x.Updated) })
	return out, nil
}
