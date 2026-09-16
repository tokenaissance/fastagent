package tools

// Policy C's declaration half (docs/sandbox-scope-leak.md §9.5).
//
// The damage of 2026-09-16 was not "the container was empty" — it was "the
// container was empty and nobody said so", so the model reasoned on a world
// fact that was really a store timeout. The mechanism side makes the scope's
// state knowable; this side puts it in front of the model, on the tools whose
// results the model would otherwise read as "your files are gone".

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

// unhydratedExecutor is a sandbox executor that also reports the Policy C
// state, i.e. what a sandbox whose store listing failed looks like to the tool
// layer.
type unhydratedExecutor struct {
	*bgRecordingExecutor
	unhydrated atomic.Bool
}

func (e *unhydratedExecutor) WorkspaceUnhydrated() bool { return e.unhydrated.Load() }

func newNoticeTestRegistry(t *testing.T, unhydrated bool) (*Registry, *unhydratedExecutor) {
	t.Helper()
	ex := &unhydratedExecutor{bgRecordingExecutor: &bgRecordingExecutor{}}
	ex.unhydrated.Store(unhydrated)
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetExecutor(ex)
	return r, ex
}

// A result the model reads as "the file is not there" must carry the reason.
func TestWorkspaceNoticeIsDeclaredOnFileToolsWhenUnhydrated(t *testing.T) {
	ctx := context.Background()
	r, _ := newNoticeTestRegistry(t, true)

	out, err := r.Execute(ctx, "read_file", `{"path":"refresh_paper_review.py"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, workspaceUnhydratedNotice) {
		t.Fatalf("read_file result carries no workspace declaration:\n%q", out)
	}
	if !strings.Contains(workspaceUnhydratedNotice, "not hydrated") {
		t.Fatalf("the declaration must name the state, not just be noise:\n%s", workspaceUnhydratedNotice)
	}
	// 2. It must separate itself from "the file does not exist" — that is the
	// exact inference the incident turned on.
	if !strings.Contains(workspaceUnhydratedNotice, "deleted") {
		t.Fatalf("the declaration must tell the model not to conclude the files were deleted:\n%s", workspaceUnhydratedNotice)
	}
	// 1. A fact, not an instruction.
	for _, imperative := range []string{"do not use", "you must", "instead", "try "} {
		if strings.Contains(strings.ToLower(workspaceUnhydratedNotice), imperative) {
			t.Fatalf("the declaration reads as an instruction (%q):\n%s", imperative, workspaceUnhydratedNotice)
		}
	}
}

// exec is the tool the incident actually ran through: it returned
// `No such file or directory` for a script that existed, because the workspace
// had come up empty. It must declare the state too — and the declaration has to
// sit AFTER the meta marker, which the agent loop strips only when it is the
// first line of the result.
func TestWorkspaceNoticeOnExecKeepsTheSandboxMetaMarkerFirst(t *testing.T) {
	ctx := context.Background()
	r, _ := newNoticeTestRegistry(t, true)

	out, err := r.Execute(ctx, "exec", execArgsJSON(t, "ls /workspace", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, MetaSandboxPrefix) {
		t.Fatalf("the sandbox meta marker must stay on line 1 (the loop strips it only there):\n%q", out)
	}
	rest := strings.TrimPrefix(out, MetaSandboxPrefix)
	if !strings.HasPrefix(rest, workspaceUnhydratedNotice) {
		t.Fatalf("exec result does not declare the workspace state:\n%q", out)
	}
}

// The declaration is a fact about THIS turn's environment. A healthy scope must
// not carry it, or it becomes wallpaper the model learns to skip.
func TestWorkspaceNoticeIsAbsentWhenTheWorkspaceIsHydrated(t *testing.T) {
	ctx := context.Background()
	r, _ := newNoticeTestRegistry(t, false)

	out, err := r.Execute(ctx, "read_file", `{"path":"refresh_paper_review.py"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, workspaceUnhydratedNotice) {
		t.Fatalf("a hydrated workspace announced a missing one:\n%q", out)
	}
}

// ...and it belongs only on tools that touch the workspace: a model thinking
// about the world in general (search, fetch) should not be told about a local
// infrastructure hiccup it cannot act on.
func TestWorkspaceNoticeStaysOffNonWorkspaceTools(t *testing.T) {
	ctx := context.Background()
	r, _ := newNoticeTestRegistry(t, true)
	r.Register("web_search", "search the web", map[string]interface{}{"type": "object"}, func(context.Context, json.RawMessage) (string, error) {
		return "results", nil
	})

	out, err := r.Execute(ctx, "web_search", `{"query":"anything"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, workspaceUnhydratedNotice) {
		t.Fatalf("a non-workspace tool carried the workspace declaration:\n%q", out)
	}
}

// The agent loop re-binds the executor every turn (bindSession → SetExecutor),
// so wrapping must be idempotent: two wraps would print the declaration twice.
func TestWorkspaceNoticeIsNotDuplicatedByRebinding(t *testing.T) {
	ctx := context.Background()
	r, ex := newNoticeTestRegistry(t, true)
	r.SetExecutor(ex)
	r.SetExecutor(ex)

	out, err := r.Execute(ctx, "read_file", `{"path":"report.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, workspaceUnhydratedNotice); got != 1 {
		t.Fatalf("declaration appears %d times, want 1:\n%q", got, out)
	}
}
