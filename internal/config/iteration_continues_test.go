package config

import "testing"

// The auto-continuation budget is the one agent knob where 0 is a real value
// ("never extend a turn") rather than "unset", so it travels as a pointer
// through every layer and lands as an int on the resolved agent. This locks the
// precedence, because getting it wrong means either a turn that silently keeps
// spending or an operator who cannot turn the behavior off.
func TestMergedAgentConfigCarriesIterationContinues(t *testing.T) {
	intPtr := func(n int) *int { return &n }

	t.Run("default is one extra segment", func(t *testing.T) {
		cfg := &Config{}
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a"})
		if resolved.MaxToolIterationContinues != DefaultToolIterationContinues {
			t.Fatalf("MaxToolIterationContinues = %d, want %d", resolved.MaxToolIterationContinues, DefaultToolIterationContinues)
		}
	})

	t.Run("explicit zero in defaults means never", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agents.Defaults.MaxToolIterationContinues = intPtr(0)
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a"})
		if resolved.MaxToolIterationContinues != 0 {
			t.Fatalf("MaxToolIterationContinues = %d, want 0 — 0 must be expressible", resolved.MaxToolIterationContinues)
		}
	})

	t.Run("per-agent override wins over defaults", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agents.Defaults.MaxToolIterationContinues = intPtr(0)
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a", MaxToolIterationContinues: intPtr(3)})
		if resolved.MaxToolIterationContinues != 3 {
			t.Fatalf("MaxToolIterationContinues = %d, want the entry's 3", resolved.MaxToolIterationContinues)
		}
	})

	t.Run("per-agent zero turns it off too", func(t *testing.T) {
		cfg := &Config{}
		cfg.Agents.Defaults.MaxToolIterationContinues = intPtr(2)
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a", MaxToolIterationContinues: intPtr(0)})
		if resolved.MaxToolIterationContinues != 0 {
			t.Fatalf("MaxToolIterationContinues = %d, want 0", resolved.MaxToolIterationContinues)
		}
	})

	t.Run("chatbot mode does not extend itself", func(t *testing.T) {
		cfg := &Config{}
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a", PromptMode: PromptModeChatbot})
		if resolved.MaxToolIterationContinues != 0 {
			t.Fatalf("MaxToolIterationContinues = %d, want 0 in chatbot mode (same reasoning as the 5-round clamp)", resolved.MaxToolIterationContinues)
		}
	})

	t.Run("chatbot mode still honors an explicit setting", func(t *testing.T) {
		cfg := &Config{}
		resolved := cfg.MergedAgentConfig(AgentEntry{ID: "agt-1", Name: "a", PromptMode: PromptModeChatbot, MaxToolIterationContinues: intPtr(1)})
		if resolved.MaxToolIterationContinues != 1 {
			t.Fatalf("MaxToolIterationContinues = %d, want the explicit 1", resolved.MaxToolIterationContinues)
		}
	})
}
