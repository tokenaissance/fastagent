// Package port holds the sandbox port: executors, pools, optional executor
// capabilities, and the cross-pod lease contract.
//
// The adapter package internal/sandbox implements these interfaces. The
// relational store also implements SandboxLeaseStore, so this package keeps
// the store adapter from importing the sandbox adapter.
package port

import (
	"context"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace/port"
)

// StoreScope names the workspace-store scope a write landed in.
type StoreScope = port.Scope

// Executor abstracts a sandboxed execution environment for one user.
type Executor interface {
	Exec(ctx context.Context, command string, timeout time.Duration) (string, error)
	ReadFile(ctx context.Context, path string) (string, error)
	WriteFile(ctx context.Context, path, content string) (string, error)
	ListDir(ctx context.Context, path string) (string, error)
	Backend() string
	Close() error
}

// ExecutorPool manages per-(agent, project, session) sandbox lifecycles.
type ExecutorPool interface {
	Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error)
	Release(agentID, projectID, sessionID string) error
	CloseAll()
	Backend() string
}

// LiveExecutorPool is the read-only half of ExecutorPool: hand back the
// scope's CURRENT instance, or false, WITHOUT creating one.
type LiveExecutorPool interface {
	LiveExecutor(agentID, projectID, sessionID string) (Executor, bool)
	LiveProjectExecutors(agentID, projectID string) []Executor
}

// LiveWorkspaceFileRemover deletes one agent-relative store path's copy inside
// the scope's live sandbox, and does nothing when there is no live sandbox.
type LiveWorkspaceFileRemover interface {
	RemoveLiveWorkspaceFile(ctx context.Context, storePath string) error
}

// WorkspaceSnapshotter is an optional Executor capability for flush-on-evict.
type WorkspaceSnapshotter interface {
	SnapshotWorkspace(ctx context.Context) (map[string][]byte, error)
}

// RemoteWorkspace marks executors whose /workspace is not shared with the
// host filesystem.
type RemoteWorkspace interface {
	IsRemoteWorkspace()
}

// PortExposer is an optional Executor capability that returns an externally
// reachable URL for a port inside the sandbox.
type PortExposer interface {
	ExposePort(ctx context.Context, port int) (string, error)
}

// TemplateProvisioner is an optional Executor capability that copies a local
// directory tree into the sandbox at destDir.
type TemplateProvisioner interface {
	ProvisionDir(ctx context.Context, localDir, destDir string) error
}

// SandboxLeaseRecord describes one live cross-pod sandbox lease.
type SandboxLeaseRecord struct {
	SandboxID string
	EnvdToken string
	Template  string
	State     string
	PausedAt  int64
	Unhydrated bool
	ExpiresAt int64
	Epoch     int64
}

// SandboxLeaseStore is the persistence port the E2B pool uses to make
// sandbox instances process-wide instead of per-pod.
type SandboxLeaseStore interface {
	GetSandboxLease(ctx context.Context, scopeKey string) (*SandboxLeaseRecord, error)
	AcquireSandboxLease(ctx context.Context, scopeKey, owner, sandboxID, envdToken, template string, ttl time.Duration) (record *SandboxLeaseRecord, acquired bool, err error)
	RenewSandboxLease(ctx context.Context, scopeKey, owner, sandboxID string, ttl time.Duration) (epoch int64, err error)
	ReplaceSandboxLease(ctx context.Context, scopeKey, owner, sandboxID, envdToken, template string, ttl time.Duration) (epoch int64, err error)
	SetSandboxLeaseState(ctx context.Context, scopeKey, owner, state string) error
	SetSandboxLeaseUnhydrated(ctx context.Context, scopeKey, owner, sandboxID string, unhydrated bool) error
	ReleaseSandboxLease(ctx context.Context, scopeKey, owner string, epoch int64) (bool, error)
}

// DefaultSandboxLeaseTTL is the lease TTL used when no explicit value is set.
const DefaultSandboxLeaseTTL = 15 * time.Minute
