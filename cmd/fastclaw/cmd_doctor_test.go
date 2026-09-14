package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// The incident was diagnosed and repaired by hand with SQL. This pins the
// command that replaces that work, end to end: seed a stored session with a
// duplicated tool reply, let `doctor sessions` find it (non-zero exit so a
// check can gate), then let `--fix` remove the extra reply and leave a backup.
func TestDoctorSessionsFindsAndFixesDuplicateReplies(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fastagent.db")
	ctx := context.Background()

	db, err := store.NewDBStore("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.SaveSession(ctx, "u_1", "agt_1", "s-doctor", &store.SessionRecord{
		Channel: "web", ChatID: "s-doctor",
		Messages: []store.SessionMessage{
			{Role: "user", Content: "run it"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{
				ID: "call_A", Type: "function", Function: provider.FunctionCall{Name: "exec"},
			}}},
			{Role: "tool", ToolCallID: "call_A", Content: provider.StoppedToolResult},
			{Role: "tool", ToolCallID: "call_A", Content: "real result"},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	// Scan: findings must surface as a non-zero exit.
	scan := doctorCmd()
	scan.SetArgs([]string{"sessions", "--db", dbPath, "--json"})
	if err := scan.Execute(); err == nil {
		t.Fatal("doctor sessions reported success with a duplicate reply present")
	}

	// Fix: duplicate removed, backup written, second scan clean.
	backupDir := filepath.Join(dir, "backup")
	fix := doctorCmd()
	fix.SetArgs([]string{"sessions", "--db", dbPath, "--fix", "--backup-dir", backupDir})
	if err := fix.Execute(); err != nil {
		t.Fatalf("doctor sessions --fix: %v", err)
	}

	verify, err := store.NewDBStore("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer verify.Close()
	rec, err := verify.GetSession(ctx, "u_1", "agt_1", "s-doctor")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	replies := 0
	for _, m := range rec.Messages {
		if m.Role == "tool" && m.ToolCallID == "call_A" {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("tool replies after --fix = %d; want 1 (%+v)", replies, rec.Messages)
	}
	if got := rec.Messages[len(rec.Messages)-1].Content; got != provider.StoppedToolResult {
		t.Fatalf("kept reply = %q; want the first reply (index order preserved)", got)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "s-doctor.json")); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	clean := doctorCmd()
	clean.SetArgs([]string{"sessions", "--db", dbPath})
	if err := clean.Execute(); err != nil {
		t.Fatalf("post-fix scan still reports findings: %v", err)
	}
}

// Since Q4 the loop leaves an interrupted turn's tool call open instead of
// persisting a synthetic reply, so an unanswered call in stored history is the
// expected shape, not debt. The command must still print it (an operator wants
// to see the interruption) but must not fail a gate on it — only duplicates
// and orphans are actionable.
func TestDoctorSessionsTreatsOpenCallAsExpected(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fastagent.db")
	ctx := context.Background()

	db, err := store.NewDBStore("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.SaveSession(ctx, "u_1", "agt_1", "s-open", &store.SessionRecord{
		Channel: "web", ChatID: "s-open",
		Messages: []store.SessionMessage{
			{Role: "user", Content: "run it"},
			{Role: "assistant", ToolCalls: []provider.ToolCall{{
				ID: "call_open", Type: "function", Function: provider.FunctionCall{Name: "exec"},
			}}},
			{Role: "user", Content: "you there?"},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = db.Close()

	scan := doctorCmd()
	scan.SetArgs([]string{"sessions", "--db", dbPath, "--json"})
	if err := scan.Execute(); err != nil {
		t.Fatalf("open call must not fail the gate: %v", err)
	}
}
