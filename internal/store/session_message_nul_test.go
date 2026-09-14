package store

import (
	"context"
	"strings"
	"testing"
)

// Tool output can carry NUL bytes: sandbox exec streams frame their output
// with "\x00\x00\x00\x00 {\"event\":…}" markers, and a real production turn
// archived exactly that. Postgres text cannot hold U+0000, so the insert
// failed with `invalid byte sequence for encoding "UTF8": 0x00` and the row
// was silently missing from the archive while the working set (JSON-escaped)
// kept it — history rendered differently from what the model had seen.
//
// The contract pinned here: whatever a tool returns, AppendSessionMessage
// stores a row (never errors on NUL) and ListSessionMessages reads it back
// NUL-free. This holds for both dialects, so the assertion is on the stored
// value rather than on an error string.
func TestAppendSessionMessageStripsNUL(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	uid, agent, key := "user-nul", "agent-nul", "s-nul"
	raw := "\x00\x00\x00\x00 {\"event\":{\"start\":{\"pid\":1608}}}\ne2b exec body read: context canceled"

	if err := db.AppendSessionMessage(ctx, uid, agent, key, SessionMessage{
		Role:       "tool",
		Content:    raw,
		ToolCallID: "call_nul",
		Name:       "exec",
		Thinking:   "thought\x00with\x00nuls",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := db.ListSessionMessages(ctx, uid, agent, key)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d; want 1", len(got))
	}
	if strings.ContainsRune(got[0].Content, 0) || strings.ContainsRune(got[0].Thinking, 0) {
		t.Fatalf("stored row still carries NUL: content=%q thinking=%q", got[0].Content, got[0].Thinking)
	}
	// Everything else survives byte for byte — this is a sanitisation, not a
	// truncation.
	want := " {\"event\":{\"start\":{\"pid\":1608}}}\ne2b exec body read: context canceled"
	if got[0].Content != want {
		t.Fatalf("content = %q; want %q", got[0].Content, want)
	}
	if got[0].ToolCallID != "call_nul" || got[0].Name != "exec" {
		t.Fatalf("row identity changed: %+v", got[0])
	}
}
