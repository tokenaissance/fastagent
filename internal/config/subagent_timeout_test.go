package config

import "testing"

// The sub-agent wall budget is configuration, so it travels the same path as
// every other agent knob: a system default in Agents.Defaults, overridable per
// agent. Locking the precedence here is what lets the agent loop stop reading
// ambient state (it used to consult an environment variable at call time, which
// no settings layer and no panel could see).
func TestMergedAgentConfigCarriesSubagentTimeout(t *testing.T) {
	t.Run("system default reaches the agent", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agents.Defaults.SubagentTimeoutSec = 1800
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a"}, nil)
		if resolved.SubagentTimeoutSec != 1800 {
			t.Fatalf("SubagentTimeoutSec = %d, want the system default 1800", resolved.SubagentTimeoutSec)
		}
	})

	t.Run("per-agent override wins", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agents.Defaults.SubagentTimeoutSec = 1800
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a", SubagentTimeoutSec: 600}, nil)
		if resolved.SubagentTimeoutSec != 600 {
			t.Fatalf("SubagentTimeoutSec = %d, want the entry's 600", resolved.SubagentTimeoutSec)
		}
	})

	t.Run("unset leaves zero so the built-in default applies", func(t *testing.T) {
		cfg := &Config{}
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a"}, nil)
		if resolved.SubagentTimeoutSec != 0 {
			t.Fatalf("SubagentTimeoutSec = %d, want 0 (agent falls back to its built-in default)", resolved.SubagentTimeoutSec)
		}
	})
}
