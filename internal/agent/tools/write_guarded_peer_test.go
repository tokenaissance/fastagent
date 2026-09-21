package tools

// B7–B9's delivery-point witnesses — the half the port's test cannot see.
//
// The obligation (docs/文件系统形式化证明/12-lease-formal-design.md §3.1, L7;
// change-register row 35): a writer that was superseded between its read and its
// write must be TOLD, not silently obeyed. The RULE is witnessed at the port by
// internal/workspace/version_test.go, which proves PutIfVersion refuses a stale
// expectation. That test cannot see whether the three tool entry points actually
// use it: a tool left calling the plain Put would keep the port green while the
// agent's write still clobbered the peer. These are the delivery-point witnesses,
// one per writer (write_file, edit_file, apply_patch) — the two halves together
// are what "landed" means here.
//
// Falsification (run for real 2026-09-21, then reverted): replacing the
// putGuarded call with r.workspaceStore.Put at any of the three sites turns the
// matching case below red; each was verified to have landed with grep first.
//
// 2026-09-22 — the first cut of these witnesses was too narrow, and this file
// now carries the correction. There are TWO registrations of these tools and
// they do not share a body: registerFile (no executor) and registerSandboxedFile
// (SetExecutor — the shape a cloud turn with an E2B sandbox runs). The three
// cases above only ever built the first one, so they stayed green while the
// production registration kept calling the plain Put. The cases below are the
// same interleaving against the other registration, plus the guard's second
// obligation: a write that FAILS must not come back as a successful one.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// peerWritesAfterTheRead is the interleaving the obligation is about:
//
//	the tool reads the version  →  a peer writes the same key  →  the tool writes
//
// It is a real store (LocalFS) with one hook on Stat: it answers with the
// version that was live at READ time, and only then lets the peer's bytes land
// (the peer stands for a panel upload, a sibling session on the same project key,
// or another replica). The conditional write that follows must notice, and the
// peer's bytes must be the survivors.
type peerWritesAfterTheRead struct {
	*workspace.LocalFS
	peerBody string
	fired    bool
}

// The scope the tool writes into: no project (so the session segment stays) and
// one session, which is also what the seeds and the reads below use.
const (
	peerAgent   = "agt"
	peerSession = "sess-1"
)

func (s *peerWritesAfterTheRead) Stat(ctx context.Context, agentID, projectID, sessionID, path string) (*workspace.ObjectInfo, error) {
	info, err := s.LocalFS.Stat(ctx, agentID, projectID, sessionID, path)
	if !s.fired {
		s.fired = true
		// A different length from anything the tool writes, so LocalFS's
		// size:mtime_ns version cannot accidentally match by size alone.
		if perr := s.LocalFS.Put(ctx, agentID, projectID, sessionID, path,
			strings.NewReader(s.peerBody), int64(len(s.peerBody)), ""); perr != nil {
			return nil, perr
		}
	}
	return info, err
}

// newPeerRegistry is the store-backed shape the three writers share: no sandbox
// executor, so the tools take their RouteWorkspaceStore branch (the one that ends
// in putGuarded).
func newPeerRegistry(t *testing.T, seeded map[string]string) (*Registry, *peerWritesAfterTheRead) {
	t.Helper()
	ctx := context.Background()
	peer := &peerWritesAfterTheRead{
		LocalFS:  workspace.NewLocalFS(t.TempDir()),
		peerBody: "the peer's version, which is a good deal longer\n",
	}
	for name, body := range seeded {
		if err := peer.LocalFS.Put(ctx, peerAgent, "", peerSession, name, strings.NewReader(body), int64(len(body)), ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetWorkspaceStore(peer, peerAgent)
	r.SetSessionID(peerSession)
	return r, peer
}

func storedBody(t *testing.T, peer *peerWritesAfterTheRead, path string) string {
	t.Helper()
	rc, err := peer.LocalFS.Get(context.Background(), peerAgent, "", peerSession, path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertRefusedAndPeerSurvived(t *testing.T, out string, err error, peer *peerWritesAfterTheRead, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the superseded write was obeyed; result = %q", out)
	}
	// The refusal must name what happened and that nothing was lost — a bare
	// "conflict" would leave the agent unable to decide what to do next.
	for _, want := range []string{"another writer changed " + path, "nothing was overwritten", "read it again"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not say %q:\n%v", want, err)
		}
	}
	if got := storedBody(t, peer, path); got != peer.peerBody {
		t.Fatalf("the store holds %q; want the peer's bytes (the tool's write must not land)", got)
	}
}

// B7 — write_file, rewriting an existing key: the peer's version wins and the
// tool is told.
func TestWriteFileRefusesAWriteThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistry(t, map[string]string{"notes.md": "v1\n"})
	out, err := r.Execute(context.Background(), "write_file", `{"path":"notes.md","content":"v2\n"}`)
	assertRefusedAndPeerSurvived(t, out, err, peer, "notes.md")
}

// B7 — write_file, creating a key: the read found nothing, so the guarded write
// is create-only, and a peer that created the key in the window is not
// clobbered either.
func TestWriteFileRefusesACreateThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistry(t, nil)
	out, err := r.Execute(context.Background(), "write_file", `{"path":"fresh.md","content":"v1\n"}`)
	assertRefusedAndPeerSurvived(t, out, err, peer, "fresh.md")
}

// B8 — edit_file: it reads the object itself (Get), then putGuarded re-reads the
// version right before the write; the peer lands in between.
func TestEditFileRefusesAnEditThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistry(t, map[string]string{"notes.md": "one\ntwo\n"})
	out, err := r.Execute(context.Background(), "edit_file",
		`{"path":"notes.md","old_string":"two","new_string":"THREE"}`)
	assertRefusedAndPeerSurvived(t, out, err, peer, "notes.md")
}

