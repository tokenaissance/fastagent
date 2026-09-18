package tools

// E2E（真机 E2B）：一条逻辑路径 = 一个 store 键，且它的沙箱副本在"该键映射到的
// 那个路径"上（docs 01 §8、§3.5）。本文件是 01 §8 修复的验收测试。
//
// E2E (real E2B): one logical path is ONE store key, and its sandbox copy lives
// at the path that key maps to (docs 01 §8). This is the acceptance test for
// the 01 §8 fix.
//
// 单元测试（apply_patch_path_scope_test.go / write_through_signal_test.go）钉的是
// 键的计算；这里钉的是这个计算在一个真沙箱上的两个可观察后果：
//
//	1. 宿主写入之后，沙箱里"读得到"新内容（穿透真的落到 hydrate 的那条路径上）；
//	2. store 里只有那一个键（apply_patch 不再产生第二个"根键"对象），
//	   且 /workspace/<裸路径> 不存在 —— 那就是修复前"一份文件两个键"的可观察形状。
//
// NOT part of the normal suite — it creates a sandbox, so it is gated:
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/agent/tools/ -run TestE2BLiveOnePathIsOneKey -v -count=1

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// liveToolSandbox brings up a real E2B-backed lifecycle pool over a fresh temp
// store, plus a Registry wired exactly the way bindSession wires it for a
// coding preview homed in a chat session (no project): file tools scoped into
// the app subdir, and the executor handed to the registry being the pool's
// lazy proxy — the shape production uses.
//
// The scope is session-addressed on purpose. That is the shape where the
// sandbox scope and the file tools' store scope are the SAME scope, so the
// hydrate and the mirror are talking about the same tree; a project-scoped
// (a project session) store scope collapses while the pool keeps the
// chat scope, which is a separate open question recorded in docs 01 §8.
func liveToolSandbox(t *testing.T, agent, sessionID string, seed func(store *workspace.LocalFS)) (*Registry, *workspace.LocalFS, *sandbox.LifecyclePool, sandbox.Executor) {
	t.Helper()
	if os.Getenv("FASTAGENT_E2B_LIVE") != "1" {
		t.Skip("live E2B: set FASTAGENT_E2B_LIVE=1 with E2B_API_KEY to run")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("E2B_API_KEY is required")
	}
	template := os.Getenv("FASTAGENT_E2B_TEMPLATE")
	if template == "" {
		template = os.Getenv("E2B_TEMPLATE")
	}
	if template == "" {
		template = "base"
	}

	store := workspace.NewLocalFS(t.TempDir())
	// Seed BEFORE the pool exists: this is what makes the hydrate deliver these
	// objects into the fresh sandbox (the incident's t₀).
	seed(store)

	inner := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	inner.SetWorkspace(store)
	lp := sandbox.NewLifecyclePool(inner, time.Hour, time.Hour)
	lp.SetWorkspace(store)
	t.Cleanup(func() { lp.CloseAll() })

	ex, err := lp.Get(context.Background(), agent, "", sessionID)
	if err != nil {
		t.Fatalf("sandbox up: %v", err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	t.Cleanup(r.Close)
	r.SetWorkspaceStore(store, agent)
	r.SetSessionID(sessionID)
	r.SetExecutor(ex)
	// The coding shape: the app lives in a subdir, so every tool path is
	// prefixed with it on its way to the store (scopeSessionID + wsPath).
	r.SetCodingSubdir("app")
	return r, store, lp, ex
}

func TestE2BLiveOnePathIsOneKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	agent := fmt.Sprintf("live_tool_scope_%d", time.Now().UnixNano())
	const sessionID = "sess-scope"

	r, store, _, ex := liveToolSandbox(t, agent, sessionID, func(s *workspace.LocalFS) {
		if err := liveSeed(ctx, s, agent, sessionID, "app/notes.md", "one\n"); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	})

	// 0. The hydrate delivered the seeded object at the path its key maps to.
	if out, err := ex.Exec(ctx, "cat /workspace/app/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "one") {
		t.Fatalf("hydrate did not deliver app/notes.md: %v (%s)", err, out)
	}

	// 1. write_file → store key "app/notes.md" + the mirror reaches the sandbox.
	if _, err := r.Execute(ctx, "write_file", `{"path":"notes.md","content":"two\n"}`); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if got := liveStoreKeys(t, ctx, store, agent, sessionID); !equalKeys(got, []string{"app/notes.md"}) {
		t.Fatalf("after write_file the store keys are %v; want exactly [app/notes.md]", got)
	}
	if out, err := ex.Exec(ctx, "cat /workspace/app/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "two") {
		t.Fatalf("the write-through did not reach the sandbox: %v (%s)", err, out)
	}

	// 2. apply_patch on the same logical path: same key, same sandbox path.
	out, err := r.Execute(ctx, "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Update File: notes.md\\n@@\\n-two\\n+three\\n*** End Patch\\n\"}")
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	t.Logf("apply_patch result: %s", out)

	if got := liveStoreKeys(t, ctx, store, agent, sessionID); !equalKeys(got, []string{"app/notes.md"}) {
		t.Fatalf("the patch left a second key behind: store keys = %v; want exactly [app/notes.md]\n"+
			"  (before 2026-09-18 the patch wrote the raw path, so a root-level notes.md appeared next to app/notes.md;\n"+
			"   see docs/文件系统形式化证明/01-current-implementation.md §8)", got)
	}
	if got := liveStoreRead(t, ctx, store, agent, sessionID, "app/notes.md"); !strings.Contains(got, "three") {
		t.Fatalf("the store holds %q; want the patched content", got)
	}
	if out, err := ex.Exec(ctx, "cat /workspace/app/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "three") {
		t.Fatalf("the patch's write-through did not reach the sandbox: %v (%s)", err, out)
	}

	// 3. The pre-fix shape must not exist: no bare /workspace/notes.md, and the
	// tools all read back the same bytes.
	if out, err := ex.Exec(ctx, "test -e /workspace/notes.md && echo yes || echo no", 60*time.Second); err != nil {
		t.Fatalf("probe: %v (%s)", err, out)
	} else if strings.Contains(out, "yes") {
		t.Errorf("a root-level /workspace/notes.md exists — the patch and the mirror disagree about the path again")
	}
	got, err := r.Execute(ctx, "read_file", `{"path":"notes.md"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(got, "three") {
		t.Errorf("read_file returned %q; want the content the patch just wrote — one file, one key, one version", got)
	}
}

func liveSeed(ctx context.Context, st workspace.Store, agent, sessionID, path, body string) error {
	return liveSeedAt(ctx, st, agent, "", sessionID, path, body)
}

func liveStoreKeys(t *testing.T, ctx context.Context, st workspace.Store, agent, sessionID string) []string {
	t.Helper()
	return liveStoreKeysAt(t, ctx, st, agent, "", sessionID)
}

func liveStoreRead(t *testing.T, ctx context.Context, st workspace.Store, agent, sessionID, path string) string {
	t.Helper()
	return liveStoreReadAt(t, ctx, st, agent, "", sessionID, path)
}

func liveSeedAt(ctx context.Context, st workspace.Store, agent, projectID, sessionID, path, body string) error {
	return st.Put(ctx, agent, projectID, sessionID, path, strings.NewReader(body), int64(len(body)), "text/plain")
}

func liveStoreKeysAt(t *testing.T, ctx context.Context, st workspace.Store, agent, projectID, sessionID string) []string {
	t.Helper()
	objs, err := st.List(ctx, agent, projectID, sessionID)
	if err != nil {
		t.Fatalf("store list: %v", err)
	}
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Path)
	}
	sort.Strings(out)
	return out
}

func liveStoreReadAt(t *testing.T, ctx context.Context, st workspace.Store, agent, projectID, sessionID, path string) string {
	t.Helper()
	rc, err := st.Get(ctx, agent, projectID, sessionID, path)
	if err != nil {
		t.Fatalf("store get %s: %v", path, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("store read %s: %v", path, err)
	}
	return string(b)
}

func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// 01 §8 的同一族，但在沙箱那一侧——**2026-09-18 决策 A 之后的样子**。
//
// 事实（全部码上可查）：
//   - 文件工具在项目会话里写项目根（workspace.WriteScope）：scopeSessionID() == ""；
//   - 沙箱池的 scope 由 bindSession 建：pool.Get(agent, projectID, chatSession)（每个 chat 一个实例）；
//   - hydrate 在 projectID != "" 时按 session="" 列整个项目（e2b_executor.go:739）；
//   - **syncSnapshot 现在也折叠**（`syncStoreScope`）：回写落项目根，与 hydrate/工具同一个键。
//
// 本测试钉的是 A 之后的三个性质：
//
//	① 一次 exec 之后不再产生"chat 子目录副本"（旧行为会给每个项目文件复制一份）；
//	② exec 新建的文件直接落项目根 ⇒ **工具/read_file 立刻看得见**（旧行为落 chat 子目录，工具看不见）；
//	③ 沙箱改既有路径仍被拒（这是设计，不是本轮改动）——而且拒绝是拿**工具读的那个键**在比，
//	   所以 agent 收到的 `NOT synced` 与它读的文件是同一条路径。
//
// This is the post-decision shape. The pre-A behaviour (a duplicate under the chat
// subtree, and a sandbox-born file the tools could not see) was pinned by an
// earlier version of this test and by TestE2BLiveProjectScopeIsNotTheSandboxScope's
// as-built assertions; the decision changed, so the assertions change with it
// (docs 01 §8.2, 10 §4 G17).
func TestE2BLiveProjectSessionKeepsOneTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if os.Getenv("FASTAGENT_E2B_LIVE") != "1" {
		t.Skip("live E2B: set FASTAGENT_E2B_LIVE=1 with E2B_API_KEY to run")
	}
	apiKey := os.Getenv("E2B_API_KEY")
	if apiKey == "" {
		t.Fatal("E2B_API_KEY is required")
	}
	template := os.Getenv("FASTAGENT_E2B_TEMPLATE")
	if template == "" {
		template = os.Getenv("E2B_TEMPLATE")
	}
	if template == "" {
		template = "base"
	}

	agent := fmt.Sprintf("live_one_tree_%d", time.Now().UnixNano())
	const projectID = "proj-one-tree"
	const chatID = "chat-one-tree"

	store := workspace.NewLocalFS(t.TempDir())
	// The object lives where the FILE TOOLS address it: the project root.
	if err := liveSeedAt(ctx, store, agent, projectID, "", "app/notes.md", "one\n"); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	inner := sandbox.NewE2BExecutorPool(apiKey, template, t.TempDir(), 10*time.Minute)
	inner.SetWorkspace(store)
	lp := sandbox.NewLifecyclePool(inner, time.Hour, time.Hour)
	lp.SetWorkspace(store)
	defer lp.CloseAll()

	ex, err := lp.Get(ctx, agent, projectID, chatID)
	if err != nil {
		t.Fatalf("sandbox up: %v", err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.SetWorkspaceStore(store, agent)
	r.SetProjectID(projectID)
	r.SetSessionID(chatID)
	r.SetExecutor(ex)
	r.SetCodingSubdir("app")

	// ① hydrate still collapses (it always did): the project root's object is in
	// the sandbox at the path its key maps to.
	if out, err := ex.Exec(ctx, "test -e /workspace/app/notes.md && echo yes || echo no", 60*time.Second); err != nil {
		t.Fatalf("probe: %v (%s)", err, out)
	} else if !strings.Contains(out, "yes") {
		t.Fatalf("the project root's object was NOT hydrated (%s)", out)
	}

	// ② An exec runs (carrying the post-exec sync), and a NEW file appears in the
	// sandbox. It must land at the project root: the key the tools read.
	artOut, err := ex.Exec(ctx, "echo artefact > /workspace/app/from_exec.txt && cat /workspace/app/from_exec.txt", 60*time.Second)
	if err != nil || !strings.Contains(artOut, "artefact") {
		t.Fatalf("exec artefact: %v (%s)", err, artOut)
	}
	projectKeys := liveStoreKeysAt(t, ctx, store, agent, projectID, "")
	chatKeys := liveStoreKeysAt(t, ctx, store, agent, projectID, chatID)
	t.Logf("project-root scope keys: %v; chat scope keys: %v", projectKeys, chatKeys)
	if !equalKeys(projectKeys, []string{"app/from_exec.txt", "app/notes.md"}) {
		t.Fatalf("project root = %v; want the seeded file plus the exec artefact, and nothing else", projectKeys)
	}
	if len(chatKeys) != 0 {
		t.Fatalf("chat scope = %v; want nothing — the sync must not fork the project tree per chat (decision A)", chatKeys)
	}
	// And the tools see what the sandbox produced (this is what A bought).
	if got, err := r.Execute(ctx, "read_file", `{"path":"from_exec.txt"}`); err != nil {
		t.Fatalf("read_file of the exec artefact: %v", err)
	} else if !strings.Contains(got, "artefact") {
		t.Fatalf("read_file returned %q; want the sandbox's file — that is the point of A", got)
	}

	// ③ A sandbox edit of an existing path is still refused, and the refusal is
	// about the key the tools read (the project root).
	editOut, err := ex.Exec(ctx, "printf 'SANDBOX EDIT\\n' >> /workspace/app/notes.md && cat /workspace/app/notes.md", 60*time.Second)
	if err != nil {
		t.Fatalf("sandbox edit: %v (%s)", err, editOut)
	}
	if !strings.Contains(editOut, "NOT synced") {
		t.Fatalf("the refusal was not delivered to the agent:\n%s", editOut)
	}
	if got := liveStoreReadAt(t, ctx, store, agent, projectID, "", "app/notes.md"); strings.Contains(got, "SANDBOX EDIT") {
		t.Fatalf("the project root took the sandbox's edit (%q) — the conservative refusal is gone", got)
	}
}
