package tools

// E2E（真机 E2B）：工具路径的删除必须"删得住"。
//
// 面板那半的对照实验在 internal/sandbox/e2b_live_panel_delete_test.go
// （TestE2BLivePanelDeleteSticks）：只删 store ⇒ 下一次 exec 触发同步 ⇒ 文件被
// 写回（"the loop did NOT reproduce" 那句断言就是这条链的证据）；删 store +
// LiveWorkspaceFileRemover ⇒ 站得住。
//
// 本文件把同一件事钉在**工具路径**上：apply_patch Delete 此前只删 store，沙箱
// 那份存活，于是同一条复活链成立（docs 10 §4 G7b、05 §8 的 d1 行）。
//
// NOT part of the normal suite — it creates a sandbox, so it is gated:
//
//	FASTAGENT_E2B_LIVE=1 E2B_API_KEY=e2b_... \
//	  go test ./internal/agent/tools/ -run TestE2BLiveApplyPatchDeleteSticks -v -count=1

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// Falsification: remove the `LiveWorkspaceFileRemover` block from
// deleteForPatchSandbox and this must fail — the sandbox copy survives, and one
// exec later the sync pushes the old content back into the store.
func TestE2BLiveApplyPatchDeleteSticks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	agent := fmt.Sprintf("live_ap_delete_%d", time.Now().UnixNano())
	const sessionID = "sess-delete"

	r, store, _, ex := liveToolSandbox(t, agent, sessionID, func(s *workspace.LocalFS) {
		if err := liveSeed(ctx, s, agent, sessionID, "app/notes.md", "old version\n"); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	})

	// 0. The hydrate delivered the file the patch is about to delete.
	if out, err := ex.Exec(ctx, "cat /workspace/app/notes.md", 60*time.Second); err != nil || !strings.Contains(out, "old version") {
		t.Fatalf("hydrate did not deliver app/notes.md: %v (%s)", err, out)
	}

	// 1. The agent deletes it through the tool — the path that used to stop at
	//    the store.
	if _, err := r.Execute(ctx, "apply_patch",
		"{\"input\":\"*** Begin Patch\\n*** Delete File: notes.md\\n*** End Patch\\n\"}"); err != nil {
		t.Fatalf("apply_patch delete: %v", err)
	}

	// 2. The store half is gone.
	if got := liveStoreKeys(t, ctx, store, agent, sessionID); len(got) != 0 {
		t.Fatalf("the store still holds %v after the tool delete", got)
	}

	// 3. The sandbox half is gone too — this is the behaviour under test.
	if out, err := ex.Exec(ctx, "test -e /workspace/app/notes.md && echo yes || echo no", 60*time.Second); err != nil {
		t.Fatalf("probe: %v (%s)", err, out)
	} else if strings.Contains(out, "yes") {
		t.Fatalf("the sandbox copy survived the tool delete: %s", out)
	}

	// 4. One command later the sync runs (same trigger the panel test uses). If
	//    the mirror had been skipped, this is where the old version comes back —
	//    which is the failure this whole path exists to prevent.
	if _, err := ex.Exec(ctx, "true", 60*time.Second); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := liveStoreKeys(t, ctx, store, agent, sessionID); len(got) != 0 {
		t.Fatalf("the delete was undone by the next sync: the store holds %v again", got)
	}
}
