package gateway

import (
	"context"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

// RemoveWorkspaceFile is the panel's delete, carried into the sandbox.
//
// It is the counterpart of the write-through the file tools perform: a store
// write is mirrored into the scope's live sandbox, and a store DELETE has to be
// mirrored too — otherwise the sandbox keeps its copy, and the next post-exec
// sync reads it as a sandbox-born file and writes it straight back to the store.
// That is the "deleted, then it came back" loop (docs 10 §4 G21 / G7b, decision
// d1).
//
// Two rules, both deliberate:
//
//   - It never creates an instance. `pool.Get` on the lifecycle pool is a lazy
//     proxy, and the removal itself asks only for a LIVE instance
//     (`LiveExecutorPool`): deleting a file in a scope nobody is running costs
//     nothing and must not bill the user for a sandbox.
//   - A backend whose /workspace IS the store (docker bind mount) has no second
//     copy: it does not implement LiveWorkspaceFileRemover, the assertion fails,
//     and the store delete was the whole job.
func (g *Gateway) RemoveWorkspaceFile(ctx context.Context, agentID, projectID, sessionID, storePath string) error {
	pool := g.SandboxPool()
	if pool == nil || agentID == "" || storePath == "" {
		return nil
	}
	ex, err := pool.Get(ctx, agentID, projectID, sessionID)
	if err != nil {
		return err
	}
	remover, ok := ex.(sandbox.LiveWorkspaceFileRemover)
	if !ok {
		return nil
	}
	return remover.RemoveLiveWorkspaceFile(ctx, storePath)
}
