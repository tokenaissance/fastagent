package sandbox

import (
	sandboxport "github.com/fastclaw-ai/fastclaw/internal/sandbox/port"
)

// The sandbox port lives in internal/sandbox/port. These aliases keep the
// adapter package source-compatible for existing callers.
type Executor = sandboxport.Executor
type ExecutorPool = sandboxport.ExecutorPool
type StoreScope = sandboxport.StoreScope
type LiveExecutorPool = sandboxport.LiveExecutorPool
type LiveWorkspaceFileRemover = sandboxport.LiveWorkspaceFileRemover
type WorkspaceSnapshotter = sandboxport.WorkspaceSnapshotter
type RemoteWorkspace = sandboxport.RemoteWorkspace
type PortExposer = sandboxport.PortExposer
type TemplateProvisioner = sandboxport.TemplateProvisioner

// PoolConfig holds configuration for creating sandbox pools.
type PoolConfig struct {
	Backend string // "docker", "e2b" (future)
	Image   string // container image (for docker backend)
	Policy  *Policy
	// E2B-specific fields (future)
	E2BTemplate string
	E2BAPIKey   string
}
