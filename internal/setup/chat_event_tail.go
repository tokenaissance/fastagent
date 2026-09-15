package setup

import (
	"context"
	"log/slog"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// liveOnlyEventTypes are the events the hub carries but the log does not keep.
//
// content_delta streams roughly one row per generated token; persisting it would
// dwarf session_events for no replay value (the trailing `content` event carries
// the full text). It reaches the tab that started the turn through that tab's
// own POST stream, so the subscribe path must not forward a second copy — hence
// a shared predicate instead of the single `== "content_delta"` comparison that
// used to live in the hub branch. The guard also covers the tail: if a live-only
// type ever does reach the table, it still must not be fanned out.
var liveOnlyEventTypes = map[string]bool{
	"content_delta": true,
}

func isLiveOnlyEventType(t string) bool { return liveOnlyEventTypes[t] }

// chatEventTailInterval is how often an open SSE subscription asks the store for
// events it has not delivered yet.
//
// The query is an indexed range scan (idx_session_events_lookup on
// user_id, agent_id, session_key, seq) that returns nothing while a session is
// idle, so a fixed interval beats any state machine on both simplicity and
// measurement. A state machine could not work here anyway: whether a turn is in
// flight lives in the memory of the pod running it — precisely the pod this
// subscription is not on.
const chatEventTailInterval = 500 * time.Millisecond

// tailSessionEvents reads the events this subscription has not sent yet.
//
// A failure is a Warn, never a torn-down stream: the subscriber degrades to
// same-pod (hub) delivery rather than losing the session. That degradation is
// the honest one — with the store unreachable there is nothing else to read.
func (s *Server) tailSessionEvents(ctx context.Context, uid, agentID, sessionID string, sinceSeq int64) []store.SessionEventRecord {
	if s.dataStore == nil {
		return nil
	}
	rows, err := s.dataStore.ListSessionEventsSince(ctx, uid, agentID, sessionID, sinceSeq)
	if err != nil {
		slog.Warn("session_events tail failed",
			"agent", agentID, "session", sessionID, "since", sinceSeq, "error", err)
		return nil
	}
	return rows
}
