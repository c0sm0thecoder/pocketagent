package core

import (
	"context"
	"errors"

	"github.com/c0sm0thecoder/pocketagent/internal/store"
)

var errNoCheckpoints = errors.New("checkpoints are off")

// Diff shows changes since the last checkpoint, or since HEAD when all is true.
func (c *Core) Diff(ctx context.Context, conv ConvID, all bool) (string, error) {
	if c.Checkpointer == nil {
		return "", errNoCheckpoints
	}
	dir, from := c.Settings(conv).Cwd, "HEAD"
	if !all {
		cps := c.Store.Get(string(conv)).Checkpoints
		if len(cps) == 0 {
			return "", errors.New("no checkpoint yet; use /diff all to compare with HEAD")
		}
		cp := cps[len(cps)-1]
		dir, from = cp.Cwd, cp.Commit
	}
	return c.Checkpointer.Diff(ctx, dir, from)
}

// Undo restores the working tree to the state before the last turn.
func (c *Core) Undo(ctx context.Context, conv ConvID) (store.Checkpoint, error) {
	if c.Checkpointer == nil {
		return store.Checkpoint{}, errNoCheckpoints
	}
	if running, _ := c.Running(conv); running {
		return store.Checkpoint{}, errors.New("wait for the current run to finish (or /stop it)")
	}
	cps := c.Store.Get(string(conv)).Checkpoints
	if len(cps) == 0 {
		return store.Checkpoint{}, errors.New("nothing to undo")
	}
	cp := cps[len(cps)-1]
	if err := c.Checkpointer.Restore(ctx, cp.Cwd, cp.Commit); err != nil {
		return cp, err
	}
	c.update(conv, func(s *store.Conversation) {
		if n := len(s.Checkpoints); n > 0 {
			s.Checkpoints = s.Checkpoints[:n-1]
		}
	})
	return cp, nil
}
