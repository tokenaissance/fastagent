package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
	// notify, when wired, delivers one sentence to the chat a dropped turn
	// belonged to. Only sources the USER created get it (cron): the harness's own
	// sources re-fire on their next cycle, so a user-facing line about them would
	// be noise (C3), while a scheduled task the user asked for and never got an
	// answer from is exactly the silent failure this whole audit is about
	// (docs 10 §3.5, G12).
	notify func(msg bus.InboundMessage, text string)
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
	notify func(msg bus.InboundMessage, text string),
) *deferredTurns {
	return &deferredTurns{
		items:      make(map[string][]*deferredTurn),
		submit:     submit,
		busy:       busy,
		notify:     notify,
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
	var expired []*deferredTurn

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
				expired = append(expired, item)
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
	// A drop is a fact about the agent's world and, for a scheduled task, about
	// the user's expectations. It used to leave only "count=2" in the log, which
	// cannot even be located afterwards (docs 10 §3.5, G12).
	for _, item := range expired {
		waited := d.now().Sub(item.parkedAt).Round(time.Second)
		slog.Warn("dropping a parked automatic turn: the session stayed busy past the wait budget",
			"agent", item.agentID, "channel", item.msg.Channel, "chat_id", item.msg.ChatID,
			"source", item.msg.Source, "budget", d.maxWait, "waited", waited,
			"text", firstLine(item.msg.Text))
		if item.msg.Source == bus.SourceCron && d.notify != nil {
			d.notify(item.msg, droppedCronNote(item.msg.Text, d.maxWait))
		}
	}
}

// firstLine bounds how much of a dropped turn's text reaches the log: enough to
// identify it, not a copy of the prompt.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 120 {
		text = text[:120] + "…"
	}
	return text
}

// droppedCronNote is the sentence a user gets when the scheduled task they set
// up never ran. The user is the one holding the belief "it will run at 9", so
// the note goes out through the channel they are already in — without starting
// a turn (docs 10 §3.5, G12).
func droppedCronNote(text string, budget time.Duration) string {
	subject := "A scheduled task"
	if name := cronJobName(text); name != "" {
		subject = fmt.Sprintf("The scheduled task %q", name)
	}
	return fmt.Sprintf("[scheduled task did not run] %s was due, but this conversation stayed busy for over %s, "+
		"so its turn was dropped. Set it up again if you still need it.", subject, budget)
}

// cronJobName reads the name out of the scheduler's trigger text
// ("[Cron Job: <name>] This is a scheduled task trigger."). Empty when the text
// does not carry a name, so the sentence never renders a dangling quote.
func cronJobName(text string) string {
	const prefix = "[Cron Job: "
	if !strings.HasPrefix(text, prefix) {
		return ""
	}
	rest := text[len(prefix):]
	end := strings.IndexByte(rest, ']')
	if end <= 0 {
		return ""
	}
	return rest[:end]
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
