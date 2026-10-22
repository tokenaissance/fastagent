package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
)

// The 202 endpoint accepts once and leaves the turn's output to the session
// subscription. A retry with the same key must answer duplicate and must not
// create a second queue row.
func TestChatSubmission202AndDuplicateE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)
	sess := ag.Sessions().Get("web", "", "chat-submit", "")
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}
	defer sess.ReleaseTurn()

	rec := newSSERecorder()
	s.handleChatSubmission(rec, chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: "chat-submit", Message: "queued once", TurnID: "key-1",
	}))
	if rec.statusCode() != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.statusCode(), rec.snapshot())
	}
	var accepted struct {
		TurnID string `json:"turnId"`
	}
	if err := json.Unmarshal([]byte(rec.snapshot()), &accepted); err != nil || accepted.TurnID == "" {
		t.Fatalf("accepted=%s err=%v", rec.snapshot(), err)
	}

	rec2 := newSSERecorder()
	s.handleChatSubmission(rec2, chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: "chat-submit", Message: "queued once", TurnID: "key-1",
	}))
	if rec2.statusCode() != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", rec2.statusCode(), rec2.snapshot())
	}
	var dup struct {
		TurnID    string `json:"turnId"`
		Duplicate bool   `json:"duplicate"`
	}
	if err := json.Unmarshal([]byte(rec2.snapshot()), &dup); err != nil || !dup.Duplicate || dup.TurnID != accepted.TurnID {
		t.Fatalf("duplicate=%s err=%v", rec2.snapshot(), err)
	}
	if _, withdrawn, err := s.withdrawEarliestPendingTurn(context.Background(), "u_1", "agt_e2e", "chat-submit"); err != nil || !withdrawn {
		t.Fatalf("withdraw queued submission: withdrawn=%v err=%v", withdrawn, err)
	}
}

func TestChatSubscribeSendsQueueSnapshotE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)
	sess := ag.Sessions().Get("web", "", "chat-snapshot", "")
	if !sess.AcquireTurn(context.Background()) {
		t.Fatal("could not take the turn slot for the test")
	}
	defer sess.ReleaseTurn()

	rec := newSSERecorder()
	s.handleChatSubmission(rec, chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: "chat-snapshot", Message: "snapshot me", TurnID: "key-s",
	}))
	if rec.statusCode() != http.StatusAccepted {
		t.Fatalf("submit status=%d body=%s", rec.statusCode(), rec.snapshot())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/subscribe?agentId=agt_e2e&sessionId=chat-snapshot&since=0", nil)
	req = req.WithContext(auth.WithIdentity(ctx, auth.Identity{UserID: "u_1", Role: "user", AuthMethod: "session"}))
	sub := newSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleChatSubscribe(sub, req)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !sub.seen("queue_snapshot") {
		if time.Now().After(deadline) {
			t.Fatalf("subscription never sent queue_snapshot; body=%q", sub.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sub.seen("snapshot me") {
		t.Fatalf("snapshot did not carry the queued text: %q", sub.snapshot())
	}
	if _, _, err := s.withdrawEarliestPendingTurn(context.Background(), "u_1", "agt_e2e", "chat-snapshot"); err != nil {
		t.Fatalf("withdraw queued submission: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not stop after cancel")
	}
}
