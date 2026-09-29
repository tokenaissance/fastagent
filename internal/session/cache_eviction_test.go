package session

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// The session cache is a cache: the store (or the file) is authoritative, so
// dropping an idle entry must be unobservable — the next Get rebuilds it
// (serverless invariant S1: memory bounded by work in flight, not by history).
//
// Falsification: make evictIdleLocked drop entries unconditionally (ignore the
// idle window) and a caller's own session disappears mid-use; remove the sweep
// and the size assertion below fails.
func TestSessionCacheEvictsIdleEntriesAndRebuildsThem(t *testing.T) {
	m := NewManager(t.TempDir())

	// Fill past the bound: every session but the one we just asked for is idle.
	for i := 0; i < agentSessionCacheMaxSessions+8; i++ {
		s := m.Get("web", "", "chat-"+string(rune('a'+i%26))+string(rune('0'+i/26)), "")
		s.Append(provider.Message{Role: "user", Content: "hello"})
	}
	if got := len(m.sessions); got > agentSessionCacheMaxSessions+1 {
		t.Fatalf("cache holds %d sessions, want <= %d (+ the one in hand)", got, agentSessionCacheMaxSessions)
	}

	// Nothing observable changed: a dropped session rebuilds from its file.
	s := m.Get("web", "", "chat-a0", "")
	msgs := s.GetMessages()
	if len(msgs) == 0 || msgs[0].Content != "hello" {
		t.Fatalf("rebuilt session lost its history: %+v", msgs)
	}
}

// A session with work in flight is never dropped: its in-memory state (steer
// buffer, turn fence, waiter queue) is not rebuildable.
func TestSessionCacheKeepsSessionsWithWorkInFlight(t *testing.T) {
	m := NewManager(t.TempDir())
	busy := m.Get("web", "", "chat-busy", "")
	busy.turnActive = true // a turn holds the slot
	busy.lastTouched = time.Now().Add(-time.Hour)

	for i := 0; i < agentSessionCacheMaxSessions+8; i++ {
		key := "chat-fill-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		m.Get("web", "", key, "")
	}

	if _, stillThere := m.sessions[busy.Key()]; !stillThere {
		t.Fatal("a session with work in flight was evicted; its in-memory state is gone")
	}
}

// The budget is counted in SESSIONS, and that is a decision, not an accident:
// a heavy session costs the same one unit as a light one, so eviction is driven
// purely by how many entries the cache holds. This pins the unit — if someone
// later makes size decide eviction, this test says so out loud instead of the
// behaviour drifting silently.
//
// Falsification: gate the sweep on anything size-derived (bytes or lines) and
// the saturated count below drops below the budget.
func TestSessionCacheBudgetIsCountedInSessionsNotSize(t *testing.T) {
	m := NewManager(t.TempDir())

	// Deliberately heavy: every session carries a large tool-output-shaped body.
	// If the budget were size-derived, these would be evicted far earlier.
	body := strings.Repeat("x", 64*1024)
	for i := 0; i < agentSessionCacheMaxSessions+16; i++ {
		s := m.Get("web", "", fmt.Sprintf("heavy-%d", i), "")
		for j := 0; j < 20; j++ {
			s.Append(provider.Message{Role: "tool", Content: body})
		}
	}

	// Saturated at the budget (plus the entry the caller is holding), because
	// size plays no part in the decision.
	if got := len(m.sessions); got != agentSessionCacheMaxSessions {
		t.Fatalf("cache holds %d sessions, want exactly the session budget %d", got, agentSessionCacheMaxSessions)
	}
	// And the eviction is still a cache eviction: a dropped session rebuilds.
	s := m.Get("web", "", "heavy-0", "")
	if len(s.GetMessages()) != 20 {
		t.Fatalf("rebuilt session lost its history: %d messages", len(s.GetMessages()))
	}
}

// "A dropped entry is indistinguishable from one that was never dropped" has to
// hold on every pass, not just once — and it is what makes lowering the budget
// (say, to keep less history warm) a tuning decision instead of a correctness
// one. This exercises the sweep at the current budget.
//
// Falsification: stop subtracting evicted entries from the map's size — i.e.
// make the sweep evict nothing — and the budget assertion below fails.
func TestEvictionIsUnobservableAtTheOperatingBudget(t *testing.T) {
	m := NewManager(t.TempDir())
	sessions := agentSessionCacheMaxSessions + 8
	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("chat-%d", i)
		s := m.Get("web", "", key, "")
		s.Append(provider.Message{Role: "user", Content: key})
		s.Append(provider.Message{Role: "assistant", Content: "reply to " + key})
	}
	if got := len(m.sessions); got > agentSessionCacheMaxSessions {
		t.Fatalf("cache holds %d sessions, want <= the budget %d", got, agentSessionCacheMaxSessions)
	}

	for i := 0; i < sessions; i++ {
		key := fmt.Sprintf("chat-%d", i)
		msgs := m.Get("web", "", key, "").GetMessages()
		if len(msgs) != 2 {
			t.Fatalf("session %s came back with %d messages after eviction, want 2", key, len(msgs))
		}
		if msgs[0].Content != key || msgs[1].Content != "reply to "+key {
			t.Fatalf("session %s came back wrong after eviction: %+v", key, msgs)
		}
	}
}

