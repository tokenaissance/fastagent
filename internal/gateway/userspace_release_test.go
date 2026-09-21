package gateway

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// Dropping a user space used to release nothing: the MCP clients the space owns
// — a stdio server is a subprocess, a standing notification stream is a
// goroutine plus a connection — were left to the garbage collector, which does
// not own either (docs 10 §3.4, row 44). These two tests pin the release and the
// two reasons it must not be immediate: the write that drops a space can come
// from inside a turn, and a turn is the only thing that calls an MCP tool.
func TestADroppedSpacesMcpClientsAreReleasedAfterTheGrace(t *testing.T) {
	sp, pid := spaceWithAStdioMCPServer(t, "u_release")
	r := newUserSpaceRegistry(nil, nil, nil, nil, nil, nil, nil, nil)
	base := time.Unix(1_800_000_000, 0)
	r.now = func() time.Time { return base }
	r.spaces[sp.UserID] = &userSpaceEntry{space: sp, lastUsed: base.Add(-time.Hour)}

	// The drop path an admin write / `mcp add` takes.
	r.invalidate(sp.UserID)
	if !processAlive(pid) {
		t.Fatalf("the drop killed the MCP server (%d) immediately: a turn that is still "+
			"running would lose its tools mid-call", pid)
	}
	if n := r.releaseRetired(); n != 0 {
		t.Fatalf("release sweep closed %d spaces inside the grace period; want 0", n)
	}
	if !processAlive(pid) {
		t.Fatal("inside the grace period the stdio server must still be there")
	}

	// Past the grace, with nothing running: the release happens.
	r.now = func() time.Time { return base.Add(releaseGrace + time.Second) }
	if n := r.releaseRetired(); n != 1 {
		t.Fatalf("release sweep closed %d spaces past the grace period; want 1", n)
	}
	waitForDeath(t, pid)
	if n := r.releaseRetired(); n != 0 {
		t.Fatalf("the release ran twice; a second sweep closed %d more", n)
	}
}

func TestASpaceWithATurnInFlightKeepsItsClientsPastTheGrace(t *testing.T) {
	sp, pid := spaceWithAStdioMCPServer(t, "u_busy")
	sess := sp.Agents.AgentByID("agt_release").Sessions().Get("telegram", "bot1", "chat1", "")
	if sess == nil {
		t.Fatal("no session for the agent; the in-flight signal would never be exercised")
	}
	sess.BeginTurn()

	r := newUserSpaceRegistry(nil, nil, nil, nil, nil, nil, nil, nil)
	base := time.Unix(1_800_000_000, 0)
	r.now = func() time.Time { return base }
	r.spaces[sp.UserID] = &userSpaceEntry{space: sp, lastUsed: base.Add(-time.Hour)}
	r.invalidate(sp.UserID)

	r.now = func() time.Time { return base.Add(releaseGrace + time.Hour) }
	if n := r.releaseRetired(); n != 0 {
		t.Fatalf("release sweep closed %d spaces while a turn was in flight; want 0", n)
	}
	if !processAlive(pid) {
		t.Fatal("the MCP server was released while a turn was in flight")
	}

	sess.EndTurn()
	if n := r.releaseRetired(); n != 1 {
		t.Fatalf("release sweep closed %d spaces after the turn ended; want 1", n)
	}
	waitForDeath(t, pid)
}

// spaceWithAStdioMCPServer builds a real UserSpace whose single agent connects
// to a real stdio MCP server, and returns the server's pid. Real because the
// fact under test is "this OS process is gone": a fake client would only prove
// that a method was called.
func spaceWithAStdioMCPServer(t *testing.T, uid string) (*UserSpace, int) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-mcp-server.sh")
	pidFile := filepath.Join(dir, "pid")
	// The id is echoed rather than hard-coded: the agent asks for tools/list
	// more than once (registration and ToolDefs), and an answer carrying the
	// wrong id is an answer the client waits past forever.
	const body = `#!/bin/sh
printf '%s\n' "$$" > "$1"
while read -r line; do
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"tools/list"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}\n' "$id" ;;
    *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}\n' "$id" ;;
    *) printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id" ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write the fake MCP server: %v", err)
	}
	db := newGatewayConfigStore(t)
	mgr, err := agent.NewManager([]config.ResolvedAgent{{
		ID: "agt_release", UserID: uid, Home: dir,
		Workspace: filepath.Join(dir, "ws"), Model: "fake-model",
		MaxTokens: 64, Temperature: 0.7, MaxToolIterations: 1,
		MCPServers: map[string]config.MCPServerConfig{
			"fake": {Type: "stdio", Command: "/bin/sh", Args: []string{script, pidFile}},
		},
	}}, nil, bus.New(),
		agent.WithUserID(uid),
		agent.WithSessionStore(session.NewStoreAdapter(db, uid)),
	)
	if err != nil {
		t.Fatalf("agent manager: %v", err)
	}
	return &UserSpace{UserID: uid, Agents: mgr}, waitForPid(t, pidFile)
}

func waitForPid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the fake MCP server never reported its pid (%s)", pidFile)
	return 0
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitForDeath(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the MCP server process %d is still alive after the release", pid)
}
