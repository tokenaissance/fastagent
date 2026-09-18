package sandbox_test

// G19, on real infrastructure: does the "this instance's /workspace was never
// filled" fact survive a POD HANDOFF?
//
// The unit tests pin the two ends (the row carries it, adoption reads it). This
// one pins the seam between them, because that seam is the whole bug: adoption
// deliberately does NOT replay hydration ("the creating pod hydrated the same
// scope"), so if the fact is not on the row, the adopting pod has no way to
// learn it — and the agent then reads an empty /workspace as "my files are
// gone". The Chinese docs call this 说假话 by omission: the declaration that
// should have been emitted simply is not.
//
// Two pools over one sqlite lease registry stand in for two replicas:
//
//	pod A: its workspace store cannot be LISTED (the outage that produces the
//	       empty /workspace) → creates the sandbox, discovers the fact, publishes it
//	pod B: a healthy store, adopts A's sandbox (hydration not replayed)
//
// The sentence the model reads is the tools layer's job and is pinned there
// (`TestWorkspaceSignalIsDeclaredOnFileToolsWhenUnhydrated`); what cannot be
// tested anywhere else is whether the fact is still there to declare, in a
// DIFFERENT process than the one that found it.
//
// NOT part of the normal suite — it creates a sandbox, so it is gated:
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/sandbox/ -run TestE2BLiveUnhydratedFactSurvivesPodHandoff -v -count=1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// listFailsStore is the store outage Policy C was written for: the scope has
// files, but the listing cannot be read, so the hydrate has to hand out an
// EMPTY /workspace and say so.
type listFailsStore struct {
	workspace.Store
}

func (listFailsStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, errors.New("workspace listing failed (injected)")
}

func TestE2BLiveUnhydratedFactSurvivesPodHandoff(t *testing.T) {
	if os.Getenv("FASTAGENT_E2B_LIVE") != "1" {
		t.Skip("live E2B: set FASTAGENT_E2B_LIVE=1 with E2B_API_KEY to run")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("E2B_API_KEY is required")
	}
	template := os.Getenv("FASTAGENT_E2B_TEMPLATE")
	if template == "" {
		template = "base"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// One lease registry, two "pods" (two pools, two owners) — the shape the
	// gateway has when two replicas serve the same agent.
	leaseDB, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "leases.db")+"?cache=shared")
	if err != nil {
		t.Fatalf("open lease db: %v", err)
	}
	defer leaseDB.Close()
	if err := leaseDB.Migrate(ctx); err != nil {
		t.Fatalf("migrate lease db: %v", err)
	}

	agent := fmt.Sprintf("live_handoff_%d", time.Now().UnixNano())
	const sessionID = "sess-handoff"

	// ── pod A: the store listing fails, so the sandbox comes up empty and says so
	poolA := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute,
		sandbox.WithSandboxLeases(sandbox.E2BLeaseOptions{Store: leaseDB, Owner: "pod-a", LeaseTTL: 10 * time.Minute}))
	defer poolA.CloseAll()
	poolA.SetWorkspace(listFailsStore{})

	exA, err := poolA.Get(ctx, agent, "", sessionID)
	if err != nil {
		t.Fatalf("pod A provision: %v", err)
	}
	unhydratedA, ok := exA.(sandbox.UnhydratedWorkspace)
	if !ok {
		t.Fatalf("executor %T does not report hydration state", exA)
	}
	if !unhydratedA.WorkspaceUnhydrated() {
		t.Fatal("the creating pod did not notice that its /workspace was never filled")
	}
	// A marker only this instance has, so "pod B adopted the same sandbox" is
	// checkable rather than assumed.
	if out, err := exA.Exec(ctx, "echo handoff > /tmp/fc-handoff && cat /tmp/fc-handoff", 60*time.Second); err != nil || !strings.Contains(out, "handoff") {
		t.Fatalf("pod A marker: %v (%s)", err, out)
	}

	// ── pod B: healthy store, fresh process, adopts A's sandbox
	poolB := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute,
		sandbox.WithSandboxLeases(sandbox.E2BLeaseOptions{Store: leaseDB, Owner: "pod-b", LeaseTTL: 10 * time.Minute}))
	defer poolB.CloseAll()
	poolB.SetWorkspace(workspace.NewLocalFS(t.TempDir()))

	exB, err := poolB.Get(ctx, agent, "", sessionID)
	if err != nil {
		t.Fatalf("pod B adopt: %v", err)
	}
	if _, ok := exB.(sandbox.UnhydratedWorkspace); !ok {
		t.Fatalf("adopted executor %T does not report hydration state", exB)
	}
	// Same instance, not a fresh one (adoption, not creation).
	if out, err := exB.Exec(ctx, "cat /tmp/fc-handoff", 60*time.Second); err != nil || !strings.Contains(out, "handoff") {
		t.Fatalf("pod B is not running pod A's sandbox: %v (%s)", err, out)
	}
	// THE CLAIM: the fact crossed the pod boundary.
	if !exB.(sandbox.UnhydratedWorkspace).WorkspaceUnhydrated() {
		t.Error("the adopting replica lost the fact that this sandbox's /workspace was never filled — " +
			"its next tool result would say nothing, and the agent would read the empty tree as deleted files " +
			"(docs 10 §4, G19)")
	}
}
