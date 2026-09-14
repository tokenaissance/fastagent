package sandbox

// The two clocks that can end a long exec both used to surface as bare
// provider strings, which is how a running batch job got read as a failed one
// (r39/r41/r45 "context canceled"; r42/553 "deadline_exceeded" with the
// answer already inside the delivered output). This pins what each clock's
// error now says.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// cutStreamTransport reproduces the r45 shape at the transport level: envd
// answers 200, delivers the start frame, and then the runtime cancels the
// request while the body is still open. That is exactly how io.ReadAll ends up
// returning bytes AND context.Canceled.
type cutStreamTransport struct {
	sandboxID string

	once sync.Once
}

func (t *cutStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start, _ := json.Marshal(map[string]any{"event": map[string]any{
		"start": map[string]any{"pid": 19038}}})
	pr, pw := io.Pipe()
	go func() {
		// The 38 bytes production saw: one envelope, one start frame.
		_, _ = pw.Write(connectEnvelope(start))
		<-req.Context().Done()
		_ = pw.CloseWithError(req.Context().Err())
	}()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       pr,
		Request:    req,
	}, nil
}

func TestE2BExecClockHints(t *testing.T) {
	t.Run("cancelled mid-stream names the runtime clock, not the sandbox", func(t *testing.T) {
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: &cutStreamTransport{sandboxID: "sb-1"}}

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel once the start frame is in flight, the way a superseded turn
		// (or a departing caller) does.
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		defer cancel()

		out, err := ex.Exec(ctx, "nohup bash /workspace/job.sh > /tmp/job.log 2>&1 & sleep 175", 240*time.Second)
		if err == nil {
			t.Fatal("a cut stream must be an error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation must stay detectable through the wrap: %v", err)
		}
		if !strings.Contains(err.Error(), "may still be running") ||
			!strings.Contains(err.Error(), "run_in_background") {
			t.Fatalf("error must tell the model the job may be alive and how to check it: %q", err.Error())
		}
		if strings.Contains(err.Error(), "the output above was delivered") {
			t.Fatalf("nothing was delivered yet, so the output clause must not appear: %q", err.Error())
		}
		// The bytes that did arrive still travel back to the caller, as before.
		if !strings.Contains(out, `"pid":19038`) {
			t.Fatalf("partial body was dropped: %q", out)
		}
	})

	t.Run("envd deadline with output says the output was delivered", func(t *testing.T) {
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: &fakeEnvdTransport{
			trailerError: "deadline_exceeded: context deadline exceeded",
		}}

		out, err := ex.Exec(context.Background(), "ls out | wc -l", 240*time.Second)
		if err == nil {
			t.Fatal("a truncated stream must be an error")
		}
		// The provider's own words are still there — the hint is additive.
		if !strings.Contains(err.Error(), "server error: ") ||
			!strings.Contains(err.Error(), "deadline_exceeded: context deadline exceeded") {
			t.Fatalf("provider detail was replaced instead of annotated: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "run_in_background") {
			t.Fatalf("envd-deadline error must name the fix: %q", err.Error())
		}
		// One supported way to wait. `</dev/null`, nohup, setsid and tmux are the
		// hand-rolled shapes whose five incidents this hint exists to end; naming
		// them here would re-teach them.
		for _, alternative := range []string{"</dev/null", "nohup", "setsid", "tmux"} {
			if strings.Contains(err.Error(), alternative) {
				t.Fatalf("hint still teaches %q: %q", alternative, err.Error())
			}
		}
		// This transport delivers no frames, so the "output was delivered"
		// clause must stay out — the hint may not claim bytes we never got.
		if strings.Contains(err.Error(), "the output above was delivered") {
			t.Fatalf("hint claimed delivered output that never arrived: %q", err.Error())
		}
		_ = out
	})

	t.Run("hint is additive only where it applies", func(t *testing.T) {
		// A plain truncation (the shape a still-booting sandbox produces) is
		// retried by Hydrate; it must not grow advice about deadlines nobody hit.
		ex := testExecutor(&leaseCloseRecorder{}, "sb-1", "tok-1")
		ex.client = &http.Client{Transport: &fakeEnvdTransport{truncateFirstN: 1}}

		_, err := ex.Exec(context.Background(), "echo ok", 10*time.Second)
		if err == nil {
			t.Fatal("a truncated stream must be an error")
		}
		var truncated *execStreamTruncatedError
		if !errors.As(err, &truncated) {
			t.Fatalf("error type changed: %T", err)
		}
		if strings.Contains(err.Error(), "[hint:") {
			t.Fatalf("no clock hint belongs on a plain truncation: %q", err.Error())
		}
	})

	t.Run("stalled hint still fires when the run had printed something", func(t *testing.T) {
		got := execStalledHint("deadline_exceeded: context deadline exceeded", "553\n")
		if !strings.Contains(got, "the output above was delivered") {
			t.Fatalf("output-present deadline hint = %q", got)
		}
		if execStalledHint("internal: sandbox is shutting down", "553\n") != "" {
			t.Fatal("only a deadline trailer may claim a stalled stream")
		}
		if execCancelledHint(errors.New("boom")) != "" {
			t.Fatal("only cancellations carry the cancelled-clock hint")
		}
	})
}

// leaseCloseRecorder and testExecutor live in the pool tests; this keeps the
// dependency explicit for readers of this file.
var _ = leaseCloseRecorder{}
