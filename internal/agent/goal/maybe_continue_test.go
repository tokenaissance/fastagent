package goal

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// fakeRoutedStore is a sequential Store stub keyed on the
// (agentID, sessionKey) the maybeContinue path uses. Separate from
// the framework-test fakeStore so this file owns its happy-path
// behavior without test cross-talk.
type fakeRoutedStore struct {
	mu   sync.Mutex
	rows map[string]*Goal // key = agentID + "|" + sessionKey
}

func newFakeRoutedStore() *fakeRoutedStore        { return &fakeRoutedStore{rows: map[string]*Goal{}} }
func (s *fakeRoutedStore) key(a, k string) string { return a + "|" + k }
func (s *fakeRoutedStore) put(g *Goal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *g
	s.rows[s.key(g.AgentID, g.SessionKey)] = &clone
}
func (s *fakeRoutedStore) CreateGoal(_ context.Context, g *Goal) error {
	s.put(g)
	return nil
}
func (s *fakeRoutedStore) GetGoalBySession(_ context.Context, agentID, sessionKey string) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.rows[s.key(agentID, sessionKey)]; ok {
		clone := *g
		return &clone, nil
	}
	return nil, ErrNotFound
}
func (s *fakeRoutedStore) GetGoalByID(context.Context, string) (*Goal, error) {
	return nil, ErrNotFound
}
func (s *fakeRoutedStore) UpdateGoal(_ context.Context, g *Goal) error { s.put(g); return nil }
func (s *fakeRoutedStore) UpdateObjective(context.Context, string, string) error {
	return nil
}
func (s *fakeRoutedStore) DeleteGoal(context.Context, string) error { return nil }
func (s *fakeRoutedStore) ListGoalsByOwner(context.Context, string, int) ([]*Goal, error) {
	return nil, nil
}

func newActiveRoutedGoal() *Goal {
	return &Goal{
		ID:          "g-1",
		AgentID:     "agent-A",
		SessionKey:  "s-1",
		OwnerUserID: "user-1",
		Channel:     "web",
		ChatID:      "chat-1",
		ProjectID:   "proj-X",
		Objective:   "translate README",
		Status:      StatusActive,
	}
}

func TestMaybeContinueActiveGoalPublishes(t *testing.T) {
	st := newFakeRoutedStore()
	_ = st.CreateGoal(context.Background(), newActiveRoutedGoal())
	mb := bus.New()
	gr := NewGoalRuntime("s-1", "agent-A", "user-1", st, mb)

	gr.maybeContinue(context.Background())

	select {
	case msg := <-mb.Inbound:
		if msg.Source != bus.SourceGoalContinuation {
			t.Errorf("Source = %q, want goal_continuation", msg.Source)
		}
		if !strings.Contains(msg.Text, "<goal_context>") || !strings.Contains(msg.Text, "translate README") {
			t.Errorf("continuation prompt didn't wrap the objective:\n%s", msg.Text)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("active goal didn't publish a continuation")
	}
}

func TestMaybeContinueSkipsWhenNoGoal(t *testing.T) {
	mb := bus.New()
	gr := NewGoalRuntime("s-1", "agent-A", "user-1", newFakeRoutedStore(), mb)
	gr.maybeContinue(context.Background())
	select {
	case msg := <-mb.Inbound:
		t.Fatalf("no goal should produce no publish, got %+v", msg)
	default:
	}
}

// TestMaybeContinueSkipsNonActive: paused / complete / budget_limited
// must NOT keep publishing continuations. The token-accounting hook
// handles the budget_limited transition exactly once on the edge;
// any later trigger sees the new status and bows out here.
func TestMaybeContinueSkipsNonActive(t *testing.T) {
	for _, status := range []Status{StatusPaused, StatusComplete, StatusBudgetLimited} {
		st := newFakeRoutedStore()
		g := newActiveRoutedGoal()
		g.Status = status
		_ = st.CreateGoal(context.Background(), g)
		mb := bus.New()
		gr := NewGoalRuntime("s-1", "agent-A", "user-1", st, mb)
		gr.maybeContinue(context.Background())
		select {
		case <-mb.Inbound:
			t.Errorf("status=%s should not publish a continuation", status)
		default:
		}
	}
}

func TestMaybeContinueSkipsWhenNoRouting(t *testing.T) {
	st := newFakeRoutedStore()
	g := newActiveRoutedGoal()
	g.Channel = ""
	g.ChatID = ""
	_ = st.CreateGoal(context.Background(), g)
	mb := bus.New()
	gr := NewGoalRuntime("s-1", "agent-A", "user-1", st, mb)
	gr.maybeContinue(context.Background())
	select {
	case <-mb.Inbound:
		t.Fatal("a goal without routing info must not produce a malformed publish")
	default:
	}
}

// TestMaybeContinueLockCollapsesBursts: two near-simultaneous calls
// must not both publish (the continuationLock try-acquire collapses
// the second one). Otherwise PostTurn + AfterToolCall firing back
// to back would inject duplicate prompts.
func TestMaybeContinueLockCollapsesBursts(t *testing.T) {
	st := newFakeRoutedStore()
	_ = st.CreateGoal(context.Background(), newActiveRoutedGoal())
	mb := bus.New()
	gr := NewGoalRuntime("s-1", "agent-A", "user-1", st, mb)

	// Pre-acquire the lock to simulate a slow first call; the second
	// one should hit the default branch and exit without publishing.
	gr.continuationLock <- struct{}{}
	gr.maybeContinue(context.Background())
	<-gr.continuationLock // release for cleanup

	select {
	case <-mb.Inbound:
		t.Fatal("burst should have been collapsed by continuationLock")
	default:
	}
}
