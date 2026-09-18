package tools

// What the agent is told about a write-through (docs/文件系统形式化证明/07 §3.11).
//
// The design settled on one principle: the agent must be able to PERCEIVE every
// state change in its world, and the harness must not keep redundant copies to
// compensate for changes it already announced. So the mirror never refuses and
// never preserves: it performs the write and states what it observed, with
// sizes and no content. Whether the replaced version mattered is a decision the
// agent already made when it wrote the file — it was told what the sandbox had
// changed by the exec signal of the command that changed it.
//
// Three signals exist, and they are mutually exclusive:
//
//   - replaced a different version  → the fact, with the size it had
//   - could not compare (over cap)  → the write happened unverified
//   - could not write at all        → the sandbox is unreachable; no version to
//                                     choose, nothing blocked
//
// And silence when there is nothing to say.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// mirrorFake stands in for the pool's write-through entry point.
type mirrorFake struct {
	fakeExecutor
	outcome sandbox.WriteThroughOutcome
	err     error
	calls   int
	// lastPrevious records the expectation the tool handed the mirror: "what
	// the store held before this write" (docs 10 §2.1, G6).
	lastPrevious string
	// lastStoreKey / lastSandboxPath record the two ends of the ONE mapping the
	// mirror uses, so a test can assert they agree (store key "app/x" ⇒ sandbox
	// "/workspace/app/x") — docs 01 §8.
	lastStoreKey    string
	lastSandboxPath string
}

func (m *mirrorFake) WriteThroughScope(_ sandbox.StoreScope, storeKey, sandboxPath, content, previous string) (sandbox.WriteThroughOutcome, error) {
	m.calls++
	m.lastPrevious = previous
	m.lastStoreKey = storeKey
	m.lastSandboxPath = sandboxPath
	return m.outcome, m.err
}

func newMirrorTestRegistry(t *testing.T, m *mirrorFake) *Registry {
	t.Helper()
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetWorkspaceStore(workspace.NewLocalFS(t.TempDir()), "agent_under_test")
	r.SetSessionID("sess_1")
	r.SetExecutor(m)
	return r
}

// The replaced version is stated by SIZE, never by content.
func TestWriteFileSignalsReplacedVersion(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareReplacedVerified, SandboxBytes: 4242}}
	r := newMirrorTestRegistry(t, m)

	const body = "the host's new version"
	out, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"`+body+`"}`)
	if err != nil {
		t.Fatalf("a stated replacement must not fail the call: %v", err)
	}
	if !strings.Contains(out, "[workspace]") || !strings.Contains(out, "4242") {
		t.Fatalf("result does not report the replaced version and its size:\n%q", out)
	}
	if strings.Contains(out, body) {
		t.Fatalf("the marker echoed file content into the model's context:\n%q", out)
	}
	if m.calls != 1 {
		t.Fatalf("expected exactly one write-through attempt, got %d", m.calls)
	}
}

// A replacement with no expectation says exactly that — and never invents a
// reason. Until 2026-09-18 this branch claimed "(over 2 MiB)" for every write
// that reached it, including 5-byte files (docs 10 §2.1, G5).
func TestWriteFileSignalsUncheckedReplacement(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareReplacedUnchecked, SandboxBytes: 7}}
	r := newMirrorTestRegistry(t, m)

	out, err := r.Execute(ctx, "write_file", `{"path":"model.bin","content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[workspace]") || !strings.Contains(out, "7 bytes") {
		t.Fatalf("result does not state that a different version was replaced:\n%q", out)
	}
	if strings.Contains(out, "too large") || strings.Contains(out, "2 MiB") {
		t.Fatalf("claimed a size reason the mirror never established:\n%q", out)
	}
	if strings.Contains(out, "different version in the sandbox") {
		t.Fatalf("claimed a verified comparison it never made:\n%q", out)
	}
}

// The docker shape: one copy, nothing mirrored, nothing replaced. Any sentence
// here would be a false alarm (docs 10 §2.1, G5).
func TestWriteFileStaysQuietOnASharedBackend(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareNoSecondCopy}}
	r := newMirrorTestRegistry(t, m)

	out, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[workspace") {
		t.Fatalf("a shared backend produced a workspace signal:\n%q", out)
	}
	if strings.Contains(out, "2 MiB") {
		t.Fatalf("a shared backend claimed a size it never saw:\n%q", out)
	}
}

