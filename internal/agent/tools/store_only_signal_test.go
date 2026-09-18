package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// sandboxListingExec is a remote-backed executor that answers the one command
// sandboxFileSet issues, so a test can state exactly which paths the sandbox has.
type sandboxListingExec struct {
	fakeExecutor
	sandboxPaths []string
	execErr      error
	execCalls    int
}

func (e *sandboxListingExec) IsRemoteWorkspace() {
	// The docker shape is the absence of this marker; a wrapper cannot remove a
	// method, so the test builds the shared case by not declaring it at all —
	// see TestListDirDoesNotProbeASharedBackend, which uses sharedExec below.
}

func (e *sandboxListingExec) Exec(_ context.Context, cmd string, _ time.Duration) (string, error) {
	e.execCalls++
	if e.execErr != nil {
		return "", e.execErr
	}
	return strings.Join(e.sandboxPaths, "\n") + "\n", nil
}

func newStoreOnlyRegistry(t *testing.T, ex *sandboxListingExec, files map[string]string) *Registry {
	t.Helper()
	ctx := context.Background()
	st := workspace.NewLocalFS(t.TempDir())
	for name, body := range files {
		if err := st.Put(ctx, "a", "", "s1", name, strings.NewReader(body), int64(len(body)), ""); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetWorkspaceStore(st, "a")
	r.SetSessionID("s1")
	r.SetExecutor(ex)
	return r
}

// The one divergence a store listing cannot show: a path the store has and the
// live sandbox does not. It must be named, with the reason and a way out
// (docs 10 §3.2, G7).
func TestListDirNamesPathsTheSandboxDoesNotHave(t *testing.T) {
	ctx := context.Background()
	ex := &sandboxListingExec{sandboxPaths: []string{"shared.csv"}}
	r := newStoreOnlyRegistry(t, ex, map[string]string{"shared.csv": "a,b\n", "uploaded.csv": "c,d\n"})

	out, err := r.Execute(ctx, "list_dir", `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	_, signal, found := strings.Cut(out, "[workspace]")
	if !found {
		t.Fatalf("the divergence was not stated:\n%q", out)
	}
	if !strings.Contains(signal, "1 path(s)") {
		t.Fatalf("the signal does not count the divergence:\n%q", signal)
	}
	if !strings.Contains(signal, "uploaded.csv") {
		t.Fatalf("the store-only path was not named:\n%q", signal)
	}
	if strings.Contains(signal, "shared.csv") {
		t.Fatalf("a path the sandbox does have was named as missing:\n%q", signal)
	}
	if !strings.Contains(signal, "exec") || !strings.Contains(signal, "write_file") {
		t.Fatalf("the signal does not say what breaks or how to fix it:\n%q", signal)
	}
}

// Silence when the two sides agree: an exception channel, not a per-listing banner.
func TestListDirStaysQuietWhenStoreAndSandboxAgree(t *testing.T) {
	ctx := context.Background()
	ex := &sandboxListingExec{sandboxPaths: []string{"shared.csv"}}
	r := newStoreOnlyRegistry(t, ex, map[string]string{"shared.csv": "a,b\n"})

	out, err := r.Execute(ctx, "list_dir", `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[workspace") {
		t.Fatalf("an agreeing pair produced a signal:\n%q", out)
	}
}

// A shared backend (docker) has one copy: nothing can diverge, and the sandbox
// must not even be asked.
func TestListDirDoesNotProbeASharedBackend(t *testing.T) {
	ctx := context.Background()
	ex := &sandboxListingExec{sandboxPaths: []string{}}
	r := newStoreOnlyRegistry(t, ex, map[string]string{"uploaded.csv": "c,d\n"})
	r.SetExecutor(sharedExec{inner: ex})

	out, err := r.Execute(ctx, "list_dir", `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[workspace") {
		t.Fatalf("a shared backend produced a divergence signal:\n%q", out)
	}
	if ex.execCalls != 0 {
		t.Fatalf("a shared backend was probed %d times", ex.execCalls)
	}
}

// If the sandbox cannot be asked, say nothing: an ungrounded signal is worse
// than silence (the same rule the write-through follows).
func TestListDirStaysQuietWhenTheSandboxCannotBeAsked(t *testing.T) {
	ctx := context.Background()
	ex := &sandboxListingExec{execErr: context.DeadlineExceeded}
	r := newStoreOnlyRegistry(t, ex, map[string]string{"uploaded.csv": "c,d\n"})

	out, err := r.Execute(ctx, "list_dir", `{"path":"."}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[workspace") {
		t.Fatalf("an ungrounded divergence signal was emitted:\n%q", out)
	}
}

// sharedExec hides the RemoteWorkspace marker: the docker shape, where /workspace
// IS the host directory and there is nothing to diverge.
type sharedExec struct{ inner sandbox.Executor }

func (s sharedExec) Exec(ctx context.Context, cmd string, d time.Duration) (string, error) {
	return s.inner.Exec(ctx, cmd, d)
}
func (s sharedExec) ReadFile(ctx context.Context, p string) (string, error) {
	return s.inner.ReadFile(ctx, p)
}
func (s sharedExec) WriteFile(ctx context.Context, p, c string) (string, error) {
	return s.inner.WriteFile(ctx, p, c)
}
func (s sharedExec) ListDir(ctx context.Context, p string) (string, error) {
	return s.inner.ListDir(ctx, p)
}
func (s sharedExec) Backend() string { return s.inner.Backend() }
func (s sharedExec) Close() error    { return s.inner.Close() }
