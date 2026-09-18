package gateway

import (
	"context"
	"errors"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// sandboxSignalStore is the durable carrier for the sandbox pool's deltas that
// are produced while nobody is reading — the idle-eviction sync.
//
// It reuses the scope-keyed configs_kv rows (the same store the mcp undo cursor
// keeps its per-session journal in), so a note survives a pod restart and is
// visible to whichever replica adopts the sandbox lease next. The pool used to
// keep these in an in-process map, which lost them in exactly those two cases
// (docs 09, G3).
type sandboxSignalStore struct{ st store.Store }

const (
	sandboxSignalKind  = "ws_signal"
	sandboxSignalScope = "agent"
)

// sandboxSignalName keys one row per scope. Project and session both take part:
// two sessions of the same agent must not read each other's notes.
func sandboxSignalName(projectID, sessionID string) string {
	return "note:" + projectID + ":" + sessionID
}

// AppendSignal accumulates the rendered sentence(s). Two evictions before the
// next turn are two facts, so this appends rather than replaces. The
// read-modify-write window is a single eviction: losing that race costs one
// duplicated sentence, never a missing one (a duplicate is the safe side of the
// trade, the same one the reconcile takes).
func (s sandboxSignalStore) AppendSignal(ctx context.Context, agentID, projectID, sessionID, text string) error {
	if s.st == nil || text == "" {
		return nil
	}
	name := sandboxSignalName(projectID, sessionID)
	prev, err := s.st.GetConfigValue(ctx, sandboxSignalKind, sandboxSignalScope, agentID, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.st.SetConfigValue(ctx, sandboxSignalKind, sandboxSignalScope, agentID, name,
		store.ConfigValue{Value: prev.Value + text})
}

// TakeSignals returns the accumulated text and clears it. The clear is what
// makes delivery exactly-once; when the delete fails the text is still returned,
// because delivering the same sentence twice is a smaller sin than dropping it.
func (s sandboxSignalStore) TakeSignals(ctx context.Context, agentID, projectID, sessionID string) (string, error) {
	if s.st == nil {
		return "", nil
	}
	name := sandboxSignalName(projectID, sessionID)
	v, err := s.st.GetConfigValue(ctx, sandboxSignalKind, sandboxSignalScope, agentID, name)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if v.Value == "" {
		return "", nil
	}
	if delErr := s.st.DeleteConfigValue(ctx, sandboxSignalKind, sandboxSignalScope, agentID, name); delErr != nil {
		return v.Value, delErr
	}
	return v.Value, nil
}
