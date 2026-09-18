package gateway

// RemoveWorkspaceFile is the gateway half of the panel's delete (decision d1):
// carry the deletion into the scope's LIVE sandbox, and only there. What these
// tests pin is the part that must never regress — it does not create a sandbox,
// and a backend with a single copy is a no-op rather than an error.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// fakePool hands back whatever it was given, and records whether Get was even
// called — "no instance" must mean "no call", not "a call that happens to
// create nothing".
type fakePool struct {
	ex      sandbox.Executor
	getErr  error
	getCall int
}

func (p *fakePool) Get(context.Context, string, string, string) (sandbox.Executor, error) {
	p.getCall++
	return p.ex, p.getErr
}
func (p *fakePool) Release(string, string, string) error { return nil }
func (p *fakePool) CloseAll()                            {}
func (p *fakePool) Backend() string                      { return "fake" }

// singleCopyExecutor is the docker shape: an executor that does NOT implement
// the removal capability, because its /workspace is the store itself.
type singleCopyExecutor struct{}

func (singleCopyExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("must not be called")
}
func (singleCopyExecutor) ReadFile(context.Context, string) (string, error) { return "", nil }
func (singleCopyExecutor) WriteFile(context.Context, string, string) (string, error) {
	return "", nil
}
func (singleCopyExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (singleCopyExecutor) Backend() string                                 { return "docker" }
func (singleCopyExecutor) Close() error                                    { return nil }

// removerExecutor is the e2b shape: a separate /workspace, so it can remove a
// file there.
type removerExecutor struct {
	gotPath string
	err     error
}

func (*removerExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (*removerExecutor) ReadFile(context.Context, string) (string, error) { return "", nil }
func (*removerExecutor) WriteFile(context.Context, string, string) (string, error) {
	return "", nil
}
func (*removerExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (*removerExecutor) Backend() string                                 { return "e2b" }
func (*removerExecutor) Close() error                                    { return nil }

func (r *removerExecutor) RemoveLiveWorkspaceFile(_ context.Context, storePath string) error {
	r.gotPath = storePath
	return r.err
}

func TestRemoveWorkspaceFile_NoPoolIsANoOp(t *testing.T) {
	g := &Gateway{}
	if err := g.RemoveWorkspaceFile(context.Background(), "agt", "", "chat", "sessions/chat/f"); err != nil {
		t.Fatalf("no pool: %v", err)
	}
}

func TestRemoveWorkspaceFile_SingleCopyBackendIsANoOp(t *testing.T) {
	pool := &fakePool{ex: singleCopyExecutor{}}
	g := &Gateway{sandboxPool: pool}
	if err := g.RemoveWorkspaceFile(context.Background(), "agt", "", "chat", "sessions/chat/f"); err != nil {
		t.Fatalf("single-copy backend: %v", err)
	}
}

func TestRemoveWorkspaceFile_AsksTheExecutorsCapability(t *testing.T) {
	ex := &removerExecutor{}
	pool := &fakePool{ex: ex}
	g := &Gateway{sandboxPool: pool}
	if err := g.RemoveWorkspaceFile(context.Background(), "agt", "proj", "chat", "projects/proj/f"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if ex.gotPath != "projects/proj/f" {
		t.Fatalf("executor got %q; want the agent-relative path unchanged", ex.gotPath)
	}
}

func TestRemoveWorkspaceFile_PoolErrorIsReported(t *testing.T) {
	pool := &fakePool{getErr: errors.New("pool down")}
	g := &Gateway{sandboxPool: pool}
	if err := g.RemoveWorkspaceFile(context.Background(), "agt", "", "chat", "f"); err == nil {
		t.Fatal("a pool error must be reported, not swallowed")
	}
}
