package gateway

import (
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// The IM half of the product contract ("who queues and who steers",
// docs/session-turn-integrity.md): an IM message folds into the running turn
// while one is active, and falls back to the queue when the session is idle.
// Pinned here because a future change that routes IM through submitTask would
// silently turn every IM message into a queued turn — a product decision, not
// a refactor.
func TestTrySteerFoldsIntoRunningTurn(t *testing.T) {
	home := t.TempDir()
	// Store-backed sessions: the production shape. A file-backed manager mints
	// a fresh session key on every Get for a non-web triple, so "the session
	// the agent resolves" would not be "the session the test holds" — the very
	// drift a store-backed manager exists to prevent.
	db := newGatewayConfigStore(t)
	mgr, err := agent.NewManager([]config.ResolvedAgent{{
		ID: "agt_steer", UserID: "u_1", Home: home,
		Workspace: filepath.Join(home, "ws"), Model: "fake-model",
		MaxTokens: 64, Temperature: 0.7, MaxToolIterations: 1,
	}}, nil, bus.New(),
		agent.WithUserID("u_1"),
		agent.WithSessionStore(session.NewStoreAdapter(db, "u_1")),
	)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	ag := mgr.AgentByID("agt_steer")
	if ag == nil {
		t.Fatal("agent not registered")
	}
	g := &Gateway{}
	msg := bus.InboundMessage{Channel: "telegram", AccountID: "bot1", ChatID: "chat1", Text: "别用那个 API"}
	sess := ag.Sessions().Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	// Persist the row so later resolutions land on this same session (a
	// brand-new triple would mint a fresh key per call).
	sess.Append(provider.Message{Role: "user", Content: "earlier turn"})
	if again := ag.Sessions().Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID); again != sess {
		t.Fatal("session resolution is not stable; the test would not exercise the real steer path")
	}

	// Idle: steer refuses, and the caller queues the message instead.
	if g.trySteer(ag, msg, msg.Text) {
		t.Fatal("trySteer accepted a message with no turn in flight; the caller would never queue it")
	}

	// A turn in flight: the message is folded into it, not queued.
	sess.BeginTurn()
	if !g.trySteer(ag, msg, msg.Text) {
		t.Fatal("trySteer refused a message while a turn was in flight; IM would stop steering")
	}
	drained := sess.DrainSteer()
	if len(drained) != 1 || drained[0].Content != msg.Text {
		t.Fatalf("steer buffer = %+v; want the IM message handed to the running turn", drained)
	}
	sess.EndTurn()

	// Back to idle: refusal again, so the next message queues.
	if g.trySteer(ag, msg, msg.Text) {
		t.Fatal("trySteer accepted a message after the turn ended")
	}
}
