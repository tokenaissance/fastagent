package tools

// The two places the model reads before it writes something long, and the one
// place it reads after forgetting the path.
//
// 2026-09-22 prod, twice in three minutes: write_file with a ~17 KB document in
// one call. The model's output is capped (8192 tokens here), so the call's
// arguments arrived as a JSON prefix — and the reply the model got back said
// "path is required and must include a filename", which is the one thing that
// was not wrong with the call. It had a path. It re-emitted the whole document
// and hit the same cap.
//
// write_file is registered twice — once for the host filesystem, once by
// SetExecutor for the sandbox — and the cloud deployment takes the second one,
// which is the one that failed. Both texts are pinned on both registrations, so
// a fix that lands on only one of them cannot pass.
//
// Falsification: remove the chunking sentence from the shared description and
// both cases fail; drop the example from the error and the third does.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// idleExecutor is enough to make SetExecutor take the sandbox branch: these
// cases read the schema, they never call a tool.
type idleExecutor struct{}

func (idleExecutor) Exec(context.Context, string, time.Duration) (string, error) { return "", nil }
func (idleExecutor) ReadFile(context.Context, string) (string, error)            { return "", nil }
func (idleExecutor) WriteFile(context.Context, string, string) (string, error)   { return "", nil }
func (idleExecutor) ListDir(context.Context, string) (string, error)             { return "", nil }
func (idleExecutor) Backend() string                                             { return "test" }
func (idleExecutor) Close() error                                                { return nil }

var _ sandbox.Executor = idleExecutor{}

// writeFileRegistrations is every write_file the model can be shown, by the name
// of the deployment shape that reaches it.
func writeFileRegistrations(t *testing.T) map[string]*Registry {
	t.Helper()
	sandboxed := NewRegistry(t.TempDir(), t.TempDir())
	sandboxed.SetExecutor(idleExecutor{})
	return map[string]*Registry{
		"built-in":  NewRegistry(t.TempDir(), t.TempDir()),
		"sandboxed": sandboxed,
	}
}

// writeFilePayload is the whole definition — description and parameters — since
// all of it rides in the schema of every request, not just the turn that calls
// the tool.
func writeFilePayload(t *testing.T, r *Registry) string {
	t.Helper()
	for _, def := range r.Definitions() {
		if def.Function.Name != "write_file" {
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	t.Fatal("write_file is not registered")
	return ""
}

func TestWriteFileSchemaSaysHowToWriteSomethingLong(t *testing.T) {
	for name, r := range writeFileRegistrations(t) {
		t.Run(name, func(t *testing.T) {
			payload := writeFilePayload(t, r)

			// The model cannot act on "your call may be cut off" unless the
			// schema names the way out.
			if !strings.Contains(payload, "edit_file") {
				t.Fatalf("the write_file schema does not say how to append to a long file; payload=%s", payload)
			}
			if !strings.Contains(strings.ToLower(payload), "long") {
				t.Fatalf("the write_file schema does not warn about long content; payload=%s", payload)
			}
		})
	}
}

// The schema is paid for on every request. The sentence above is worth its cost;
// a paragraph is not.
func TestWriteFileSchemaStaysSmall(t *testing.T) {
	const budget = 900
	for name, r := range writeFileRegistrations(t) {
		if got := len(writeFilePayload(t, r)); got > budget {
			t.Fatalf("%s write_file payload = %d chars; budget %d — it rides in every request", name, got, budget)
		}
	}
}

func TestTheEmptyPathErrorShowsAPathThatWorks(t *testing.T) {
	err := validateFileTargetPath("")
	if err == nil {
		t.Fatal("an empty path was accepted")
	}
	msg := err.Error()

	if !strings.Contains(msg, "notes.md") {
		t.Fatalf("the error states the rule but not a path that satisfies it: %q", msg)
	}
	// write_file and edit_file share this function, so naming either one would
	// be wrong half the time.
	for _, op := range []string{"write_file", "edit_file"} {
		if strings.Contains(msg, op) {
			t.Fatalf("the shared path error names one op: %q", msg)
		}
	}
}
