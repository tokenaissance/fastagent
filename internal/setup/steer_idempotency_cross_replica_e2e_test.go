package setup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
)

// A retried steer_task that lands on the other replica must not fold twice.
func TestSteerIdempotencyKeyDuplicateOnTheOtherReplicaE2E(t *testing.T) {
	prov := &toolScriptProvider{toolName: "slow_probe"}
	podA, podB, agA, _, _ := newReplicaPairWithLease(t, prov, 4)

	const session = "chat-steer-idem-xreplica"
	sess := agA.Sessions().Get("web", "", session, "")
	if sess == nil {
		t.Fatal("session not found")
	}
	sess.BeginTurn()
	defer sess.EndTurn()

	steer := func(s *Server) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/chat/steer",
			strings.NewReader(`{"agentId":"agt_e2e","sessionId":"`+session+`","message":"hurry up","idempotencyKey":"s-cross"}`))
		req.Header.Set("X-Fastagent-Client", "client-a")
		req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: "u_1", Role: "user", AuthMethod: "session"}))
		s.handleChatSteer(rec, req)
		return rec
	}
	first := steer(podA)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"buffered":true`) {
		t.Fatalf("first steer = %d %s; want buffered", first.Code, first.Body.String())
	}
	// The retry lands on the pod that does not hold the turn. It must answer
	// from the durable claim, not fold a second copy.
	second := steer(podB)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"duplicate":true`) {
		t.Fatalf("retry on B = %d %s; want duplicate", second.Code, second.Body.String())
	}
	if got := len(sess.DrainSteer()); got != 1 {
		t.Fatalf("buffered steers = %d; want 1", got)
	}
}
