package tools

// apply_patch's two skill-namespace witnesses — the delivery point the other
// file tools guard and this one, until now, did not.
//
// Obligation under test (the same one read_file / write_file / edit_file honour,
// registry.go `skillManifestBlocked`): a non-admin chatter must not reach a
// BUNDLED skill's SKILL.md through a file tool, and the chat-time
// `skills/<name>/...` namespace is written only by the one tool that has a
// write path into it (write_file → per-user skills bucket + store mirror).
//
// Why apply_patch could not inherit either rule by sharing read_file's body: it
// keeps its own hand-written ladder (readForPatch / writeForPatch / deleteForPatch
// and their *ForPatchSandbox twins, apply_patch.go:500-698). Neither ladder ever
// calls skillManifestBlocked, and neither routes `skills/...` to the skill bucket,
// so the two rules that hold on the other three tools hold on this one by nothing
// at all.
//
// Falsification (run for real 2026-09-22, then reverted): deleting the pre-flight
// gate in either registration turns the matching case below red —
//   * the Update+Move case reddens with the executor's own WriteFile carrying the
//     SKILL.md body to `leaked.md` (the back door out of the read-only mount), and
//   * the Add-File case reddens with the executor writing `skills/mine/SKILL.md`
//     into the sandbox (host mode: the file appears in the agent's home instead).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// manifestBody stands in for the operator's private skill text. Any copy of it
// landing outside the read-only mount is the leak.
const manifestBody = "# secret-skill\n\noperator-private instructions\n"

// skillRouteExecutor answers the reads a misrouted patch performs and records
// every write/exec, so the witness can assert on what the sandbox was told to do
// rather than on a downstream symptom. Distinct from the counter-only
// recordingExecutor in apply_patch_identity_gate_cloudpath_e2e_test.go.
type skillRouteExecutor struct {
	reads  []string
	writes []recWrite
	execs  []string
}

type recWrite struct{ path, content string }

func (e *skillRouteExecutor) Exec(_ context.Context, cmd string, _ time.Duration) (string, error) {
	e.execs = append(e.execs, cmd)
	return "", nil
}

func (e *skillRouteExecutor) ReadFile(_ context.Context, path string) (string, error) {
	e.reads = append(e.reads, path)
	if path == "/skills/secret/SKILL.md" {
		return manifestBody, nil
	}
	return "", fmt.Errorf("cat %s: No such file or directory", path)
}

func (e *skillRouteExecutor) WriteFile(_ context.Context, path, content string) (string, error) {
	e.writes = append(e.writes, recWrite{path: path, content: content})
	return "ok", nil
}

func (e *skillRouteExecutor) ListDir(_ context.Context, _ string) (string, error) { return "", nil }
func (e *skillRouteExecutor) Backend() string                                     { return "skill-route-test" }
func (e *skillRouteExecutor) Close() error                                        { return nil }

// TestApplyPatchRefusesToMoveABundledSkillManifestOutOfTheMount is the back-door
// case: Update a bundled SKILL.md and Move it to a workspace path. The read is
// legitimate only if the text cannot then be written somewhere readable — which
// the Move does. The gate must fire before any write.
func TestApplyPatchRefusesToMoveABundledSkillManifestOutOfTheMount(t *testing.T) {
	ex := &skillRouteExecutor{}
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetExecutor(ex)

	out, err := r.Execute(context.Background(), "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: /skills/secret/SKILL.md\\n*** Move to: leaked.md\\n@@\\n+appended\\n*** End Patch\\n\"}")
	if err != nil {
		t.Fatalf("apply_patch errored instead of refusing: %v", err)
	}
	// Assert the leak itself first, so a failure names the exfil ("the body
	// reached leaked.md") rather than only the missing refusal.
	for _, w := range ex.writes {
		if strings.Contains(w.content, "operator-private instructions") {
			t.Fatalf("the SKILL.md body was written out to %q — the read-only mount's content escaped", w.path)
		}
	}
	if len(ex.writes) != 0 {
		t.Fatalf("a refused patch still wrote through the sandbox: %+v", ex.writes)
	}
	if !strings.Contains(out, "refused") {
		t.Fatalf("the manifest gate did not fire; result = %q", out)
	}
}

// TestApplyPatchRefusesTheChatSkillNamespace is the routing case: `skills/<name>/…`
// belongs to write_file (host bucket + store mirror). apply_patch has no matching
// write path and no way to withdraw the store mirror on Delete, so it must say so
// rather than drop the file into the sandbox's /workspace, where it is invisible
// to SkillsLoader and reappears in the chatter's file list.
func TestApplyPatchRefusesTheChatSkillNamespace(t *testing.T) {
	const patch = "{\"input\":\"*** Begin Patch\\n*** Add File: skills/mine/SKILL.md\\n+hello\\n*** End Patch\\n\"}"

	t.Run("sandbox registration", func(t *testing.T) {
		ex := &skillRouteExecutor{}
		r := NewRegistry(t.TempDir(), t.TempDir())
		t.Cleanup(r.Close)
		r.SetExecutor(ex)

		out, err := r.Execute(context.Background(), "apply_patch", patch)
		if err != nil {
			t.Fatalf("apply_patch errored instead of refusing: %v", err)
		}
		if !strings.Contains(out, "write_file") {
			t.Fatalf("the refusal does not point at the tool that can write a skill; result = %q", out)
		}
		for _, w := range ex.writes {
			if strings.HasPrefix(w.path, "skills/") {
				t.Fatalf("apply_patch wrote the skill namespace into the sandbox at %q", w.path)
			}
		}
	})

	t.Run("host registration", func(t *testing.T) {
		root := t.TempDir()
		r := NewRegistry(root, t.TempDir())
		t.Cleanup(r.Close)

		out, err := r.Execute(context.Background(), "apply_patch", patch)
		if err != nil {
			t.Fatalf("apply_patch errored instead of refusing: %v", err)
		}
		if !strings.Contains(out, "write_file") {
			t.Fatalf("the refusal does not point at the tool that can write a skill; result = %q", out)
		}
		if _, statErr := os.Stat(filepath.Join(root, "skills", "mine", "SKILL.md")); statErr == nil {
			t.Fatalf("apply_patch wrote the skill namespace onto the agent home at %s", filepath.Join(root, "skills", "mine", "SKILL.md"))
		}
	})

	// The namespace refusal is a ROUTING limit (apply_patch has no correct
	// place to put the file), not a confidentiality gate, so it does not
	// exempt the admin the way skillManifestBlocked does — the admin loses a
	// working-looking path that never actually worked on their behalf.
	t.Run("admin is refused too", func(t *testing.T) {
		ex := &skillRouteExecutor{}
		r := NewRegistry(t.TempDir(), t.TempDir())
		t.Cleanup(r.Close)
		r.SetExecutor(ex)
		r.SetCallerIsAdmin(true)

		out, err := r.Execute(context.Background(), "apply_patch", patch)
		if err != nil {
			t.Fatalf("apply_patch errored instead of refusing: %v", err)
		}
		if !strings.Contains(out, "write_file") {
			t.Fatalf("admin patch of the skill namespace was not routed to write_file; result = %q", out)
		}
		if len(ex.writes) != 0 {
			t.Fatalf("admin patch still wrote through the sandbox: %+v", ex.writes)
		}
	})
}