// B9 — apply_patch: phase 1 reads the file, phase 2 flushes the write through
// writeForPatch → putGuarded; the peer lands between the two phases.
func TestApplyPatchRefusesAPatchThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistry(t, map[string]string{"notes.md": "one\n"})
	out, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}")
	assertRefusedAndPeerSurvived(t, out, err, peer, "notes.md")
}

// ── the same obligation, in the registration SetExecutor installs ─────────────
//
// registerSandboxedFile picks its own RouteWorkspaceStore branch; it is not a
// wrapper around the one above. A guard in only one registration is a guard on
// the mode nobody runs — everything above stays green while the branch a cloud
// turn actually takes keeps blind-writing. `peer.fired` is asserted first, so a
// branch that consults no version at all fails for the reason that matters
// ("a peer cannot even be noticed here"), not for a downstream symptom.

func newPeerRegistryWithExecutor(t *testing.T, seeded map[string]string) (*Registry, *peerWritesAfterTheRead) {
	t.Helper()
	r, peer := newPeerRegistry(t, seeded)
	r.SetExecutor(fakeExecutor{}) // ← the production registration
	return r, peer
}

func assertSandboxRefused(t *testing.T, out string, err error, peer *peerWritesAfterTheRead, path string) {
	t.Helper()
	if !peer.fired {
		t.Fatalf("the sandbox registration read no version before writing %s: a peer's write cannot be noticed there at all", path)
	}
	assertRefusedAndPeerSurvived(t, out, err, peer, path)
}

// B7, sandbox registration — write_file.
func TestSandboxWriteFileRefusesAWriteThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistryWithExecutor(t, map[string]string{"notes.md": "v1\n"})
	out, err := r.Execute(context.Background(), "write_file", `{"path":"notes.md","content":"v2\n"}`)
	assertSandboxRefused(t, out, err, peer, "notes.md")
}

// B8, sandbox registration — edit_file (the one branch that was already guarded;
// kept so a future edit that guards the other two by deleting the guard here
// cannot pass silently).
func TestSandboxEditFileRefusesAnEditThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistryWithExecutor(t, map[string]string{"notes.md": "one\ntwo\n"})
	out, err := r.Execute(context.Background(), "edit_file",
		`{"path":"notes.md","old_string":"two","new_string":"THREE"}`)
	assertSandboxRefused(t, out, err, peer, "notes.md")
}

// B9, sandbox registration — apply_patch.
func TestSandboxApplyPatchRefusesAPatchThatLostItsRace(t *testing.T) {
	r, peer := newPeerRegistryWithExecutor(t, map[string]string{"notes.md": "one\n"})
	out, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}")
	assertSandboxRefused(t, out, err, peer, "notes.md")
}

// ── a failed write must never read as a successful one ───────────────────────
//
// putGuarded adds exactly ONE refusal (a stale expectation). Everything else the
// store can answer — a 500, a permission error, a closed connection — still has
// to travel to the caller: a tool that says "Written 3 bytes" while the store
// holds nothing is the same silent-loss class this family exists to remove, and
// the plain Put these calls replaced did report it.

type storeThatFailsTheWrite struct {
	*workspace.LocalFS
	err error
}

func (s *storeThatFailsTheWrite) PutIfVersion(ctx context.Context, agentID, projectID, sessionID, path string, r io.Reader, size int64, contentType string, expected workspace.Version) error {
	_, _ = io.Copy(io.Discard, r)
	return s.err
}

func TestAGuardedWriteReportsAStoreFailureInsteadOfSuccess(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("s3: 500 InternalError")
	const patch = "{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}"
	cases := []struct {
		name    string
		tool    string
		args    string
		seed    map[string]string
		sandbox bool
	}{
		{name: "write_file", tool: "write_file", args: `{"path":"notes.md","content":"v2\n"}`},
		{name: "edit_file", tool: "edit_file", args: `{"path":"notes.md","old_string":"one","new_string":"two"}`, seed: map[string]string{"notes.md": "one\n"}},
		{name: "apply_patch", tool: "apply_patch", args: patch, seed: map[string]string{"notes.md": "one\n"}},
		{name: "write_file_sandbox", tool: "write_file", args: `{"path":"notes.md","content":"v2\n"}`, sandbox: true},
		{name: "edit_file_sandbox", tool: "edit_file", args: `{"path":"notes.md","old_string":"one","new_string":"two"}`, seed: map[string]string{"notes.md": "one\n"}, sandbox: true},
		{name: "apply_patch_sandbox", tool: "apply_patch", args: patch, seed: map[string]string{"notes.md": "one\n"}, sandbox: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &storeThatFailsTheWrite{LocalFS: workspace.NewLocalFS(t.TempDir()), err: boom}
			for name, body := range tc.seed {
				if err := store.LocalFS.Put(ctx, peerAgent, "", peerSession, name, strings.NewReader(body), int64(len(body)), ""); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			r := NewRegistry(t.TempDir(), t.TempDir())
			t.Cleanup(r.Close)
			r.SetWorkspaceStore(store, peerAgent)
			r.SetSessionID(peerSession)
			if tc.sandbox {
				r.SetExecutor(fakeExecutor{})
			}
			out, err := r.Execute(ctx, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("the store failed the write and the tool still reported success: out=%q", out)
			}
			if !strings.Contains(err.Error(), "500") {
				t.Fatalf("the failure reached the caller without its cause: %v", err)
			}
		})
	}
}