// The footprint line is instrumentation for the session budget, so it has to
// report the same history the eviction path reasons about — including the undo
// snapshot, which is a second resident copy that the store cannot rebuild.
func TestSessionCacheFootprintCountsTheUndoSnapshot(t *testing.T) {
	m := NewManager(t.TempDir())
	s := m.Get("web", "", "chat-snap", "")
	for i := 0; i < 5; i++ {
		s.Append(provider.Message{Role: "user", Content: "hello"})
	}
	if got := m.cacheMessagesLocked(); got != 5 {
		t.Fatalf("footprint counts %d lines before a snapshot, want 5", got)
	}
	s.Snapshot() // /retry's restore point: process memory only
	if got := m.cacheMessagesLocked(); got != 10 {
		t.Fatalf("footprint counts %d lines with a snapshot, want 10 (the snapshot is a second resident copy)", got)
	}
}

// The idle window is a DROP rule, not only an ordering preference: an entry
// nobody has touched for sessionCacheMaxIdle goes even when both budgets are
// satisfied. Before this, a single warm conversation stayed resident for the
// life of the process merely because the cache was under budget — which is how a
// long-lived pod's footprint tracks the history it has served instead of the
// work in flight (serverless invariant S1).
//
// Falsification: turn the idle test back into an ordering preference (drop only
// while over budget) and this fails with "still resident".
func TestSessionCacheDropsEntriesPastTheIdleWindow(t *testing.T) {
	m := NewManager(t.TempDir())
	idle := m.Get("web", "", "chat-idle", "")
	idle.Append(provider.Message{Role: "user", Content: "hello"})
	// A second Get, so the idle one is not the entry the caller is holding: the sweeper never
	// drops `lastKey` (the caller is holding it right now), and that exemption is not what this
	// test is about.
	m.Get("web", "", "chat-other", "")

	// Both budgets satisfied, nothing in flight — the only reason to drop `chat-idle` is that it
	// has gone idle.
	idle.mu.Lock()
	idle.lastTouched = time.Now().Add(-(sessionCacheMaxIdle + time.Minute))
	idle.mu.Unlock()

	m.evictIdleLocked(time.Now())
	if _, still := m.sessions["chat-idle"]; still {
		t.Fatalf("an idle entry is still resident: %d session(s)", len(m.sessions))
	}

	// And it rebuilds: the store is authoritative, so dropping is unobservable.
	rebuilt := m.Get("web", "", "chat-idle", "")
	if msgs := rebuilt.GetMessages(); len(msgs) == 0 || msgs[0].Content != "hello" {
		t.Fatalf("rebuilt session lost its history: %+v", msgs)
	}
}

// A snapshot has a lifetime: it is a shallow copy, so its own cost is per line, but while it exists
// every message it replaced stays reachable. It used to go only on Undo or on cache eviction, so a
// single snapshot taken once and never undone could pin a long conversation's old history for the
// life of the process.
//
// Falsification: drop the expiry from HasSnapshot/Undo and both assertions below fail.
func TestSessionSnapshotExpiresAndStopsPinningHistory(t *testing.T) {
	m := NewManager(t.TempDir())
	s := m.Get("web", "", "chat-snap", "")
	s.Append(provider.Message{Role: "user", Content: "one"})
	s.Snapshot()
	if !s.HasSnapshot() {
		t.Fatal("a fresh snapshot is not reported")
	}

	s.mu.Lock()
	s.snapshotAt = time.Now().Add(-(sessionSnapshotMaxAge + time.Minute))
	s.mu.Unlock()

	if s.HasSnapshot() {
		t.Fatal("an expired snapshot is still reported")
	}
	if s.Undo() {
		t.Fatal("an expired snapshot was still applied")
	}
	if got := len(s.GetMessages()); got != 1 {
		t.Fatalf("history was disturbed by an expired snapshot: %d message(s)", got)
	}
}

