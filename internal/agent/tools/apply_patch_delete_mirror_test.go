package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// mirrorRemovingExec is the e2b shape: /workspace is a second copy, and the
// executor can remove one path from the scope's live sandbox.
type mirrorRemovingExec struct {
	sandboxListingExec
	removed []string
	err     error
}

func (e *mirrorRemovingExec) RemoveLiveWorkspaceFile(_ context.Context, storePath string) error {
	e.removed = append(e.removed, storePath)
	return e.err
}

// apply_patch's Delete used to stop at the store: the sandbox copy survived, the
// next sync read "the store has no such path, the sandbox does ⇒ that file is a
// sandbox product" and pushed the OLD version back — the deletion was undone and
// the caller had been told it worked. The panel's delete had already been fixed
// this way (d1, docs 05 §8); this pins the same treatment for the tool path.
//
// Falsification: delete the `LiveWorkspaceFileRemover` block in
// deleteForPatchSandbox and this fails — the sandbox half is never asked.
func TestApplyPatchDeleteReachesTheLiveSandboxCopy(t *testing.T) {
	ctx := context.Background()
	ex := &mirrorRemovingExec{}
	r := newStoreOnlyRegistry(t, &ex.sandboxListingExec, map[string]string{"notes.md": "old version"})

	if err := r.deleteForPatchSandbox(ctx, ex, "notes.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(ex.removed) != 1 {
		t.Fatalf("the sandbox copy was asked to delete %d times, want exactly 1", len(ex.removed))
	}
	// The capability takes the STORE key, not the sandbox path (executor.go:102).
	if ex.removed[0] != r.wsPath("notes.md") {
		t.Fatalf("mirror delete got %q, want the store key %q", ex.removed[0], r.wsPath("notes.md"))
	}
	if _, err := r.workspaceStore.Get(ctx, "a", "", "s1", "notes.md"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("the store half did not happen: %v", err)
	}
}

// A failed mirror is NOT a failed delete: the store half really is done, so
// failing the call would tell the agent to redo a delete that already took
// effect. Same posture as the panel's `sandboxRemoved:false` — succeed, and say
// what did not happen (the signal is attached through the existing exit).
func TestApplyPatchDeleteSurvivesAFailedMirrorWithoutLying(t *testing.T) {
	ctx := context.Background()
	ex := &mirrorRemovingExec{err: errors.New("sandbox unreachable")}
	r := newStoreOnlyRegistry(t, &ex.sandboxListingExec, map[string]string{"notes.md": "old version"})

	if err := r.deleteForPatchSandbox(ctx, ex, "notes.md"); err != nil {
		t.Fatalf("a mirror failure must not fail the call: %v", err)
	}
	if len(ex.removed) != 1 {
		t.Fatalf("the mirror was asked %d times, want 1", len(ex.removed))
	}
	if _, err := r.workspaceStore.Get(ctx, "a", "", "s1", "notes.md"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("the store half must still be committed (no half-rollback): %v", err)
	}
}

// The docker shape: /workspace IS the store, so the backend does not implement
// the capability — the assertion fails and nothing is owed. Asserting this is
// the point: "no-op here" is the design, not a gap somebody should close later.
func TestApplyPatchDeleteIsANoOpWhenTheBackendHasNoSecondCopy(t *testing.T) {
	ctx := context.Background()
	ex := &sandboxListingExec{} // no RemoveLiveWorkspaceFile
	r := newStoreOnlyRegistry(t, ex, map[string]string{"notes.md": "old version"})

	if err := r.deleteForPatchSandbox(ctx, ex, "notes.md"); err != nil {
		t.Fatalf("delete on a shared backend: %v", err)
	}
	if _, err := r.workspaceStore.Get(ctx, "a", "", "s1", "notes.md"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("the store half is the whole job on this backend, and it did not happen: %v", err)
	}
}
