package session

import (
	"fmt"
	"strings"
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
