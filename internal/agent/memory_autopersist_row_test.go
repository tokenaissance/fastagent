package agent

// The `memory` namespace on the production path.
//
// Same family as row 49 (piiScrubbing) and row 51 (skillsLearner): the row was
// writable through the settings API / CLI, readable back, and — on the path
// production actually builds — read by nobody, because the only reader was
// NewAgentWithFullCfg, a constructor no production path calls. The per-agent
// field even *documented* an inherit ("nil = inherit system
// MemoryCfg.AutoPersist.Enabled") that could not happen.
//
// The Manager now hands the resolved row to the shared constructor, which
// stamps it as the default layer and lets the per-agent override win over it.
// These two tests pin the precedence (including `false` as a veto) and the pass
// the switch is about.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func memoryGateAgent(t *testing.T, mem config.MemoryCfg, override *bool) *Agent {
	t.Helper()
	base := t.TempDir()
	t.Setenv("FASTAGENT_HOME", base)
	home := filepath.Join(base, "agents", "agt_mem", "agent")
	rc := config.ResolvedAgent{
		ID: "agt_mem", UserID: "u_owner", Home: home,
		Workspace: filepath.Join(home, "workspace"), Model: "fake-model",
		MaxTokens: 128, Temperature: 0.7, MaxToolIterations: 2,
		AutoPersist: override,
	}
	mgr, err := NewManager([]config.ResolvedAgent{rc}, &learnerProbeProvider{reply: "ok"}, bus.New(),
		WithUserID("u_owner"), WithMemory(mem))
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	ag := mgr.AgentByID(rc.ID)
	if ag == nil {
		t.Fatal("agent not built")
	}
	return ag
}

func TestTheMemoryRowIsTheDefaultLayerAndThePerAgentFlagOverridesIt(t *testing.T) {
	on := config.MemoryCfg{AutoPersist: config.AutoPersistCfg{
		Enabled: true, EveryNTurns: 20, Model: "distiller",
	}}
	tr, fa := true, false

	cases := []struct {
		name      string
		mem       config.MemoryCfg
		override  *bool
		wantOn    bool
		wantTurns int
		wantModel string
		explain   string
	}{
		{"row off, no override", config.MemoryCfg{}, nil, false, 5, "",
			"the default is off and the cadence still gets a usable value"},
		{"row on, no override", on, nil, true, 20, "distiller",
			"nil now really means inherit — the contract the per-agent field documents"},
		{"row on, per-agent false", on, &fa, false, 20, "distiller",
			"a per-agent false is a veto against a system-level on"},
		{"row off, per-agent true", config.MemoryCfg{}, &tr, true, 5, "",
			"the per-agent flag still turns it on alone (the pre-existing path)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ag := memoryGateAgent(t, tc.mem, tc.override)
			got := ag.memoryCfg.AutoPersist
			if got.Enabled != tc.wantOn {
				t.Errorf("enabled = %v, want %v — %s", got.Enabled, tc.wantOn, tc.explain)
			}
			if got.EveryNTurns != tc.wantTurns {
				t.Errorf("everyNTurns = %d, want %d", got.EveryNTurns, tc.wantTurns)
			}
			if got.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", got.Model, tc.wantModel)
			}
		})
	}
}

// The pass the switch turns on really does write the durable files. Pinned here
// because "the gate fired" and "something was remembered" are different facts,
// and only the first one is observable from the gate's log line.
func TestAutoPersistMemoryWritesMemoryAndUserFiles(t *testing.T) {
	home := t.TempDir()
	mem := NewMemory(home)
	prov := &learnerProbeProvider{
		reply: `{"memory_facts":["the release goes out on fridays"],"user_notes":["prefers terse summaries"]}`,
	}

	AutoPersistMemory(context.Background(), mem, prov, "distiller",
		[]provider.Message{{Role: "user", Content: "ship it on friday, and keep the summary short"}})

	gotMem := mem.LoadMemory()
	if !strings.Contains(gotMem, "Auto-persisted") || !strings.Contains(gotMem, "the release goes out on fridays") {
		t.Errorf("MEMORY.md did not receive the extracted fact:\n%s", gotMem)
	}
	gotUser := mem.LoadUserFile()
	if !strings.Contains(gotUser, "Auto-persisted") || !strings.Contains(gotUser, "prefers terse summaries") {
		t.Errorf("USER.md did not receive the extracted note:\n%s", gotUser)
	}
}
