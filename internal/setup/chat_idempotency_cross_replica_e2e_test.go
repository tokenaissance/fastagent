package setup

import (
	"strings"
	"testing"
	"time"
)

// A retry of run_task that lands on the other replica must not enqueue a second
// instruction. The idempotency window lives in the store, so the winner is
// visible to the pod that did not accept the first POST.
func TestChatIdempotencyKeyDuplicateOnTheOtherReplicaE2E(t *testing.T) {
	prov := &toolScriptProvider{toolName: "slow_probe"}
	podA, podB, agA, agB, _ := newReplicaPairWithLease(t, prov, 4)
	started, release := registerBlockingTool(t, agA, "slow_probe")
	registerInstantTool(t, agB, "slow_probe")

	const session = "chat-idem-xreplica"
	recA := newSSERecorder()
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		podA.handleChatStream(recA, chatStreamRequest(t, chatRequest{
			AgentID: "agt_e2e", SessionID: session, Message: "one instruction",
			IdempotencyKey: "same-key",
		}))
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("replica A's turn never reached the tool")
	}

	recB := newSSERecorder()
	podB.handleChatStream(recB, chatStreamRequest(t, chatRequest{
		AgentID: "agt_e2e", SessionID: session, Message: "one instruction",
		IdempotencyKey: "same-key",
	}))
	body := recB.snapshot()
	if !strings.Contains(body, `"type":"duplicate"`) {
		t.Fatalf("retry on B did not answer duplicate: %q", body)
	}
	if strings.Contains(body, "content_delta") {
		t.Fatalf("retry on B started a turn: %q", body)
	}

	release()
	select {
	case <-doneA:
	case <-time.After(15 * time.Second):
		t.Fatal("replica A's turn did not finish")
	}

	users := 0
	for _, m := range agA.WebChatHistory(session, false) {
		if m["role"] == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("user messages = %d; want 1 (the retry must not create a second instruction)", users)
	}
}
