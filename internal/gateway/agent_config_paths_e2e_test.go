package gateway

/**
 * [INPUT]: one real store (sqlite), one real Gateway, and the write functions
 *          the HTTP handlers and the agent tools call.
 * [OUTPUT]: a pass over every write path an agent-config change can take, each
 *           followed by the read the runtime makes, plus the counter check that
 *           makes the protocol testable.
 * [POS]: End-to-end for docs/fastagent/design/15-agent-config-consistency.md
 *        §7: "pod A changes config → the next command in the SAME session reads
 *        the new rc". The handlers and the tool layer sit above the functions
 *        here (panel_read_write_model_test.go covers that seam), so this file
 *        covers the layer where the invariant actually lives.
 * [PROTOCOL]: On change, update this header, then check §4 (the read and write
 *        protocols) and the register row that introduced the epoch counter.
 */

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agentconfig"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// TestAgentConfigWritePathsAreVisibleToTheRuntimeRead walks every write path.
//
// The order is the resolution order on purpose: system, then user, then the
// agent row, then the agent-scope settings row. Each step asserts the value the
// runtime read returns AND that the counter moved, because a write that does
// not move the counter is a write the cache cannot see.
func TestAgentConfigWritePathsAreVisibleToTheRuntimeRead(t *testing.T) {
	db := openEpochStore(t)
	ctx := context.Background()
	g := &Gateway{store: db}
	// The invalidation choke points bump the counter only when the user-space
	// registry is wired, which is how every real composition root builds the
	// gateway. An empty registry is enough: the paths below never build a space.
	g.users = newUserSpaceRegistry(nil, db, nil, nil, nil, nil, nil, nil)

	const agentID, userID = "agt_paths", "u_paths"
	if err := db.SaveAgent(ctx, &store.AgentRecord{ID: agentID, UserID: userID, Name: "paths"}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	read := func(t *testing.T) config.ResolvedAgent {
		t.Helper()
		rc, err := g.resolvedAgentFor(ctx, agentconfig.Scope{UserID: userID, AgentID: agentID})
		if err != nil {
			t.Fatalf("runtime read: %v", err)
		}
		return rc
	}
	epoch := func(t *testing.T) int64 {
		t.Helper()
		v, err := db.CurrentConfigEpoch(ctx)
		if err != nil {
			t.Fatalf("read counter: %v", err)
		}
		return v
	}

	// Fill the cache. Every step below then proves a WRITE is visible, not that
	// the cache was empty.
	if _, err := g.resolvedAgentFor(ctx, agentconfig.Scope{UserID: userID, AgentID: agentID}); err != nil {
		t.Fatalf("warm read: %v", err)
	}

	steps := []struct {
		name  string
		write func(t *testing.T)
		check func(t *testing.T, rc config.ResolvedAgent)
	}{
		{
			name: "system agents.defaults (the dashboard default)",
			write: func(t *testing.T) {
				must(t, scope.SaveSetting(ctx, db, "", "", "agents.defaults",
					map[string]interface{}{"model": "sys/model"}))
				must(t, g.NotifySystemReload())
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				want(t, "model", rc.Model, "sys/model")
			},
		},
		{
			name: "user agents.defaults (this user's own choice)",
			write: func(t *testing.T) {
				must(t, scope.SaveSetting(ctx, db, userID, "", "agents.defaults",
					map[string]interface{}{"model": "user/model"}))
				g.InvalidateUser(userID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				// A user row beats the system row, and it is the caller's own
				// scope, so the agent row below still outranks it.
				want(t, "model", rc.Model, "user/model")
			},
		},
		{
			name: "agent row agents.config (the layer-3 row the loader serves)",
			write: func(t *testing.T) {
				if err := db.SaveAgent(ctx, &store.AgentRecord{
					ID: agentID, UserID: userID, Name: "paths",
					Config: map[string]interface{}{"model": "row/model", "maxTokens": 4096},
				}); err != nil {
					t.Fatalf("save agent row: %v", err)
				}
				g.InvalidateAgent(agentID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				want(t, "model", rc.Model, "row/model")
				wantInt(t, "maxTokens", rc.MaxTokens, 4096)
			},
		},
		{
			name: "agent-scope agents.defaults (the settings page and `agents config set`)",
			write: func(t *testing.T) {
				must(t, scope.SaveSettingByScope(ctx, db, scope.Agent, agentID, "agents.defaults",
					map[string]interface{}{
						"model":        "agent/model",
						"maxTokens":    2048,
						"promptMode":   config.PromptModeChatbot,
						"splitReplies": true,
						"autoPersist":  true,
					}))
				g.InvalidateAgent(agentID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				want(t, "model", rc.Model, "agent/model")
				wantInt(t, "maxTokens", rc.MaxTokens, 2048)
				want(t, "promptMode", rc.PromptMode, config.PromptModeChatbot)
				if rc.SplitReplies == nil || !*rc.SplitReplies {
					t.Fatalf("splitReplies = %v; want the agent row's explicit true", rc.SplitReplies)
				}
				if rc.AutoPersist == nil || !*rc.AutoPersist {
					t.Fatalf("autoPersist = %v; want the agent row's explicit true", rc.AutoPersist)
				}
			},
		},
		{
			name: "agent-scope provider credential",
			write: func(t *testing.T) {
				must(t, scope.SaveProviderByScope(ctx, db, scope.Agent, agentID, "e2e",
					config.ProviderConfig{APIBase: "https://e2e.example/v1", APIKey: "e2e-key"}))
				g.InvalidateAgent(agentID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				got, ok := rc.Providers["e2e"]
				if !ok {
					t.Fatalf("agent-scope provider missing; providers = %v", rc.Providers)
				}
				want(t, "provider key", got.APIKey, "e2e-key")
			},
		},
		{
			name: "provider delete (the dashboard's remove button)",
			write: func(t *testing.T) {
				rec, err := db.GetConfigByName(ctx, store.KindProvider, "", agentID, "e2e")
				if err != nil || rec == nil {
					t.Fatalf("load the provider row: %v", err)
				}
				must(t, scope.DeleteProvider(ctx, db, rec))
				g.InvalidateAgent(agentID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				if _, still := rc.Providers["e2e"]; still {
					t.Fatalf("a deleted provider still resolves: %v", rc.Providers)
				}
			},
		},
		{
			name: "MCP server row (the per-key table)",
			write: func(t *testing.T) {
				must(t, db.AddMCPServer(ctx, agentID, "e2e-mcp",
					config.MCPServerConfig{Type: "http", URL: "https://mcp.e2e.example/x"}))
				g.InvalidateAgent(agentID)
			},
			check: func(t *testing.T, rc config.ResolvedAgent) {
				got, ok := rc.MCPServers["e2e-mcp"]
				if !ok {
					t.Fatalf("MCP row missing; servers = %v", rc.MCPServers)
				}
				want(t, "MCP url", got.URL, "https://mcp.e2e.example/x")
			},
		},
	}

	previous := epoch(t)
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.write(t)

			after := epoch(t)
			if after <= previous {
				t.Fatalf("counter did not move: %d then %d", previous, after)
			}
			previous = after

			step.check(t, read(t))
		})
	}

	t.Run("steady state rebuilds nothing", func(t *testing.T) {
		before := g.mustStats(t)
		read(t)
		read(t)
		after := g.mustStats(t)

		if after.Rebuilds != before.Rebuilds {
			t.Fatalf("rebuilds moved without a write: %d then %d", before.Rebuilds, after.Rebuilds)
		}
		if after.Checks < before.Checks+2 {
			t.Fatalf("checks = %d; want one per read on top of %d", after.Checks, before.Checks)
		}
		if after.StaleHits != before.StaleHits {
			t.Fatalf("staleHits moved without a write: %d then %d", before.StaleHits, after.StaleHits)
		}
	})

	t.Run("a write cannot land without moving the version, choke point or not", func(t *testing.T) {
		// Before P0b this case was the opposite: a row written straight to the
		// store left the counter alone, so the cached read kept the old value
		// until someone called an invalidation choke point. The choke point is
		// no longer part of the correctness argument — the write stamps inside
		// its own transaction — so the value is visible with no notification at
		// all. The choke point still matters for the OTHER cache (the built
		// UserSpace) and for other replicas.
		must(t, scope.SaveSettingByScope(ctx, db, scope.Agent, agentID, "agents.defaults",
			map[string]interface{}{"model": "no-notify/model"}))

		if got := read(t); got.Model != "no-notify/model" {
			t.Fatalf("model = %q; the write did not stamp its own version", got.Model)
		}
	})
}

func (g *Gateway) mustStats(t *testing.T) agentconfig.Stats {
	t.Helper()
	snapshot, ok := g.AgentConfigCacheSnapshot(context.Background())
	if !ok {
		t.Fatal("AgentConfigCacheSnapshot reported no cache")
	}
	return snapshot.Stats
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

func want(t *testing.T, what, got, expected string) {
	t.Helper()
	if got != expected {
		t.Fatalf("%s = %q; want %q", what, got, expected)
	}
}

func wantInt(t *testing.T, what string, got, expected int) {
	t.Helper()
	if got != expected {
		t.Fatalf("%s = %d; want %d", what, got, expected)
	}
}
