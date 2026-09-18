package sandbox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

var errStubRead = errors.New("stub read failure")

// durableSignals is the test's stand-in for the runtime's durable carrier: the
// point of the type is that it OUTLIVES a pool (a pod restart, or another
// replica adopting the lease) — which is exactly what the in-process map could
// not do (docs 09, G3).
type durableSignals struct {
	mu    sync.Mutex
	rows  map[string]string
	takes int
}

func newDurableSignals() *durableSignals { return &durableSignals{rows: map[string]string{}} }

func (d *durableSignals) key(agentID, projectID, sessionID string) string {
	return agentID + "|" + projectID + "|" + sessionID
}

func (d *durableSignals) AppendSignal(_ context.Context, agentID, projectID, sessionID, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows[d.key(agentID, projectID, sessionID)] += text
	return nil
}

func (d *durableSignals) TakeSignals(_ context.Context, agentID, projectID, sessionID string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.takes++
	k := d.key(agentID, projectID, sessionID)
	out := d.rows[k]
	delete(d.rows, k)
	return out, nil
}

func (d *durableSignals) parked() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.rows)
}

// The acceptance test for G3: a delta produced by the idle-eviction sync is
// delivered by a DIFFERENT pool instance over the same carrier. With the old
// in-process map the note died with the process (or with the lease hand-off).
func TestEvictSignalOutlivesThePoolThatProducedIt(t *testing.T) {
	carrier := newDurableSignals()

	// Pod A: the sandbox grew a file the store does not have, and the scope went
	// idle. The eviction sync writes it into the store and parks the fact.
	lpA, _, poolA := syncFixture(t, "stored", "stored")
	lpA.SetSignalStore(carrier)
	poolA.current.files["while_away.md"] = []byte("written by a background script")
	lpA.flushIfSupported(sandboxScope{agentID: "erin"})
	if carrier.parked() != 1 {
		t.Fatalf("the eviction signal was not parked durably: %d rows", carrier.parked())
	}

	// Pod B: a fresh pool over the same scope (a restart, or an adoption).
	lpB, _, _ := syncFixture(t, "stored", "stored")
	lpB.SetSignalStore(carrier)

	ex, err := lpB.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(context.Background(), "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "while_away.md") || !strings.Contains(out, "synced to the workspace store") {
		t.Fatalf("the parked signal never reached the agent:\n%q", out)
	}
	if carrier.parked() != 0 {
		t.Fatal("the signal was delivered but not cleared; it would repeat forever")
	}

	// And exactly once: the next exec has nothing left to say.
	out2, err := ex.Exec(context.Background(), "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out2, "while_away.md") {
		t.Fatalf("the parked signal was delivered twice:\n%q", out2)
	}
}

// Refusals and failures are NOT carried: a refusal leaves the two copies
// divergent, so the next sync re-derives it — and carrying it too would report
// the same path twice (docs 09, G3).
func TestBlockedPathsAreReDerivedRatherThanCarried(t *testing.T) {
	carrier := newDurableSignals()
	lp, _, pool := syncFixture(t, "stored", "stored")
	lp.SetSignalStore(carrier)

	// The store moves on, the sandbox keeps its own (different) copy: the sync
	// must refuse, not choose.
	hostWrite(t, lp.workspace.(*countingWorkspace), "HOST VERSION")
	pool.current.files["report.html"] = []byte("SANDBOX VERSION")
	lp.flushIfSupported(sandboxScope{agentID: "erin"})

	if carrier.parked() != 0 {
		t.Fatalf("a refusal was carried durably; it is re-derivable and would double-report (%d rows)", carrier.parked())
	}

	// The very next sync (post-exec) derives it again, in front of the agent.
	ex, err := lp.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(context.Background(), "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "NOT synced") || !strings.Contains(out, "report.html") {
		t.Fatalf("the refusal was not re-derived for the agent:\n%q", out)
	}
}

// A rebuild is discovered INSIDE a tool call, so its note rides that call's own
// result — no queue, no process memory (docs 09, G1/G2/G3).
func TestReplacedSandboxNoteRidesTheCallThatFoundIt(t *testing.T) {
	lp, _, pool := syncFixture(t, "stored", "stored")
	pool.current.replaced = true

	ex, err := lp.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.ReadFile(context.Background(), "/workspace/report.html")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "sandbox was REPLACED") {
		t.Fatalf("the rebuilding call did not carry the note:\n%q", out)
	}

	// One-shot: the flag is consumed by the call that found it.
	out2, err := ex.ReadFile(context.Background(), "/workspace/report.html")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(out2, "sandbox was REPLACED") {
		t.Fatalf("the replacement note was delivered twice:\n%q", out2)
	}
}

// A failing call cannot carry the note (the tool layer drops the text when it
// returns an error), so it must leave it for the next call that can.
func TestFailingCallLeavesTheReplacementNoteForTheNext(t *testing.T) {
	lp, _, pool := syncFixture(t, "stored", "stored")
	pool.current.replaced = true
	pool.current.readErr = errStubRead

	ex, err := lp.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := ex.ReadFile(context.Background(), "/workspace/report.html"); err == nil {
		t.Fatal("expected the failing read to propagate")
	}
	pool.current.readErr = nil

	out, err := ex.ReadFile(context.Background(), "/workspace/report.html")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "sandbox was REPLACED") {
		t.Fatalf("a failing call swallowed the note:\n%q", out)
	}
}

// G4, the half that needs no state: the reconcile's walk domain is the sandbox
// snapshot, so "the store has a path the sandbox does not" was never examined.
// One List per sync closes it — and the signal must state the fact WITHOUT
// claiming who caused it (a deletion inside the sandbox and an upload after it
// started leave the identical trace).
func TestSyncReportsPathsTheStoreHasAndTheSandboxDoesNot(t *testing.T) {
	lp, ws, _ := syncFixture(t, "stored", "stored")

	// An object the sandbox has never seen — the trace an upload (or a deletion
	// inside the sandbox) leaves.
	if err := ws.Put(context.Background(), "erin", "", "", "uploaded.csv",
		strings.NewReader("a,b\n"), 4, ""); err != nil {
		t.Fatalf("seed store-only object: %v", err)
	}

	ex, err := lp.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(context.Background(), "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "uploaded.csv") {
		t.Fatalf("the store-only path was not reported:\n%q", out)
	}
	if !strings.Contains(out, "cannot tell which") {
		t.Fatalf("the signal claims an attribution this runtime cannot make:\n%q", out)
	}
	if !strings.Contains(out, "exec will not find them") {
		t.Fatalf("the signal does not say what breaks:\n%q", out)
	}
}

// skills/ objects live in the read-only /skills mount, not in /workspace: an
// agent-scope sandbox would otherwise report every installed skill as "missing".
func TestSyncDoesNotReportTheSkillsNamespace(t *testing.T) {
	lp, ws, _ := syncFixture(t, "stored", "stored")
	if err := ws.Put(context.Background(), "erin", "", "", "skills/demo/SKILL.md",
		strings.NewReader("---\nname: demo\n---\n"), 21, ""); err != nil {
		t.Fatalf("seed skill object: %v", err)
	}

	ex, err := lp.Get(context.Background(), "erin", "", "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out, err := ex.Exec(context.Background(), "true", 30*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "SKILL.md") {
		t.Fatalf("the skills namespace was reported as a divergence:\n%q", out)
	}
}
