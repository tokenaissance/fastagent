package agent

// Where the environment signal's baseline comes from (docs 10 §4, G9 + G20):
// the conversation's OWN turn receipts, not this process's memory.
//
// The write half — Append stamping the document onto the assistant message, and
// the store round trip that must survive a reload — is pinned in
// internal/session/run_receipt_test.go. This file pins the read half and the
// properties that decide whether the signal can be trusted:
//
//	the last receipt wins            (it is the most recent world the
//	                                  conversation saw itself run in)
//	no receipt  ⇒ not sampled        (silence, never a default)
//	undecodable ⇒ not sampled        (same rule: a read that failed is not a
//	                                  change)
//	only receipts, never inference   (the provider/model columns next to it are
//	                                  not enough to reassemble a world)

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// stamped builds the assistant row session.Append produces: the receipt
// document in metadata, the model that produced it in the columns beside it.
func stamped(doc string) provider.Message {
	m := provider.Message{Role: "assistant", Content: "ok"}
	if doc != "" {
		m.Metadata = map[string]any{session.RunReceiptMetadataKey: doc}
	}
	return m
}

func receiptFor(s envSnapshot) string { return encodeEnvSnapshot(s) }

func TestEnvBaselineComesFromTheTurnReceipt(t *testing.T) {
	history := []provider.Message{
		{Role: "user", Content: "hi"},
		stamped(receiptFor(snapIdentity(snap(nil, nil, ""), nil, "model=small prompt_mode=chatbot"))),
		{Role: "tool", Content: "…"},
		stamped(receiptFor(snapIdentity(snap(nil, nil, ""), nil, "model=large prompt_mode=agent"))),
		{Role: "user", Content: "and now?"},
	}
	prev, seen := envBaselineFromReceipt(history)
	if !seen {
		t.Fatal("a history with receipts was reported as not sampled")
	}
	if prev.config != "model=large prompt_mode=agent" {
		t.Fatalf("baseline config = %q; want the LAST receipt", prev.config)
	}

	// Unknown / unusable histories all read as "not sampled".
	for name, h := range map[string][]provider.Message{
		"empty":               nil,
		"no assistant rows":   {{Role: "user", Content: "hi"}},
		"assistant, no stamp": {{Role: "assistant", Content: "no receipt"}},
		"undecodable receipt": {stamped("{not json")},
	} {
		if _, ok := envBaselineFromReceipt(h); ok {
			t.Fatalf("%s was treated as a usable baseline", name)
		}
	}

	// The provider/model columns record which LLM answered, not what the world
	// looked like. Reassembling a world from them would have to guess the rest,
	// and a guess that misses produces a fabricated change on every turn.
	modelOnly := []provider.Message{{Role: "assistant", Content: "ok", Provider: "openai", Model: "gpt-5.5"}}
	if _, ok := envBaselineFromReceipt(modelOnly); ok {
		t.Fatal("a model-only row was accepted as a world baseline")
	}
}

// The G9 shape, end to end on the read side: a rebuilt agent (no instance state
// whatsoever) plus the conversation's receipts ⇒ the change is stated.
func TestRebuiltAgentStatesTheChangeFromTheReceipt(t *testing.T) {
	before := receiptFor(snapIdentity(snap(nil, nil, ""), nil, "model=small prompt_mode=chatbot"))
	history := []provider.Message{{Role: "user", Content: "make me a site"}, stamped(before)}

	prev, seen := envBaselineFromReceipt(history)
	cur := snapIdentity(snap(nil, nil, ""), nil, "model=large prompt_mode=agent")
	sig := renderEnvDelta(prev, seen, cur)
	if !strings.Contains(sig, "my configuration changed") ||
		!strings.Contains(sig, "model=small") || !strings.Contains(sig, "model=large") {
		t.Fatalf("the rebuilt agent did not state the change from its own receipts:\n%s", sig)
	}

	// The same conversation with a matching receipt says nothing (C3).
	prev, seen = envBaselineFromReceipt([]provider.Message{stamped(receiptFor(cur))})
	if got := renderEnvDelta(prev, seen, cur); got != "" {
		t.Fatalf("an unchanged world produced a signal:\n%s", got)
	}
}

// The whole point of the receipt: a change to a family that used to be
// in-process-only (skills, tools, memory, identity, cron) is stated by an
// instance that never saw the old value either — the same property G9 got for
// configuration, now for all five (docs 10 §4, G20).
func TestReceiptCarriesEverySampledFamily(t *testing.T) {
	prev := envSnapshot{
		skills:     map[string]string{"alpha": "agent|a", "beta": "agent|b"},
		tools:      map[string]bool{"exec": true, "write_file": true},
		memoryHash: hashMemory("remember X"),
		identity:   map[string]string{"USER.md": hashMemory("Berlin")},
		cron:       map[string]string{"job1": "morning-brief|0 9 * * *|prompt|enabled=true"},
		config:     "model=small prompt_mode=agent",
	}
	cur := envSnapshot{
		skills:     map[string]string{"alpha": "agent|a"},
		tools:      map[string]bool{"exec": true},
		memoryHash: "",
		identity:   map[string]string{"USER.md": hashMemory("Lisbon")},
		cron:       map[string]string{},
		config:     "model=large prompt_mode=agent",
	}

	// Through the wire, exactly as the receipt travels.
	decoded, ok := envBaselineFromReceipt([]provider.Message{stamped(encodeEnvSnapshot(prev))})
	if !ok {
		t.Fatal("the receipt did not decode")
	}
	sig := renderEnvDelta(decoded, true, cur)
	for _, want := range []string{
		"skills removed: beta",
		"tools no longer available: write_file",
		"long-term memory was CLEARED",
		"identity files changed: USER.md",
		"scheduled jobs no longer exist: morning-brief",
		"my configuration changed",
	} {
		if !strings.Contains(sig, want) {
			t.Fatalf("the receipt-borne baseline lost %q:\n%s", want, sig)
		}
	}
}
