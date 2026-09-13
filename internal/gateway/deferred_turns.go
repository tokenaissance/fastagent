package gateway

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// deferredTurn is an automatic turn-start request (cron tick, goal
// continuation, heartbeat, subagent delivery) that lost the race to a turn
// already running on the same session.
type deferredTurn struct {
	agentID   string
	accountID string
	msg       bus.InboundMessage
	parkedAt  time.Time
}

// deferredTurns parks automatic turns until the target session is idle.
//
// Why park instead of blocking the task-queue worker: the agent's turn gate
// serialises writers per session, so a worker that simply waited would sit
// inside its own turn budget (300 s by default) doing nothing, and the tick
// would be lost when that budget expired. Parking hands the worker back
// immediately; the drain loop submits the message once the session looks
// idle, and if a turn slipped in meanwhile the agent refuses it again
// (StartIfIdle -> ErrTurnNotAdmitted) and processTask re-parks it. Nothing is
// written to a session while its message is parked.
type deferredTurns struct {
	mu     sync.Mutex
	items  map[string][]*deferredTurn
	submit func(agentID, chatKey string, msg bus.InboundMessage, accountID string)
	// busy reports whether the session this message targets currently has a
	// turn in flight. Injected so the drain can skip a still-busy session
	// instead of blindly re-submitting and re-parking on every tick.
	busy func(ctx context.Context, agentID string, msg bus.InboundMessage) bool
	now  func() time.Time
	// maxWait bounds how long automatic work may wait for a user turn before
	// it is dropped with a warning. Cron re-fires on its own schedule and goal
	// continuations re-fire on the next PostTurn, so dropping is recoverable;
	// holding a tick forever is not.
	maxWait time.Duration
	// retryEvery is the drain cadence.
	retryEvery time.Duration
}

func newDeferredTurns(
	submit func(agentID, chatKey string, msg bus.InboundMessage, accountID string),
	busy func(ctx context.Context, agentID string, msg bus.InboundMessage) bool,
) *deferredTurns {
	return &deferredTurns{
		items:      make(map[string][]*deferredTurn),
		submit:     submit,
		busy:       busy,
		now:        time.Now,
		maxWait:    5 * time.Minute,
		retryEvery: time.Second,
	}
}

func (d *deferredTurns) park(agentID, chatKey string, msg bus.InboundMessage, accountID string) {
	if d == nil || d.submit == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items[chatKey] = append(d.items[chatKey], &deferredTurn{
		agentID:   agentID,
		accountID: accountID,
		msg:       msg,
		parkedAt:  d.now(),
	})
	slog.Info("automatic turn parked until the session is idle",
		"agent", agentID, "channel", msg.Channel, "chat_id", msg.ChatID,
		"source", msg.Source, "parked", d.countLocked())
}

// drain submits each parked message whose session looks idle, in per-chat FIFO
// order (only the head of a chat's queue is eligible, so ordering is kept).
// Messages that waited past maxWait are dropped with a warning.
func (d *deferredTurns) drain(ctx context.Context) {
	if d == nil {
		return
	}
	type submitJob struct {
		chatKey string
		item    *deferredTurn
	}
	var ready []submitJob
	var expired int

	d.mu.Lock()
	// Sorted keys keep submission order deterministic across chats (map
	// iteration would otherwise shuffle independent conversations' turns).
	keys := make([]string, 0, len(d.items))
	for key := range d.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		queue := d.items[key]
		kept := queue[:0]
		for i, item := range queue {
			if d.now().Sub(item.parkedAt) > d.maxWait {
				expired++
				continue
			}
			// FIFO: a later turn for the same chat must not overtake the head.
			if i == 0 && (d.busy == nil || !d.busy(ctx, item.agentID, item.msg)) {
				ready = append(ready, submitJob{chatKey: key, item: item})
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(d.items, key)
		} else {
			d.items[key] = kept
		}
	}
	d.mu.Unlock()

	for _, job := range ready {
		d.submit(job.item.agentID, job.chatKey, job.item.msg, job.item.accountID)
	}
	if expired > 0 {
		slog.Warn("dropping parked automatic turns: session stayed busy past the wait budget",
			"count", expired, "budget", d.maxWait)
	}
}

func (d *deferredTurns) count() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.countLocked()
}

func (d *deferredTurns) countLocked() int {
	n := 0
	for _, queue := range d.items {
		n += len(queue)
	}
	return n
}

// run drains on a fixed cadence until the done channel closes.
func (d *deferredTurns) run(ctx context.Context) {
	if d == nil {
		return
	}
	ticker := time.NewTicker(d.retryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if d.count() == 0 {
				continue
			}
			d.drain(ctx)
		}
	}
}
