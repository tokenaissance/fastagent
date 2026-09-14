package sandbox

import (
	"errors"
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
	sink := &headTailSink{}
	// Chunked because the input here is already the oversized result: copying it
	// whole would double exactly what this file exists to bound.
	for len(s) > 0 {
		n := min(OutputHeadCap, len(s))
		sink.Write([]byte(s[:n]))
		s = s[n:]
	}
	return sink.String(), sink.dropped()
}

// ClipAndLog is ClipOutput plus the one log line operators grep for.
func ClipAndLog(s string, where string) string {
	clipped, dropped := ClipOutput(s)
	if dropped {
		logTruncation(where, len(s), len(clipped))
	}
	return clipped
}

// headTailSink is the streaming form of the same rule: it holds the first
// OutputHeadCap bytes and the last OutputTailCap bytes of whatever is written
// to it and counts the rest. A 76 MB stream therefore costs 128 KiB of memory
// and can still report its own size — which is the point, because the streaming
// reader would otherwise have to hold the whole response to bound it.
type headTailSink struct {
	head  []byte
	tail  []byte
	total int
}

func (s *headTailSink) Write(p []byte) {
	s.total += len(p)
	if n := min(OutputHeadCap-len(s.head), len(p)); n > 0 {
		s.head = append(s.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	// Keep the tail as a ring: a frame that is itself longer than the window
	// replaces it outright, anything shorter shifts the older bytes out.
	if len(p) >= OutputTailCap {
		s.tail = append(s.tail[:0], p[len(p)-OutputTailCap:]...)
		return
	}
	if over := len(s.tail) + len(p) - OutputTailCap; over > 0 {
		s.tail = append(s.tail[:0], s.tail[over:]...)
	}
	s.tail = append(s.tail, p...)
}

// drop counts bytes that were produced but deliberately not stored — the
// middle of a stream that was already past the cap. They belong in the
// reported total and nowhere else.
func (s *headTailSink) drop(n int) {
	if n > 0 {
		s.total += n
	}
}

// kept is everything the sink still holds: all of a stream under the cap,
// head+tail of one over it.
func (s *headTailSink) kept() []byte {
	out := make([]byte, 0, len(s.head)+len(s.tail))
	out = append(out, s.head...)
	return append(out, s.tail...)
}

func (s *headTailSink) dropped() bool {
	return s.total > OutputHeadCap+OutputTailCap
}

// String renders the sink: the stream unchanged when it fit, head + marker +
// tail otherwise.
func (s *headTailSink) String() string {
	if !s.dropped() {
		return string(s.kept())
	}
	// Cut on a line boundary when one is close by, so the model never sees half
	// a line of output as if it were the whole thing.
	head := trimToLastNewline(string(s.head))
	tail := trimToFirstNewline(string(s.tail))
	return head + clipMarker(s.total-OutputHeadCap-OutputTailCap, s.total, len(head), len(tail)) + tail
}

func clipMarker(omitted, total, headLen, tailLen int) string {
	return fmt.Sprintf(
		"\n\n[… %s of output omitted — the command produced %s; showing the first %s and the last %s. Re-run with `tail -n 40`, `wc -l` or `head -c` to see a specific part. …]\n\n",
		humanBytes(omitted), humanBytes(total), humanBytes(headLen), humanBytes(tailLen))
}

func logTruncation(where string, produced, kept int) {
	slog.Warn("tool output truncated",
		"where", where, "bytes", produced, "kept", kept, "omitted", produced-OutputHeadCap-OutputTailCap)
}

// clipOutput is the tool-result contract for one exec: the command's stdout and
// stderr, each bounded as it arrives, rendered as the single string the caller
// (and so the model) sees.
//
// The two streams are still concatenated the way they always were — stdout, a
// newline when both spoke, then stderr. stderr is folded into stdout's sink at
// render time rather than concatenated after it, so the combination is clipped
// once; clipping each stream separately would put two markers in one result.
type clipOutput struct {
	where  string
	stdout headTailSink
	stderr headTailSink
}

func newClipOutput(where string) *clipOutput { return &clipOutput{where: where} }

func (o *clipOutput) writeStdout(p []byte) error { o.stdout.Write(p); return nil }
func (o *clipOutput) writeStderr(p []byte) error { o.stderr.Write(p); return nil }

// produced is the pre-clip size, the number operators grep for to spot a
// runaway command.
func (o *clipOutput) produced() int {
	if o.stderr.total > 0 && o.stdout.total > 0 {
		return o.stdout.total + 1 + o.stderr.total
	}
	return o.stdout.total + o.stderr.total
}

// text renders the result and logs the truncation once. Call it after the
// stream has been read; it is idempotent but logs on each call.
func (o *clipOutput) text() string {
	out := o.render()
	if o.produced() > OutputHeadCap+OutputTailCap {
		logTruncation(o.where, o.produced(), len(out))
	}
	return out
}

func (o *clipOutput) render() string {
	if o.stderr.total == 0 {
		return o.stdout.String()
	}
	if o.stdout.total == 0 {
		return o.stderr.String()
	}
	// A copy, so text() does not mutate the sink it may be asked to render
	// twice; both windows are at most 64 KiB each.
	combined := &headTailSink{total: o.stdout.total}
	combined.head = append(combined.head, o.stdout.head...)
	combined.tail = append(combined.tail, o.stdout.tail...)
	combined.Write([]byte("\n"))
	combined.Write(o.stderr.kept())
	// Anything the stderr sink dropped is dropped here too; only its ending can
	// survive anyway, and a marker in the middle would be clipped out again.
	combined.drop(o.stderr.total - len(o.stderr.kept()))
	return combined.String()
}

// The caps the two exec contracts use are the same ones, applied differently:
// a tool result is shortened, a machine payload is refused. errPayloadOverCap
// is how the refusal travels back to the caller that owns the diagnosis.
var errPayloadOverCap = errors.New("payload is over its cap")

// payloadOutput is the machine-payload contract: keep every byte, up to the
// cap, and stop the read past it. Silently keeping less would corrupt a base64
// tar; draining 101 MB of body in order to reject it is what the cap exists to
// avoid.
type payloadOutput struct {
	limit int
	buf   strings.Builder
}

func newPayloadOutput(limit int) *payloadOutput { return &payloadOutput{limit: limit} }

func (p *payloadOutput) writeStdout(b []byte) error {
	if p.buf.Len()+len(b) > p.limit {
		return errPayloadOverCap
	}
	p.buf.Write(b)
	return nil
}

// stderr on a machine payload is never part of the payload — appending it would
// corrupt the base64, and dropping it would hide the reason. Name it instead.
func (p *payloadOutput) writeStderr(b []byte) error {
	return fmt.Errorf("payload command wrote %d bytes to stderr: %s", len(b), snippet(b, 200))
}

func (p *payloadOutput) produced() int { return p.buf.Len() }
func (p *payloadOutput) text() string  { return p.buf.String() }

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
