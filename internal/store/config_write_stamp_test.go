package store

import (
	"context"
	"errors"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// TestAWriteThatFailsLeavesTheCounterAlone is the witness for C5: the content
// and the version commit together, so a write that does not happen must not
// move the version either. Before P0b the counter moved in a separate
// transaction, so a reader could see a version with no content behind it (or
// content with no version, which is the same defect seen from the other side).
func TestAWriteThatFailsLeavesTheCounterAlone(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	agentID := "agt_stamp_c5"
	cfg := config.MCPServerConfig{Type: "http", URL: "https://mcp.example/x"}

	before, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}

	if err := db.AddMCPServer(ctx, agentID, "first", cfg); err != nil {
		t.Fatalf("first add: %v", err)
	}
	afterFirst, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if afterFirst <= before {
		t.Fatalf("counter did not move on a successful write: %d then %d", before, afterFirst)
	}

	// The duplicate violates the primary key, so the transaction rolls back.
	if err := db.AddMCPServer(ctx, agentID, "first", cfg); !errors.Is(err, ErrMCPServerExists) {
		t.Fatalf("duplicate add = %v; want ErrMCPServerExists", err)
	}
	afterFailed, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if afterFailed != afterFirst {
		t.Fatalf("counter moved on a failed write: %d then %d", afterFirst, afterFailed)
	}
	servers, err := db.ListMCPServers(ctx, agentID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %+v; want exactly the one row that succeeded", servers)
	}
}

// TestDeleteAlsoStamps covers the other direction: a delete that succeeds moves
// the version, and a delete that finds nothing moves neither the version nor the
// rows. The MCP control plane reads through the versioned cache, so a removal
// that does not stamp keeps serving the removed server.
func TestDeleteAlsoStamps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	agentID := "agt_stamp_delete"

	if err := db.AddMCPServer(ctx, agentID, "victim", config.MCPServerConfig{Type: "http", URL: "https://x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}

	if err := db.DeleteMCPServer(ctx, agentID, "victim"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	after, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if after <= before {
		t.Fatalf("counter did not move on delete: %d then %d", before, after)
	}

	if err := db.DeleteMCPServer(ctx, agentID, "never-there"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete = %v; want ErrNotFound", err)
	}
	afterMissing, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if afterMissing != after {
		t.Fatalf("counter moved on a delete that found nothing: %d then %d", after, afterMissing)
	}
}

// TestSaveAgentStamps covers the layer-3 row: the CLI and the dashboard both
// save through this method, so the stamp belongs to the method rather than to
// the caller.
func TestSaveAgentStamps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	before, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if err := db.SaveAgent(ctx, &AgentRecord{ID: "agt_stamp_row", UserID: "u_stamp", Name: "row"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	after, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if after <= before {
		t.Fatalf("counter did not move on an agent save: %d then %d", before, after)
	}
}
