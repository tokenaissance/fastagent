package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DockerExecutor wraps DockerSandbox to implement Executor. The container
// has the user's workspace mounted at /workspace and all tool calls are
// forwarded as docker exec commands.
type DockerExecutor struct {
	sb *DockerSandbox
}

// NewDockerExecutor creates a sandbox Executor backed by a Docker container.
// workspace is the host-side directory to mount (e.g. the user's workspace
// synced from S3, or a tmpdir for ephemeral use).
func NewDockerExecutor(image, workspace string, policy *Policy) (*DockerExecutor, error) {
	sb := NewDockerSandbox(image, workspace, policy)
	if err := sb.Create(); err != nil {
		return nil, fmt.Errorf("create docker sandbox: %w", err)
	}
	return &DockerExecutor{sb: sb}, nil
}

func (d *DockerExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	execCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return d.sb.Exec(execCtx, command, "/workspace")
}

func (d *DockerExecutor) ReadFile(ctx context.Context, path string) (string, error) {
	return d.sb.Exec(ctx, fmt.Sprintf("cat %s", shellQuote(path)), "/workspace")
}

func (d *DockerExecutor) WriteFile(ctx context.Context, path, content string) (string, error) {
	// Pipe content via stdin instead of argv. Heredoc-in-argv (the previous
	// implementation) sliced bytes into the docker-exec command line, which
	// fails with "fork/exec: invalid argument" the moment content contains
	// a NULL byte — every PNG, audio file, or other binary blob hits this
	// because execve rejects NULs inside argv elements. stdin sidesteps the
	// argv limit entirely.
	cmd := fmt.Sprintf("mkdir -p \"$(dirname %s)\" && cat > %s",
		shellQuote(path), shellQuote(path))
	out, err := d.sb.ExecWithStdin(ctx, cmd, "/workspace", strings.NewReader(content))
	if err != nil {
		return out, err
	}
	return fmt.Sprintf("Written to %s", path), nil
}

func (d *DockerExecutor) ListDir(ctx context.Context, path string) (string, error) {
	return d.sb.Exec(ctx, fmt.Sprintf("ls -la %s", shellQuote(path)), "/workspace")
}

func (d *DockerExecutor) Close() error {
	return d.sb.Close()
}

// SnapshotWorkspace walks the host-side mounted workspace dir (which is
// bind-mounted into the container at /workspace) and returns every
// regular file's bytes keyed by its container-relative path. Used by
// LifecyclePool for flush-on-evict so files the agent created via
// `exec` (not via write_file) still make it to the durable store.
//
// Walking the host dir directly is faster and more reliable than doing
// tar-over-exec: the mount already gives us a POSIX view of the same
// bytes the sandbox sees.
func (d *DockerExecutor) SnapshotWorkspace(ctx context.Context) (map[string][]byte, error) {
	root := d.sb.workspace
	if root == "" {
		return nil, nil
	}
	out := make(map[string][]byte)
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

// Ensure DockerExecutor satisfies the optional snapshot contract. A compile
// error here would flag any accidental interface drift.
var _ WorkspaceSnapshotter = (*DockerExecutor)(nil)

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// DockerExecutorPool manages per-user DockerExecutor instances.
type DockerExecutorPool struct {
	mu        sync.Mutex
	executors map[string]*DockerExecutor
	image     string
	policy    *Policy
	// workspaceRoot is the base dir; each user gets workspaceRoot/{userID}/
	workspaceRoot string
}

// NewDockerExecutorPool creates a pool of Docker-backed executors.
func NewDockerExecutorPool(image, workspaceRoot string, policy *Policy) *DockerExecutorPool {
	if image == "" {
		image = "fastclaw/sandbox:latest"
	}
	return &DockerExecutorPool{
		executors:     make(map[string]*DockerExecutor),
		image:         image,
		policy:        policy,
		workspaceRoot: workspaceRoot,
	}
}

func (p *DockerExecutorPool) Get(ctx context.Context, agentID string) (Executor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if ex, ok := p.executors[agentID]; ok {
		return ex, nil
	}

	// workspaceRoot is FASTCLAW_HOME (~/.fastclaw or env override). The
	// per-agent workspace lives under workspaces/<agentID>/, NOT at the
	// root + agentID — that earlier path mounted runtime/imgany into
	// the container, which doesn't exist, instead of
	// runtime/workspaces/imgany which is where the agent runtime
	// actually writes files.
	workspace := filepath.Join(p.workspaceRoot, "workspaces", agentID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir %s: %w", workspace, err)
	}

	// Build the sandbox by hand so we can wire skill mounts BEFORE
	// Create() bakes the docker run args. Constructing through
	// NewDockerExecutor would call Create immediately on a sandbox
	// that hasn't been told about skill dirs.
	sb := NewDockerSandbox(p.image, workspace, p.policy)
	sb.SetSkillDirs(skillDirsForAgent(p.workspaceRoot, agentID))
	if err := sb.Create(); err != nil {
		return nil, fmt.Errorf("create docker sandbox: %w", err)
	}
	ex := &DockerExecutor{sb: sb}
	p.executors[agentID] = ex
	return ex, nil
}

func (p *DockerExecutorPool) Release(userID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ex, ok := p.executors[userID]; ok {
		delete(p.executors, userID)
		return ex.Close()
	}
	return nil
}

func (p *DockerExecutorPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for uid, ex := range p.executors {
		ex.Close()
		delete(p.executors, uid)
	}
}

// Ensure interfaces are satisfied.
var (
	_ Executor     = (*DockerExecutor)(nil)
	_ ExecutorPool = (*DockerExecutorPool)(nil)
)
