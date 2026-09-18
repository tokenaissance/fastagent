package sandbox

import (
	"context"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// Executor abstracts a sandboxed execution environment for one user.
// All agent tool calls (exec, read_file, write_file, list_dir) are routed
// through this interface in cloud mode so that each user gets an isolated
// filesystem and runtime. Implementations can be Docker containers,
// Firecracker microVMs, E2B hosted sandboxes, or any other backend.
type Executor interface {
	// Exec runs a shell command and returns combined stdout+stderr.
	Exec(ctx context.Context, command string, timeout time.Duration) (string, error)
	// ReadFile reads a file from the sandbox filesystem.
	ReadFile(ctx context.Context, path string) (string, error)
	// WriteFile writes content to a file (creating parent dirs as needed).
	WriteFile(ctx context.Context, path, content string) (string, error)
	// ListDir lists a directory and returns a human-readable listing.
	ListDir(ctx context.Context, path string) (string, error)
	// Backend returns the short identifier of the underlying provider
	// ("docker", "e2b", "boxlite"). Used for log lines so operators can
	// confirm at a glance which provider handled a given exec.
	Backend() string
	// Close destroys the sandbox and releases resources.
	Close() error
}

// ExecutorPool manages per-(agent, project, session) sandbox lifecycles.
// Get lazily creates a sandbox on first access; Release tears it down.
//
// Why agentID + projectID + sessionID: parallel sessions of the same
// agent must not see each other's /workspace files (collision +
// cross-talk) — each gets its own container with a session-scoped
// bind mount. projectID overrides that isolation: every chat in the
// same project shares one container mounted on the project folder, so
// notes/files persist across the project's chats. Pool key is
// (agentID, projectID, sessionID); empty project + empty session is
// the agent-shared scope used by legacy callers.
type ExecutorPool interface {
	Get(ctx context.Context, agentID, projectID, sessionID string) (Executor, error)
	Release(agentID, projectID, sessionID string) error
	CloseAll()
	// Backend returns the short identifier of the underlying provider
	// ("docker", "e2b", "boxlite"). Mirrors Executor.Backend so callers
	// holding a pool handle don't have to lazily resolve an executor
	// just to learn the provider name.
	Backend() string
}

// StoreScope names the workspace-store scope a write landed in.
//
// It is a parameter rather than something the pool derives, because only the
// CALLER knows it: in a coding project session the file tools write the project
// root (session="") while the sandbox that mirrors the write is keyed by chat,
// and inferring one from the other is what produced three separate defects in a
// single session — the panel delete that addressed a key nobody used (docs 10 §4
// G21), the sync that forked every project file per chat (G17/A), and the stamp
// that silently stopped landing in project sessions (G22). The rule they share:
// the party that knows the key states it; nobody guesses at the arbitration
// point.
// StoreScope is the store scope a write-through belongs to. It is an alias of
// workspace.Scope on purpose: "which scope does this key live in" is the
// workspace package's fact (workspace.WriteScope is its one expression — docs
// 10 §4 G23), and an alias keeps this port from growing a second copy of it.
type StoreScope = workspace.Scope

// LiveExecutorPool is the optional read-only half of ExecutorPool: hand back the
// scope's CURRENT instance, or false, WITHOUT creating one.
//
// It exists for work that must never turn into a sandbox birth. The panel's file
// delete is the first such caller: removing a file from a scope nobody is
// running should cost nothing, and paying for a sandbox in order to delete a
// file inside it would be absurd (and would surprise the user with a new billable
// instance). A pool that does not implement this is treated as "no live
// instance" — the safe answer, because the store is the authority.
type LiveExecutorPool interface {
	LiveExecutor(agentID, projectID, sessionID string) (Executor, bool)
	// LiveProjectExecutors returns every live instance whose scope belongs to
	// (agentID, projectID) — the set one project write has to reach.
	//
	// A project's file tree is ONE tree, but its containers are per chat (that is
	// deliberate: concurrent chats must not share shell state), and the preview's
	// dev server may live in any of them — a sibling chat's, or the
	// project-addressed slot the console starts. Docker closes that gap with a
	// bind mount; a backend without one closes it with this set (docs 10 §4 G17,
	// option H: the store is the channel).
	LiveProjectExecutors(agentID, projectID string) []Executor
}

// LiveWorkspaceFileRemover is the narrow capability the file panel needs on the
// executor it is handed: delete one agent-relative store path's copy inside the
// scope's live sandbox, and do nothing when there is no live sandbox.
//
// It is deliberately not part of Executor: a backend whose /workspace IS the
// store (docker bind mount) must NOT implement it — there is no second copy, and
// the store delete is the whole job. Callers therefore type-assert, exactly the
// way they already do for WriteThroughExecutor.
type LiveWorkspaceFileRemover interface {
	RemoveLiveWorkspaceFile(ctx context.Context, storePath string) error
}

// WorkspaceSnapshotter is an optional capability an Executor can implement
// to support flush-on-evict. Returns a map of sandbox-relative path →
// file contents for everything under /workspace.
//
// Implementations are expected to be best-effort: a file's content should
// reflect what the sandbox sees at the time of the call, but perfect
// consistency with a live shell is not promised (agent should not be
// writing during flush). Large files / binaries are returned as-is.
//
// Not part of the base Executor interface because not every backend can
// cheaply enumerate its workspace (e.g. E2B requires an extra API call);
// callers should type-assert and skip gracefully when absent.
type WorkspaceSnapshotter interface {
	SnapshotWorkspace(ctx context.Context) (map[string][]byte, error)
}

// RemoteWorkspace marks executors whose /workspace is NOT shared with the
// host filesystem (no bind mount). Implementers need an explicit sync
// after every successful exec — otherwise files the skill writes
// inside the sandbox (e.g. image-tool's /workspace/gen_xxx.webp) never
// become visible to the host's workspace.Store and the UI breaks
// surfacing them. Docker doesn't implement this (its /workspace is a
// bind mount, files are on host the moment exec returns); E2B does.
type RemoteWorkspace interface {
	IsRemoteWorkspace()
}

// PortExposer is an optional Executor capability: given a port a process
// inside the sandbox is listening on, return an externally reachable URL.
// Docker publishes the port to a host port; E2B serves it at
// <port>-<sandboxID>.e2b.app with no extra step. The project runtime
// type-asserts this to build a live-preview URL; a backend that doesn't
// implement it can't host a preview (the runtime returns a clear error).
type PortExposer interface {
	ExposePort(ctx context.Context, port int) (string, error)
}

// TemplateProvisioner is an optional Executor capability: copy a local
// directory tree into the sandbox at destDir. Used to seed a coding
// template (e.g. shipany-tanstack) when the backend has no host bind
// mount to share it through. Docker doesn't implement it (it bind-mounts
// the template); remote backends (E2B) upload a tarball and extract it.
type TemplateProvisioner interface {
	ProvisionDir(ctx context.Context, localDir, destDir string) error
}

// PoolConfig holds configuration for creating sandbox pools.
type PoolConfig struct {
	Backend string // "docker", "e2b" (future)
	Image   string // container image (for docker backend)
	Policy  *Policy
	// E2B-specific fields (future)
	E2BTemplate string
	E2BAPIKey   string
}
