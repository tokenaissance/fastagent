package session

// The turn receipt is the durable home of "under what configuration did this
// conversation last run" (docs 10 §3.3/§4, G9).
//
// The property that matters is the ROUND TRIP, not the in-memory stamp: the
// reader of this fact is a DIFFERENT agent instance — the one a config change
// rebuilt — so a stamp that only exists in the writer's memory would be exactly
// the bug the receipt is there to close. This test therefore writes through one
// manager and reads through another.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestRunReceiptStampSurvivesAReload(t *testing.T) {
	db := newSessionE2EDB(t)
	defer db.Close()
	createSessionE2EUser(t, db, "u_runcfg", "user", "")

	const fp = "model=openai/gpt-5.5 prompt_mode=agent"
	mgr := NewManagerWithStoreForUser(t.TempDir(), NewStoreAdapter(db, "u_runcfg"), "u_runcfg", "agt_runcfg")
	s := mgr.Get("web", "acct", "chat-1", "")
	s.SetProviderModel("openai", "gpt-5.5")
	s.SetRunReceipt(fp)
	s.Append(provider.Message{Role: "user", Content: "hi", Timestamp: time.Now().UnixMilli()})
	s.Append(provider.Message{Role: "assistant", Content: "hello", Timestamp: time.Now().UnixMilli()})

	// A second manager over the same store: same shape as the rebuilt agent
	// reading a conversation it never wrote.
	reloaded := NewManagerWithStoreForUser(t.TempDir(), NewStoreAdapter(db, "u_runcfg"), "u_runcfg", "agt_runcfg")
	msgs := reloaded.GetByKey(s.Key()).GetMessages()
	if len(msgs) == 0 {
		t.Fatal("the reloaded conversation has no history at all")
	}

	var got string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			got = RunReceiptOf(msgs[i])
			break
		}
	}
	if got != fp {
		t.Fatalf("reloaded receipt carries %q; want %q — the stamp did not survive the store round trip", got, fp)
	}
	// The other half of the same receipt, so the two stay read together: which
	// LLM produced the reply is recorded on the same message.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			if msgs[i].Provider != "openai" || msgs[i].Model != "gpt-5.5" {
				t.Fatalf("assistant receipt carries provider/model %q/%q; want openai/gpt-5.5",
					msgs[i].Provider, msgs[i].Model)
			}
			break
		}
	}
	// User rows never carry it: the stamp is about the turn's configuration, and
	// stamping every row would make "not sampled" unrepresentable.
	for _, m := range msgs {
		if m.Role != "assistant" && RunReceiptOf(m) != "" {
			t.Fatalf("a %s row carries a run-receipt stamp: %q", m.Role, RunReceiptOf(m))
		}
	}
}

// No configuration bound to the session (skill/test harness shape, or a turn
// that ran before the stamp existed) ⇒ the receipt carries nothing, so the
// reader reports "not sampled" instead of some default that would be diffed
// against and reported as a change.
func TestRunReceiptIsAbsentWhenNothingWasBound(t *testing.T) {
	// A real file path so the storeless fallback does not print a persist error;
	// the property under test is the stamp, not the file.
	s := &Session{filePath: filepath.Join(t.TempDir(), "session.json")}
	s.Append(provider.Message{Role: "assistant", Content: "hello"})
	msgs := s.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("append did not record the message: %d", len(msgs))
	}
	if got := RunReceiptOf(msgs[0]); got != "" {
		t.Fatalf("an unbound session stamped %q; want nothing", got)
	}
}
