package agent

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// queueStore is the durable wait list when the composition root wired one.
// Nil keeps the legacy in-process wait for CLI and tests.
func (a *Agent) queueStore() store.SessionQueueStore {
	if a == nil || a.dataStore == nil {
		return nil
	}
	qs, _ := a.dataStore.(store.SessionQueueStore)
	return qs
}

// queueEntry returns this turn's durable submission, if it was accepted by the
// dashboard handler. Non-web turns have no entry and keep the old path.
func (a *Agent) queueEntry(ctx context.Context) (store.SessionQueueStore, *store.SessionQueueEntry) {
	qs := a.queueStore()
	if qs == nil {
		return nil, nil
	}
	id := TurnIDFromContext(ctx)
	if id == "" {
		return qs, nil
	}
	entry, err := qs.GetSessionQueue(ctx, id)
	if err != nil {
		return qs, nil
	}
	return qs, entry
}

// queueCanceled reports a withdrawal that arrived on another pod. The waiter
// polls this on every retry, so cancellation no longer depends on the request
// reaching the pod that owns the goroutine.
func (a *Agent) queueCanceled(ctx context.Context) bool {
	qs, entry := a.queueEntry(ctx)
	return qs != nil && entry != nil && entry.State == store.SessionQueueCanceled
}

// queuePosition is the 1-based place of this submission among still-queued
// entries, counted from the store. It replaces the per-pod waiter count: two
// pods no longer report "position 1" for different submissions.
func (a *Agent) queuePosition(ctx context.Context) (int, bool) {
	qs, entry := a.queueEntry(ctx)
	if qs == nil || entry == nil {
		return 0, false
	}
	list, err := qs.ListSessionQueue(ctx, entry.UserID, entry.AgentID, entry.SessionID)
	if err != nil {
		return 0, false
	}
	position := 0
	for _, e := range list {
		if e.State != store.SessionQueueQueued {
			continue
		}
		position++
		if e.ID == entry.ID {
			return position, true
		}
	}
	return 0, false
}

// queueHeadWait reports whether this submission must wait for an earlier
// ticket. The store's seq is the FIFO order; the lease is only exclusion.
func (a *Agent) queueHeadWait(ctx context.Context) bool {
	qs, entry := a.queueEntry(ctx)
	if qs == nil || entry == nil || entry.State != store.SessionQueueQueued {
		return false
	}
	list, err := qs.ListSessionQueue(ctx, entry.UserID, entry.AgentID, entry.SessionID)
	if err != nil {
		return false
	}
	for _, e := range list {
		if e.State != store.SessionQueueQueued {
			continue
		}
		return e.ID != entry.ID
	}
	return false
}

func (a *Agent) queueMarkStarted(ctx context.Context) {
	qs, entry := a.queueEntry(ctx)
	if qs == nil || entry == nil {
		return
	}
	_ = qs.MarkSessionQueueStarted(ctx, entry.ID)
}
