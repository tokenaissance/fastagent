package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRegisterSerial_BlocksConcurrentSelfCalls fans out 5 goroutines into
// one serial-registered tool and asserts only one is inside the fn at any
// moment. This is the exact failure mode the delegate_task fan-out hit:
// multiple sub-agent invocations sharing a single browser daemon would
// step on each other's state. The mutex wrapper has to serialize them
// transparently — callers shouldn't need to coordinate.
func TestRegisterSerial_BlocksConcurrentSelfCalls(t *testing.T) {
	r := NewRegistry("", "")

	var inFlight atomic.Int32
	var peak atomic.Int32
	r.RegisterSerial("slow_tool", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		cur := inFlight.Add(1)
		// Record the highest "concurrent in fn" we ever saw — must
		// stay at 1 for serial to be doing its job.
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		return "ok", nil
	})

	fn := r.GetFunc("slow_tool")
	if fn == nil {
		t.Fatal("tool not registered")
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = fn(context.Background(), nil)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("serial guarantee violated: peak concurrent in fn = %d, want 1", got)
	}
}

// TestRegisterSerial_DoesNotBlockOtherTools confirms the mutex is
// per-tool, not global — a slow serial tool must not pin a fast
// unrelated tool. Otherwise an in-flight delegate_task would freeze
// every other tool the parent agent wants to run alongside.
func TestRegisterSerial_DoesNotBlockOtherTools(t *testing.T) {
	r := NewRegistry("", "")

	released := make(chan struct{})
	r.RegisterSerial("slow_serial", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		<-released
		return "slow_done", nil
	})
	r.Register("fast_other", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		return "fast_done", nil
	})

	// Start the slow serial call; it'll block until we release it.
	slowDone := make(chan string)
	go func() {
		out, _ := r.GetFunc("slow_serial")(context.Background(), nil)
		slowDone <- out
	}()

	// Other tool must complete immediately — the serial mutex is per
	// tool, not a global gate.
	fastOut, _ := r.GetFunc("fast_other")(context.Background(), nil)
	if fastOut != "fast_done" {
		t.Fatalf("other tool didn't run: got %q", fastOut)
	}

	close(released)
	if got := <-slowDone; got != "slow_done" {
		t.Fatalf("slow tool didn't run: got %q", got)
	}
}

// A queued call has to be abandonable by its caller. The wait behind a serial
// tool is the turn's clock too: the second delegate_task in a round used to sit
// on a bare sync.Mutex, so when the turn's clock ran out it stayed blocked —
// and then entered the tool body anyway with a ctx that had already died.
// That is the "Queued (waiting on prior sub-agent)…" turn (2026-09-14), and its
// result went nowhere because nobody was waiting for it anymore.
func TestRegisterSerialQueuedCallIsReleasedByItsContext(t *testing.T) {
	r := NewRegistry("", "")
	entered := make(chan string, 4)
	holdFirst := make(chan struct{})
	r.RegisterSerial("serial_tool", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		entered <- string(args)
		if string(args) == `"first"` {
			<-holdFirst
		}
		return "ok", nil
	})
	fn := r.GetFunc("serial_tool")

	go func() { _, _ = fn(context.Background(), json.RawMessage(`"first"`)) }()
	if got := <-entered; got != `"first"` {
		t.Fatalf("first call did not enter the body, got %s", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := fn(ctx, json.RawMessage(`"queued"`))
		done <- err
	}()

	// The queued call is waiting on the running one; end its clock.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("an abandoned queued call must report its context, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the queued call waited for the running one instead of its own context")
	}
	select {
	case got := <-entered:
		t.Fatalf("the abandoned call still entered the tool body: %s", got)
	default:
	}

	close(holdFirst)
}

// Same rule at the other end: a call that arrives with a dead context must not
// take the lock and run — the model emitted it for a turn that is already over.
func TestRegisterSerialAlreadyCancelledCallNeverEnters(t *testing.T) {
	r := NewRegistry("", "")
	ran := make(chan struct{}, 1)
	r.RegisterSerial("serial_tool", "test", nil, func(ctx context.Context, args json.RawMessage) (string, error) {
		ran <- struct{}{}
		return "ok", nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := r.GetFunc("serial_tool")(ctx, json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (out=%q)", err, out)
	}
	select {
	case <-ran:
		t.Fatal("a call whose turn is over must not run")
	default:
	}
}
