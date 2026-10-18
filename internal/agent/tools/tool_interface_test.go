package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// structTool is a stateful Tool. A ToolFunc would need a closure to carry the
// counter, so this type pins the interface path through registration.
type structTool struct {
	calls atomic.Int32
}

func (t *structTool) Call(context.Context, json.RawMessage) (string, error) {
	t.calls.Add(1)
	return "ok", nil
}

func TestToolFuncImplementsTool(t *testing.T) {
	var tool Tool = ToolFunc(func(context.Context, json.RawMessage) (string, error) {
		return "ok", nil
	})
	out, err := tool.Call(context.Background(), nil)
	if err != nil || out != "ok" {
		t.Fatalf("ToolFunc.Call = (%q, %v), want (ok, nil)", out, err)
	}
}

func TestRegisterToolStoresStructToolWithoutWrapper(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	probe := &structTool{}
	r.RegisterTool("struct_probe", "test", nil, probe)

	got := r.GetTool("struct_probe")
	if got != probe {
		t.Fatalf("GetTool = %T, want the registered *structTool", got)
	}
	if _, err := got.Call(context.Background(), nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := probe.calls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
}

func TestGetToolUnknownReturnsNil(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	if got := r.GetTool("does-not-exist"); got != nil {
		t.Fatalf("GetTool(unknown) = %T, want nil", got)
	}
}

func TestGetToolDoesNotAllocate(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	var missing bool
	allocs := testing.AllocsPerRun(1000, func() {
		missing = r.GetTool("exec") == nil
	})
	if missing {
		t.Fatal("exec not registered")
	}
	if allocs != 0 {
		t.Fatalf("GetTool allocated %.1f objects per lookup, want 0", allocs)
	}
}

// A workspace tool must carry its signal decorator from registration. Two
// lookups therefore return the same decorated shape, and each call still gets
// its own collector.
func TestWorkspaceToolIsWrappedAtRegistration(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	r.RegisterTool("read_file", "test", nil, ToolFunc(func(ctx context.Context, _ json.RawMessage) (string, error) {
		r.addSignal(ctx, "delta")
		return "body", nil
	}))

	for i := 0; i < 2; i++ {
		got := r.GetTool("read_file")
		wrapped, ok := got.(signalTool)
		if !ok {
			t.Fatalf("lookup %d returned %T, want signalTool", i, got)
		}
		if wrapped.registry != r {
			t.Fatalf("lookup %d bound a different registry", i)
		}
		out, err := got.Call(context.Background(), nil)
		if err != nil {
			t.Fatalf("lookup %d Call: %v", i, err)
		}
		if !strings.Contains(out, "delta") {
			t.Fatalf("lookup %d lost the call signal: %q", i, out)
		}
	}
}

// serialStructTool records how many calls run inside its body at once.
type serialStructTool struct {
	inFlight *atomic.Int32
	peak     *atomic.Int32
	delay    time.Duration
}

func (t *serialStructTool) Call(context.Context, json.RawMessage) (string, error) {
	cur := t.inFlight.Add(1)
	for {
		p := t.peak.Load()
		if cur <= p || t.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	time.Sleep(t.delay)
	t.inFlight.Add(-1)
	return "ok", nil
}

func TestRegisterSerialToolSerializesStructTool(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	var inFlight, peak atomic.Int32
	r.RegisterSerialTool("serial_struct", "test", nil, &serialStructTool{
		inFlight: &inFlight,
		peak:     &peak,
		delay:    20 * time.Millisecond,
	})

	tool := r.GetTool("serial_struct")
	if tool == nil {
		t.Fatal("serial_struct not registered")
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := tool.Call(context.Background(), nil); err != nil {
				t.Errorf("Call: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent calls = %d, want 1", got)
	}
}

// The decorator order is load-bearing: the signal exit stays outside the serial
// slot, as it was when GetFunc wrapped at lookup time.
func TestWorkspaceSerialToolKeepsSignalOutsideSerial(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	r.RegisterSerialTool("exec", "test", nil, ToolFunc(func(ctx context.Context, _ json.RawMessage) (string, error) {
		r.addSignal(ctx, "delta")
		return "body", nil
	}))

	got := r.GetTool("exec")
	signal, ok := got.(signalTool)
	if !ok {
		t.Fatalf("GetTool = %T, want signalTool", got)
	}
	if _, ok := signal.inner.(serialTool); !ok {
		t.Fatalf("signalTool.inner = %T, want serialTool", signal.inner)
	}
	out, err := got.Call(context.Background(), nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(out, "delta") {
		t.Fatalf("signal did not pass through both decorators: %q", out)
	}
}

func TestRegisterToolFromKeepsSource(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	r.RegisterToolFrom("plugin_probe", "test", nil, &structTool{}, SourcePlugin)

	if r.HasBuiltin("plugin_probe") {
		t.Fatal("a plugin-sourced tool must not report as builtin")
	}
	if r.GetTool("plugin_probe") == nil {
		t.Fatal("plugin_probe not registered")
	}
}

func TestExecuteCallsToolInterface(t *testing.T) {
	r := NewRegistry("", "")
	defer r.Close()
	probe := &structTool{}
	r.RegisterTool("execute_probe", "test", nil, probe)

	out, err := r.Execute(context.Background(), "execute_probe", "{}")
	if err != nil || out != "ok" {
		t.Fatalf("Execute = (%q, %v), want (ok, nil)", out, err)
	}
	if got := probe.calls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}
}
