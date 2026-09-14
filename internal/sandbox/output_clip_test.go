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

// The rule has one implementation now — the streaming sink — and ClipOutput is
// its string form. These two must never disagree, whatever the shape of the
// input: the loop backstop clips a string, the exec reader clips a stream, and
// the same result has to come out of both.
func TestClipOutputMatchesTheStreamingSink(t *testing.T) {
	line := "filler line that a command printed\n"
	inputs := map[string]string{
		"empty":          "",
		"short":          "ok\n",
		"exactly at cap": strings.Repeat("x", OutputHeadCap+OutputTailCap),
		"one byte over":  strings.Repeat("x", OutputHeadCap+OutputTailCap+1),
		"no newlines":    strings.Repeat("x", 400<<10),
		"line aligned":   strings.Repeat(line, 30000),
		"head only":      strings.Repeat("x", OutputHeadCap-1) + strings.Repeat("y", OutputTailCap+64),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			want, dropped := ClipOutput(in)

			// Streamed in awkward chunk sizes: the sink must not care where the
			// writes land.
			sink := &headTailSink{}
			for i := 0; i < len(in); {
				n := min(1+(i%7919), len(in)-i)
				sink.Write([]byte(in[i : i+n]))
				i += n
			}
			if got := sink.String(); got != want {
				t.Fatalf("streamed sink disagrees with ClipOutput:\n got %q\nwant %q", snippet([]byte(got), 200), snippet([]byte(want), 200))
			}
			if sink.dropped() != dropped {
				t.Fatalf("dropped = %v, want %v", sink.dropped(), dropped)
			}
			if got := sink.total; got != len(in) {
				t.Fatalf("the sink counted %d bytes, the input was %d", got, len(in))
			}
		})
	}
}

// The reason the exec reader can stream a 76 MB result at all: whatever is
// written to it, the sink holds two fixed windows and nothing else.
func TestHeadTailSinkNeverGrowsPastTheCap(t *testing.T) {
	sink := &headTailSink{}
	chunk := bytes.Repeat([]byte("abcdefgh"), 8<<10) // 64 KiB
	const writes = 1024                              // 64 MiB
	for i := 0; i < writes; i++ {
		sink.Write(chunk)
	}
	if sink.total != writes*len(chunk) {
		t.Fatalf("sink counted %d bytes, want %d", sink.total, writes*len(chunk))
	}
	if held := len(sink.head) + len(sink.tail); held > OutputHeadCap+OutputTailCap {
		t.Fatalf("the sink holds %d bytes after %d MiB — it must hold at most the two windows",
			held, writes*len(chunk)>>20)
	}
}

// stdout and stderr were always concatenated (stdout, a newline when both
// spoke, then stderr) and clipped as one string. Doing it per stream would put
// two markers in one result.
func TestCombinedStreamsKeepOneMarkerAndTheStderrTail(t *testing.T) {
	logs := captureSandboxWarnings(t)
	o := newClipOutput("exec/e2b")
	if err := o.writeStdout([]byte(strings.Repeat("out line\n", 40000))); err != nil {
		t.Fatal(err)
	}
	if err := o.writeStderr([]byte("warning: last line\n")); err != nil {
		t.Fatal(err)
	}
	got := o.text()
	if n := strings.Count(got, "of output omitted"); n != 1 {
		t.Fatalf("expected exactly one marker, got %d", n)
	}
	if !strings.HasSuffix(got, "\nwarning: last line\n") {
		t.Fatalf("stderr must still be the end of the result: %q", snippet([]byte(got), 200))
	}
	if !strings.HasPrefix(got, "out line\n") {
		t.Fatalf("stdout must still be the start of the result: %q", snippet([]byte(got), 200))
	}
	if o.produced() != 40000*len("out line\n")+1+len("warning: last line\n") {
		t.Fatalf("produced = %d", o.produced())
	}
	if n := strings.Count(logs.String(), "tool output truncated"); n != 1 {
		t.Fatalf("expected one truncation log line, got %d", n)
	}
	if !strings.Contains(logs.String(), "bytes=360020") {
		t.Errorf("the log must report the pre-clip size: %q", logs.String())
	}
}

// Two big streams: the result stays bounded and still carries both ends.
func TestBothStreamsHugeStaysBounded(t *testing.T) {
	captureSandboxWarnings(t)
	o := newClipOutput("exec/e2b")
	_ = o.writeStdout(bytes.Repeat([]byte("o"), 500<<10))
	_ = o.writeStderr(bytes.Repeat([]byte("e"), 500<<10))
	got := o.text()
	if len(got) > OutputHeadCap+OutputTailCap+400 {
		t.Fatalf("result is %d bytes; the cap must bound it", len(got))
	}
	if n := strings.Count(got, "of output omitted"); n != 1 {
		t.Fatalf("expected one marker, got %d", n)
	}
}