// write_file hands the mirror what the store held before the write, so the
// mirror can tell a stale copy from the sandbox's own edit (docs 10 §2.1, G6).
// The read only happened for edit_file before this.
func TestWriteFilePassesThePreviousStoreVersionAsExpectation(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareNothingReplaced}}
	r := newMirrorTestRegistry(t, m)
	st := r.workspaceStore
	if err := st.Put(ctx, "agent_under_test", "", "sess_1", "notes.md", strings.NewReader("before"), int64(len("before")), ""); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	if _, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"after"}`); err != nil {
		t.Fatal(err)
	}
	if m.lastPrevious != "before" {
		t.Fatalf("write_file passed %q as the expectation, want the store's previous bytes", m.lastPrevious)
	}

	// A brand-new path has no expectation to give, and must not invent one.
	if _, err := r.Execute(ctx, "write_file", `{"path":"fresh.md","content":"x"}`); err != nil {
		t.Fatal(err)
	}
	if m.lastPrevious != "" {
		t.Fatalf("a new path passed %q as the expectation, want none", m.lastPrevious)
	}
}

// An unreachable sandbox has no version to choose and blocks nothing.
func TestWriteFileSignalsUnreachableSandbox(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{err: errors.New("sandbox is gone")}
	r := newMirrorTestRegistry(t, m)

	out, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "could not be updated") {
		t.Fatalf("result does not state that the sandbox copy is behind:\n%q", out)
	}
	// No choice is offered, because there is no second version to choose.
	if strings.Contains(out, "resolve_workspace_conflict") {
		t.Fatalf("an unreachable sandbox must not be presented as a choice:\n%q", out)
	}
}

// Silence otherwise: markers are an exception channel.
func TestWriteFileStaysQuietWhenMirrorIsUneventful(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareNothingReplaced}}
	r := newMirrorTestRegistry(t, m)

	out, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"fine"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[workspace") {
		t.Fatalf("an uneventful mirror produced a marker:\n%q", out)
	}
}

// A PARTIAL mirror gets a sentence, because its symptom is user-visible and
// confusing: the file is in the store and in this container, but another
// container of the same project did not get it, so a preview running there keeps
// showing the old version (docs 10 §4 G17, option H).
func TestWriteFileStatesAPartialProjectMirror(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{
		Comparison:        sandbox.CompareNothingReplaced,
		BroadcastFailures: 2,
	}}
	r := newMirrorTestRegistry(t, m)

	out, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"new"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[workspace]") {
		t.Fatalf("a partial mirror stayed silent:\n%q", out)
	}
	if !strings.Contains(out, "2 other sandbox") || !strings.Contains(out, "older version") {
		t.Fatalf("the sentence does not say what happened or what it costs:\n%q", out)
	}

	// And it is an exception channel: with no failures there is nothing to say,
	// even though the mirror ran.
	m2 := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareNothingReplaced}}
	r2 := newMirrorTestRegistry(t, m2)
	out2, err := r2.Execute(ctx, "write_file", `{"path":"notes.md","content":"new"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "[workspace") {
		t.Fatalf("a complete mirror produced a marker:\n%q", out2)
	}
}

// The mirror is the second consumer of the path mapping (docs 01 §8). It is
// handed a store key AND a sandbox path, and those two ends must describe the
// same object: the key the store write landed on, and the absolute path the
// hydrate would have materialised that key at. Before 2026-09-18 apply_patch
// wrote the store via wsPath() but the mirror derived its path from the raw
// tool path, so one write disagreed with itself — a file the patch had just
// written was still read as the old one by exec inside the sandbox.
func TestWriteThroughMirrorsOneKeyAndOnePath(t *testing.T) {
	ctx := context.Background()
	m := &mirrorFake{outcome: sandbox.WriteThroughOutcome{Comparison: sandbox.CompareNothingReplaced}}
	r := newMirrorTestRegistry(t, m)
	// The coding shape: one shared app tree (no session segment in the store
	// scope) with the app in a subdir (every tool path is prefixed with it).
	r.SetCodingSubdir("app")
	r.SetProjectID("proj-1")

	const wantKey = "app/notes.md"
	const wantPath = "/workspace/app/notes.md"

	if _, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"one\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if m.lastStoreKey != wantKey || m.lastSandboxPath != wantPath {
		t.Fatalf("write_file mirrored (key=%q path=%q); want (%q, %q)",
			m.lastStoreKey, m.lastSandboxPath, wantKey, wantPath)
	}

	// apply_patch must hand the mirror the SAME pair: one logical file has one
	// key and one sandbox path, whichever tool touched it.
	m.lastStoreKey, m.lastSandboxPath = "", ""
	if _, err := r.Execute(ctx, "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}"); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if m.lastStoreKey != wantKey || m.lastSandboxPath != wantPath {
		t.Fatalf("apply_patch mirrored (key=%q path=%q); want (%q, %q) — one file, one key",
			m.lastStoreKey, m.lastSandboxPath, wantKey, wantPath)
	}

	// And the store agrees with both: exactly one object, holding the patch's
	// bytes — not a root-level second copy the mirror would then have to guess
	// about.
	keys := storeKeys(t, r.workspaceStore.(*workspace.LocalFS), "agent_under_test", "proj-1", "")
	if len(keys) != 1 || keys[0] != wantKey {
		t.Fatalf("store keys = %v; want exactly [%s]", keys, wantKey)
	}
}
