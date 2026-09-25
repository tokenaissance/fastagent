package setup

import (
	"testing"
)

// O1 (docs/mcp-task-submission.md §11): a submitted turn is ADDRESSABLE after it
// is over. The id the client minted for the POST has to survive as data on the
// stored user message, because that is the only thing a later reader has: the
// pending-turn registry forgets the turn when it finishes, and history entries
// carry no id of their own.
//
// Witness: submit with `turnId`, read the history the web UI reads, and the user
// entry carries it back verbatim.
// Falsification (run): drop the `metadata["turnId"]` write in buildUserMessage —
// this test fails on the missing "turnId" key.
func TestSubmittedTurnIDIsStoredOnTheUserMessageE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)

	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID:   "agt_e2e",
		SessionID: "chat-turn-id",
		Message:   "address me later",
		TurnID:    "turn-e2e-0001",
	})
	s.handleChatStream(rec, req) // returns when the stream ends

	user := firstUserEntry(t, ag.WebChatHistory("chat-turn-id"))
	if got := user["turnId"]; got != "turn-e2e-0001" {
		t.Fatalf("user entry turnId = %v, want turn-e2e-0001 (entry: %#v)", got, user)
	}
}

// The same submission WITHOUT a turn id must not grow the field: "no id" and
// "id = empty string" have to look the same to every reader, or a caller would
// have to distinguish them.
func TestTurnWithoutAnIDAddsNoFieldE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)

	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID:   "agt_e2e",
		SessionID: "chat-no-turn-id",
		Message:   "no id here",
	})
	s.handleChatStream(rec, req)

	user := firstUserEntry(t, ag.WebChatHistory("chat-no-turn-id"))
	if _, present := user["turnId"]; present {
		t.Fatalf("user entry carries turnId=%v without one being supplied", user["turnId"])
	}
}

func firstUserEntry(t *testing.T, history []map[string]any) map[string]any {
	t.Helper()
	if len(history) == 0 {
		t.Fatal("history is empty — the turn produced no archived messages")
	}
	for _, entry := range history {
		if entry["role"] == "user" {
			return entry
		}
	}
	t.Fatalf("history has no user entry: %#v", history)
	return nil
}
