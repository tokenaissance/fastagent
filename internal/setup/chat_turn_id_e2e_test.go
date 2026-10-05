package setup

import (
	"strings"
	"testing"
)

// O1 (docs/mcp-task-submission.md §11): a submitted turn is ADDRESSABLE after it
// is over. The entry the web UI reads has to carry the identity that makes it so
// — and that identity is the SERVER's, not the caller's.
//
// History, kept because this file used to assert the opposite: 40d0d0a stored
// "the turn id the client minted" on the user message. The turn-identity work
// after it (docs/fastagent/design/14-turn-identity.md §2, I1/I2;
// internal/agent/turn_id.go) made the identity server-minted at acceptance —
// a cron tick or a goal continuation has no client to mint one — and demoted the
// caller's string to a DEDUPE key (`chatRequest.TurnID` is only its legacy
// spelling). So the stored field is `t_…` even when the POST carried a `turnId`,
// and the caller's string must never appear there: it names a retry, not a turn.
//
// Witnesses (both directions): submit with the legacy client string and without
// it, read the history the web UI reads, and the user entry carries a
// server-minted identity either way; the client string is not it.
// Falsification (run): drop the `metadata["turnId"]` write in buildUserMessage —
// both tests go red on the missing key.
func TestStoredUserMessageCarriesTheServersTurnIdentityE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)

	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID:   "agt_e2e",
		SessionID: "chat-turn-id",
		Message:   "address me later",
		TurnID:    "turn-e2e-0001",
	})
	s.handleChatStream(rec, req) // returns when the stream ends

	user := firstUserEntry(t, ag.WebChatHistory("chat-turn-id", false))
	got, _ := user["turnId"].(string)
	if !strings.HasPrefix(got, "t_") {
		t.Fatalf("user entry turnId = %q, want a server-minted t_… identity (entry: %#v)", got, user)
	}
	if got == "turn-e2e-0001" {
		t.Fatalf("the caller's dedupe key was stored as the identity; the design says the caller's string is never the identity (entry: %#v)", user)
	}
}

// The same submission WITHOUT the legacy string still gets an identity — that is
// the point of minting at acceptance: every turn is addressable, including the
// ones nobody's client asked for.
func TestStoredUserMessageCarriesAnIdentityWithoutAClientStringE2E(t *testing.T) {
	s, ag, _ := newQueuedChatHarness(t)

	rec := newSSERecorder()
	req := chatStreamRequest(t, chatRequest{
		AgentID:   "agt_e2e",
		SessionID: "chat-no-turn-id",
		Message:   "no client string here",
	})
	s.handleChatStream(rec, req)

	user := firstUserEntry(t, ag.WebChatHistory("chat-no-turn-id", false))
	got, _ := user["turnId"].(string)
	if !strings.HasPrefix(got, "t_") {
		t.Fatalf("user entry turnId = %q, want a server-minted t_… identity (entry: %#v)", got, user)
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
