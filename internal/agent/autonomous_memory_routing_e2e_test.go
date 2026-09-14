package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Cloud-path e2e for the 2026-09-14 production report: the operator's MEMORY.md
// edits failed with "system file get: store: not found" whenever the turn came
// from the agent's own cron job.
//
// Chain under test, all real except the message producer: bus message →
// Agent.chatterUserID (identity seam) → Registry.systemFileUserID → file tool →
// MemoryStoreAdapter → agent_files row. The store is a real DBStore, so the
// (agent_id, user_id, filename) keying that broke is the thing being asserted,
// not a mock's idea of it.
//
// Production shapes reproduced:
//   - the gateway mints a synthetic chatter for the sentinel BEFORE the loop
//     sees it (routing.go: "web:cron" → u_cd824…), so msg.UserID is u_xxx and
//     only msg.Source still identifies the turn as automatic;
//   - a direct producer (webhook-style) hands the raw sentinel to the loop.
const (
	cronE2EAgentID = "agt_cron_e2e"
	cronE2EOwnerID = "u_cron_owner"
	// The minted app_user the gateway created for "web:cron" in production.
	cronE2ESynthetic = "u_cd824b9bc93943e40f84"
)

func newCronE2EStore(t *testing.T) (*store.DBStore, context.Context) {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.CreateUser(ctx, &store.UserRecord{
		ID: cronE2EOwnerID, Username: "rain", Email: "rain@example.com",
		PasswordHash: "x", Role: "user", Status: "active",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	return db, ctx
}

// newCronE2ERegistry wires the file tools the way manager.go does for a
// cloud-hosted agent: store-backed identity files, owner + agent-owner stamped.
func newCronE2ERegistry(t *testing.T, st *store.DBStore) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry(t.TempDir(), "")
	reg.SetSystemFileStore(NewMemoryStoreAdapter(st), cronE2EAgentID)
	reg.SetOwnerUserID(cronE2EOwnerID)
	reg.SetAgentOwnerUserID(cronE2EOwnerID)
	return reg
}

func toolArgs(t *testing.T, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAutonomousTurnMemoryRouting_CloudPathE2E(t *testing.T) {
	db, ctx := newCronE2EStore(t)
	adapter := NewMemoryStoreAdapter(db)

	// The operator's memory: written by their own interactive turns, so the row
	// exists under THEIR user_id only.
	const seeded = "## 事实\n- 剔除稳定币/金本位：`USDC-USDT`、`XAUT-USDT`。\n"
	if err := adapter.SaveMemory(ctx, cronE2EAgentID, cronE2EOwnerID, seeded); err != nil {
		t.Fatalf("seed owner memory: %v", err)
	}

	cases := []struct {
		name string
		msg  bus.InboundMessage
	}{
		{
			name: "cron: gateway already minted the synthetic chatter",
			msg: bus.InboundMessage{
				Channel: "web", ChatID: "hJKMWwtOp3mJOtqN8Uz2mW",
				UserID: cronE2ESynthetic, OwnerUserID: cronE2EOwnerID,
				Source: bus.SourceCron,
			},
		},
		{
			name: "cron: raw sentinel straight from the producer",
			msg: bus.InboundMessage{
				Channel: "web", UserID: "cron", OwnerUserID: cronE2EOwnerID,
				Source: bus.SourceCron,
			},
		},
		{
			name: "heartbeat: tick carries no owner field",
			msg: bus.InboundMessage{
				Channel: "heartbeat", ChatID: "heartbeat_sakurain",
				UserID: "system", Source: bus.SourceHeartbeat,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := newCronE2ERegistry(t, db)
			// The identity seam the fix lives in. A bare Agent is enough: only
			// ownerUserID is read.
			a := &Agent{ownerUserID: cronE2EOwnerID}
			reg.SetChatterUserID(a.chatterUserID(tc.msg))

			out, err := reg.Execute(ctx, "edit_file", toolArgs(t, map[string]any{
				"path":       "MEMORY.md",
				"old_string": "`XAUT-USDT`。",
				"new_string": "`XAUT-USDT`。\n- ★ 2026-09-14 · 噪声地板预测【已验证】。\n",
			}))
			if err != nil {
				t.Fatalf("edit_file from an automatic turn: %v", err)
			}
			if !strings.Contains(out, "Edited") {
				t.Fatalf("unexpected tool output: %q", out)
			}

			// The finding landed in the operator's memory — the file their own
			// chats read.
			got, err := adapter.GetMemory(ctx, cronE2EAgentID, cronE2EOwnerID)
			if err != nil {
				t.Fatalf("owner memory after edit: %v", err)
			}
			if !strings.Contains(got, "噪声地板预测") {
				t.Errorf("owner MEMORY.md = %q, want the appended finding", got)
			}

			// Nothing was stranded in a synthetic row.
			for _, uid := range []string{cronE2ESynthetic, "cron", "system"} {
				if _, err := db.GetAgentFileExact(ctx, cronE2EAgentID, uid, "MEMORY.md"); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("row for synthetic chatter %q: err = %v, want ErrNotFound", uid, err)
				}
			}
		})
	}
}

// The other half of the same routing rule: a real visitor keeps its own
// private memory. The agent home's disk mirror and the owner's row must stay
// out of reach — that is why the disk fallback is owner-gated.
func TestVisitorTurnMemoryStaysPrivate_CloudPathE2E(t *testing.T) {
	db, ctx := newCronE2EStore(t)
	adapter := NewMemoryStoreAdapter(db)
	if err := adapter.SaveMemory(ctx, cronE2EAgentID, cronE2EOwnerID, "owner-secret\n"); err != nil {
		t.Fatalf("seed owner memory: %v", err)
	}

	// The agent home also holds the owner's un-scoped mirror, which is what a
	// disk fallback would have handed this visitor.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "MEMORY.md"), []byte("owner-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := tools.NewRegistry(home, "")
	reg.SetSystemFileStore(adapter, cronE2EAgentID)
	reg.SetOwnerUserID(cronE2EOwnerID)
	reg.SetAgentOwnerUserID(cronE2EOwnerID)

	const visitorID = "u_visitor"
	a := &Agent{ownerUserID: cronE2EOwnerID}
	reg.SetChatterUserID(a.chatterUserID(bus.InboundMessage{Channel: "telegram", UserID: visitorID}))

	out, err := reg.Execute(ctx, "read_file", toolArgs(t, map[string]any{"path": "MEMORY.md"}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if strings.Contains(out, "owner-secret") {
		t.Fatalf("visitor read the owner's MEMORY.md: %q", out)
	}

	if _, err := reg.Execute(ctx, "write_file", toolArgs(t, map[string]any{
		"path": "MEMORY.md", "content": "## 我的记忆\n- 喜欢简洁的回答\n",
	})); err != nil {
		t.Fatalf("write_file: %v", err)
	}

	if got, _ := adapter.GetMemory(ctx, cronE2EAgentID, cronE2EOwnerID); got != "owner-secret\n" {
		t.Errorf("owner MEMORY.md = %q, want it untouched by the visitor", got)
	}
	got, err := adapter.GetMemory(ctx, cronE2EAgentID, visitorID)
	if err != nil {
		t.Fatalf("visitor memory: %v", err)
	}
	if !strings.Contains(got, "我的记忆") {
		t.Errorf("visitor MEMORY.md = %q, want their own content", got)
	}
}