// Dev, 2026-09-29: the largest session held 3708 lines / 32 MB — ~8.6 KB per line, because the
// weight is the payload. At that width the line budget (20_000) is ~180 MB, so it stops bounding
// memory exactly when it is needed. The byte budget is what catches this shape.
//
// Falsification: drop the byte term from the budget checks and this fails with "cache holds N
// sessions" — four 25 MiB sessions fit under every other budget.
func TestSessionCacheEvictsOnTheByteBudget(t *testing.T) {
	m := NewManager(t.TempDir())
	big := strings.Repeat("x", 25<<20) // 25 MiB of payload in ONE message
	for i := 0; i < 4; i++ {
		s := m.Get("web", "", fmt.Sprintf("chat-big-%d", i), "")
		s.Append(provider.Message{Role: "user", Content: big})
	}
	m.evictIdleLocked(time.Now())
	if got := len(m.sessions); got > 3 {
		t.Fatalf("cache holds %d sessions (~%d MiB of payload), want <= 3", got, got*25)
	}
	if bytes := m.cacheResidentBytes(); bytes > agentSessionCacheMaxBytes {
		t.Fatalf("estimate exceeds the byte budget: %d > %d", bytes, agentSessionCacheMaxBytes)
	}
}

// mapStore is the smallest SessionStore that is a real store: it keeps what it is given. That is the
// whole point — with no authoritative copy behind it, "the cache dropped it" and "it never existed"
// are the same observation, which is what made the first two attempts at the test below fail for the
// wrong reason.
type mapStore struct {
	mu   sync.Mutex
	msgs map[string][]provider.Message
}

func newMapStore() *mapStore { return &mapStore{msgs: map[string][]provider.Message{}} }
func (s *mapStore) k(agentID, sessionKey string) string {
	return agentID + "/" + sessionKey
}
func (s *mapStore) GetSession(_ context.Context, agentID, sessionKey string) ([]provider.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.msgs[s.k(agentID, sessionKey)]
	out := make([]provider.Message, len(held))
	copy(out, held)
	return out, nil
}
func (s *mapStore) SaveSession(_ context.Context, agentID, sessionKey string, messages []provider.Message, _ WriteScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	s.msgs[s.k(agentID, sessionKey)] = out
	return nil
}
func (s *mapStore) AppendMessage(_ context.Context, agentID, sessionKey string, msg provider.Message, _ WriteScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.k(agentID, sessionKey)
	s.msgs[key] = append(s.msgs[key], msg)
	return nil
}
func (s *mapStore) ListMessages(ctx context.Context, agentID, sessionKey string) ([]provider.Message, error) {
	return s.GetSession(ctx, agentID, sessionKey)
}
func (s *mapStore) ListWebSessions(context.Context, string) ([]WebSession, error) { return nil, nil }
func (s *mapStore) DeleteSession(_ context.Context, agentID, sessionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.msgs, s.k(agentID, sessionKey))
	return nil
}
func (s *mapStore) RenameSession(context.Context, string, string, string) error { return nil }
func (s *mapStore) MoveSession(context.Context, string, string, string) error   { return nil }
func (s *mapStore) ResolveActiveSessionKey(context.Context, string, string, string, string) (string, error) {
	return "", nil
}
func (s *mapStore) LookupSessionTriple(context.Context, string, string) (string, string, string, error) {
	return "", "", "", nil
}
func (s *mapStore) LookupSessionProject(context.Context, string, string) (string, error) {
	return "", nil
}

// One conversation above its share of the byte budget is dropped even when every other budget is
// satisfied. Dev, 2026-09-29: the largest session held 3708 lines / 32 MB — five times the 6.4 MiB
// share — which is the shape that let a single agent sit at ~190 MB: one session, under the session
// count, and its lines were not what made it heavy.
//
// Falsification: remove the `oversized` term from the drop loop and it fails with "stayed resident".
func TestSessionCacheDropsOneOversizedConversation(t *testing.T) {
	m := NewManagerWithStoreForUser(t.TempDir(), newMapStore(), "u_owner", "agent-1")
	big := strings.Repeat("x", 8<<20)
	for i := 0; i < 3; i++ {
		s := m.Get("web", "", "chat-huge", "")
		s.Append(provider.Message{Role: "user", Content: big})
	}
	m.Get("web", "", "chat-small", "") // so `chat-huge` is not the entry in hand

	m.evictIdleLocked(time.Now())
	if _, still := m.sessions["chat-huge"]; still {
		t.Fatalf("an oversized conversation stayed resident (%d sessions)", len(m.sessions))
	}

	// And it rebuilds: dropping is unobservable only because the store had it.
	back := m.Get("web", "", "chat-huge", "")
	if msgs := back.GetMessages(); len(msgs) == 0 {
		t.Fatalf("the rebuilt session is empty — the store did not have it")
	}
}
