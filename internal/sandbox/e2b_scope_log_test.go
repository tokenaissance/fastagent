package sandbox

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// recordHandler captures slog records so a test can assert what an operator
// would actually see.
type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordHandler) attrs(msg string) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Message != msg {
			continue
		}
		out := map[string]string{}
		r.Attrs(func(a slog.Attr) bool {
			out[a.Key] = a.Value.String()
			return true
		})
		return out
	}
	return nil
}

// The incident this guards (docs/sandbox-scope-leak.md §8-§9) had to be
// diagnosed by matching "when did a turn run" against "when was a sandbox
// created", because no line carried both a scope and a sandbox id. Registering
// is the one moment both are in hand, and it covers create, adopt and revive —
// all three go through here.
func TestRegisterExecutorLogsTheScopeAndSandboxTogether(t *testing.T) {
	handler := &recordHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(previous)

	ex := &E2BExecutor{ident: sandboxIdent{id: "sbx-scope-log"}}
	p := &E2BExecutorPool{executors: map[string]*E2BExecutor{}, leaseEpochs: map[string]int64{}}
	p.registerExecutor("agt_x:s:sess_y", ex)

	attrs := handler.attrs("e2b sandbox bound to scope")
	if attrs == nil {
		t.Fatal("registering an executor logged nothing: a sandbox id with no scope is what made the last incident slow to place")
	}
	if attrs["scopeKey"] != "agt_x:s:sess_y" {
		t.Errorf("scopeKey = %q, want the key it was registered under", attrs["scopeKey"])
	}
	if attrs["sandboxID"] != "sbx-scope-log" {
		t.Errorf("sandboxID = %q, want the instance's id", attrs["sandboxID"])
	}
	// And the registration still happened.
	if p.executors["agt_x:s:sess_y"] != ex {
		t.Error("executor was not registered")
	}
}
