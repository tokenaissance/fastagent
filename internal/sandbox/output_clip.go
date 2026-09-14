package sandbox

import (
	"fmt"
	"log/slog"
	"strings"
)

// Tool output has no natural size limit, and that cost us a process per hour.
//
// Production evidence (2026-09-14, production namespace): every command on one
// agent returned ~72 MiB — not because the commands were big, but because each
// one re-read the same growing run log (the byte counts crept up by 16–228 bytes
// between calls seconds apart). The pod's limit is 1 GiB and it was OOMKilled
// twice in one hour (exit 137); the last log line before each death was the exec
// that delivered 73–75 MB. The cost is not one spike either: the tool result
// stays in the conversation, so every later model round in that turn re-serialized
// it.
//
// So every producer clips at the source. Keeping the head and the tail matters:
// the head shows what the command was doing, the tail usually carries the exit
// summary — and the marker tells the model exactly how much it is not seeing, so
// it can re-run with `tail -n 40` / `wc -l` instead of guessing.
const (
	// OutputHeadCap is how much of the beginning survives a clip.
	OutputHeadCap = 64 << 10
	// OutputTailCap is how much of the end survives a clip.
	OutputTailCap = 64 << 10
)

// ClipOutput bounds a tool result. It returns the input unchanged when it fits,
// and otherwise head + marker + tail. The bool reports whether anything was
// dropped so callers can log the truncation exactly once.
func ClipOutput(s string) (string, bool) {
	if len(s) <= OutputHeadCap+OutputTailCap {
		return s, false
	}
	omitted := len(s) - OutputHeadCap - OutputTailCap
	// Cut on a line boundary when one is close by, so the model never sees half
	// a line of output as if it were the whole thing.
	head := trimToLastNewline(s[:OutputHeadCap])
	tail := trimToFirstNewline(s[len(s)-OutputTailCap:])
	marker := fmt.Sprintf(
		"\n\n[… %s of output omitted — the command produced %s; showing the first %s and the last %s. Re-run with `tail -n 40`, `wc -l` or `head -c` to see a specific part. …]\n\n",
		humanBytes(omitted), humanBytes(len(s)), humanBytes(len(head)), humanBytes(len(tail)))
	return head + marker + tail, true
}

// ClipAndLog is ClipOutput plus the one log line operators grep for.
func ClipAndLog(s string, where string) string {
	clipped, dropped := ClipOutput(s)
	if dropped {
		slog.Warn("tool output truncated",
			"where", where, "bytes", len(s), "kept", len(clipped), "omitted", len(s)-OutputHeadCap-OutputTailCap)
	}
	return clipped
}

func trimToLastNewline(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i > len(s)/2 {
		return s[:i+1]
	}
	return s
}

func trimToFirstNewline(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < len(s)/2 {
		return s[i+1:]
	}
	return s
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
