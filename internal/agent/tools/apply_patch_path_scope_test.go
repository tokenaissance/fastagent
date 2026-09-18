package tools

// 01 §8 (docs): one logical path is ONE store key. write_file / edit_file /
// read_file resolve it with r.scopeSessionID() + r.wsPath(path); apply_patch
// used r.sessionID + the raw path, so in a project session (collapsed scope +
// coding subdir) the same "notes.md" produced two store objects — the patch's
// root key and the mirrored "app/notes.md" — and a read_file that disagreed
// with the patch that had just written it.
//
// These pin the store key each of apply_patch's operations lands on, in the two
// scope shapes the coding runtime actually uses.

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// inProject selects the scope shape: a project chat collapses the store scope
// (workspace.WriteScope) while a loose chat keeps its session segment.
func newPathScopeRegistry(t *testing.T, subdir string, inProject bool) (*Registry, *workspace.LocalFS) {
	t.Helper()
	st := workspace.NewLocalFS(t.TempDir())
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetWorkspaceStore(st, "agt_scope")
	r.SetSessionID("sess-1")
	if subdir != "" {
		r.SetCodingSubdir(subdir)
	}
	if inProject {
		r.SetProjectID("proj-1")
	}
	return r, st
}

// storeKeys lists the scope's keys, sorted, so a test can assert "one file, one
// key" instead of only "this key exists".
func storeKeys(t *testing.T, st *workspace.LocalFS, agentID, projectID, sessionID string) []string {
	t.Helper()
	objs, err := st.List(context.Background(), agentID, projectID, sessionID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Path)
	}
	sort.Strings(out)
	return out
}

// A project session: the patch must touch the SAME key write_file wrote
// ("app/notes.md"), not a second root-level "notes.md".
func TestApplyPatchUsesTheSameStoreKeyAsWriteFile(t *testing.T) {
	r, st := newPathScopeRegistry(t, "app", true)

	if _, err := r.Execute(context.Background(), "write_file",
		`{"path":"notes.md","content":"one\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if got := storeKeys(t, st, "agt_scope", "proj-1", ""); len(got) != 1 || got[0] != "app/notes.md" {
		t.Fatalf("after write_file keys = %v; want [app/notes.md]", got)
	}

	// apply_patch's Update reads, then writes, the same logical path.
	if _, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}"); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if got := storeKeys(t, st, "agt_scope", "proj-1", ""); len(got) != 1 || got[0] != "app/notes.md" {
		t.Fatalf("after apply_patch keys = %v; want exactly [app/notes.md] (one file, one key)", got)
	}

	// And the read side agrees: read_file sees what the patch wrote.
	out, err := r.Execute(context.Background(), "read_file", `{"path":"notes.md"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if strings.TrimSpace(out) != "two" {
		t.Fatalf("read_file returned %q; want the patched content", out)
	}
}

// Delete is the other side of the same mapping: it must remove the key the
// other tools use, not leave it behind under a second spelling.
func TestApplyPatchDeleteUsesTheSameStoreKey(t *testing.T) {
	r, st := newPathScopeRegistry(t, "app", true)
	if _, err := r.Execute(context.Background(), "write_file",
		`{"path":"notes.md","content":"x\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Delete File: notes.md\\n*** End Patch\\n\"}"); err != nil {
		t.Fatalf("apply_patch delete: %v", err)
	}
	if got := storeKeys(t, st, "agt_scope", "proj-1", ""); len(got) != 0 {
		t.Fatalf("after delete keys = %v; want none", got)
	}
}

// A loose chat (no project, so the session segment is preserved) must land on
// the plain key — the mapping is the same function, just with an empty subdir.
func TestApplyPatchKeyInANonCodingSession(t *testing.T) {
	r, st := newPathScopeRegistry(t, "", false)
	if _, err := r.Execute(context.Background(), "write_file",
		`{"path":"notes.md","content":"one\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-one\\n+two\\n*** End Patch\\n\"}"); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if got := storeKeys(t, st, "agt_scope", "", "sess-1"); len(got) != 1 || got[0] != "notes.md" {
		t.Fatalf("keys = %v; want [notes.md]", got)
	}
}
