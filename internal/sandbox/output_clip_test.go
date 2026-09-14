package sandbox

// The clip is the fix for the 2026-09-14 OOM loop (72 MiB exec results, two
// OOMKilled pods in an hour — see output_clip.go). These tests pin the three
// properties the fix depends on: nothing small is touched, something big keeps
// both ends, and the model is told how much it is missing.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func captureSandboxWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestClipOutputLeavesSmallOutputAlone(t *testing.T) {
	small := "tick 1\ntick 2\nexited(0)\n"
	got, dropped := ClipOutput(small)
	if dropped || got != small {
		t.Fatalf("ClipOutput(small) = %q, %v; want it unchanged", got, dropped)
	}

	// Exactly at the ceiling: still untouched (the cap is inclusive).
	exact := strings.Repeat("x", OutputHeadCap+OutputTailCap)
	if got, dropped := ClipOutput(exact); dropped || got != exact {
		t.Fatalf("ClipOutput(at cap) dropped %d bytes; the boundary must be inclusive", len(exact)-len(got))
	}
}

func TestClipOutputKeepsBothEndsAndSaysWhatItDropped(t *testing.T) {
	// 1 MiB of numbered lines: head and tail must be recognisable, the middle
	// must be gone, and the marker must name the size so the model can decide
	// what to re-run.
	const lines = 20000
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 40))
		sb.WriteString("\n")
	}
	big := "HEAD-MARKER\n" + sb.String() + "TAIL-MARKER\n"

	got, dropped := ClipOutput(big)
	if !dropped {
		t.Fatal("a 900 KB result must be clipped")
	}
	if len(got) > OutputHeadCap+OutputTailCap+400 {
		t.Fatalf("clipped output is %d bytes — the marker and ends alone must stay near the cap", len(got))
	}
	if !strings.HasPrefix(got, "HEAD-MARKER\n") {
		t.Error("the beginning of the output must survive")
	}
	if !strings.HasSuffix(got, "TAIL-MARKER\n") {
		t.Error("the end of the output must survive — that is where exit summaries live")
	}
	markerAt := strings.Index(got, "of output omitted")
	if markerAt < 0 {
		t.Errorf("the marker must state the omitted size:\n%s", got[:min(400, len(got))])
	} else if !strings.ContainsAny(got[max(0, markerAt-12):markerAt], "KMG") {
		t.Errorf("the omitted size must carry a unit, got %q", got[max(0, markerAt-12):markerAt])
	}
	if !strings.Contains(got, "tail -n 40") {
		t.Error("the marker must point at the cheap way to see more")
	}
}

func TestClipAndLogLogsExactlyOnce(t *testing.T) {
	logs := captureSandboxWarnings(t)
	small := ClipAndLog("ok", "test")
	if small != "ok" || logs.String() != "" {
		t.Fatalf("small output logged: %q", logs.String())
	}
	ClipAndLog(strings.Repeat("y", 300<<10), "exec/e2b")
	if n := strings.Count(logs.String(), "tool output truncated"); n != 1 {
		t.Fatalf("expected one truncation log line, got %d: %q", n, logs.String())
	}
	if !strings.Contains(logs.String(), "where=exec/e2b") {
		t.Errorf("the log line must name the producer: %q", logs.String())
	}
}
