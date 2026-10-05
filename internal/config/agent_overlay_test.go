package config

import "testing"

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

// TestApplyAgentDefaultsCarriesEveryField is the witness for the field list.
// Two gateway call sites apply an agent-scope agents.defaults row, and each one
// used to carry its own copy of this list: a field added to one copy and not
// the other silently broke per-agent settings for one attach path. The test
// names every field, so dropping one here fails and cannot pass unnoticed.
func TestApplyAgentDefaultsCarriesEveryField(t *testing.T) {
	rc := ResolvedAgent{
		MaxTokens:                 100,
		Temperature:               0.1,
		MaxToolIterations:         3,
		MaxToolIterationContinues: 1,
		MaxParallelToolCalls:      1,
		Thinking:                  "off",
		PolicyPreset:              "default",
		PromptMode:                PromptModeAgent,
	}
	row := AgentDefaults{
		Model:                     "agent/model",
		MaxTokens:                 4096,
		Temperature:               0.7,
		MaxToolIterations:         20,
		MaxToolIterationContinues: intPtr(0),
		MaxParallelToolCalls:      4,
		Thinking:                  "high",
		PolicyPreset:              "strict",
		PromptMode:                PromptModeChatbot,
		SplitReplies:              boolPtr(true),
		AutoPersist:               boolPtr(true),
	}

	model := ApplyAgentDefaults(&rc, row)

	if model != "agent/model" {
		t.Fatalf("model = %q; want the row's model returned to the caller", model)
	}
	if rc.Model != "" {
		t.Fatalf("rc.Model = %q; ApplyAgentDefaults must not write the model", rc.Model)
	}
	if rc.MaxTokens != 4096 || rc.Temperature != 0.7 || rc.MaxToolIterations != 20 ||
		rc.MaxParallelToolCalls != 4 {
		t.Fatalf("numeric fields not applied: %+v", rc)
	}
	if rc.MaxToolIterationContinues != 0 {
		t.Fatalf("MaxToolIterationContinues = %d; an explicit 0 must win", rc.MaxToolIterationContinues)
	}
	if rc.Thinking != "high" || rc.PolicyPreset != "strict" || rc.PromptMode != PromptModeChatbot {
		t.Fatalf("string fields not applied: %+v", rc)
	}
	if rc.SplitReplies == nil || !*rc.SplitReplies {
		t.Fatalf("SplitReplies not applied: %v", rc.SplitReplies)
	}
	if rc.AutoPersist == nil || !*rc.AutoPersist {
		t.Fatalf("AutoPersist not applied: %v", rc.AutoPersist)
	}
}

// TestApplyAgentDefaultsLeavesUnsetFieldsAlone pins the other half of the
// contract: a row that carries no opinion must not overwrite the value a lower
// layer supplied.
func TestApplyAgentDefaultsLeavesUnsetFieldsAlone(t *testing.T) {
	rc := ResolvedAgent{
		MaxTokens:                 100,
		Temperature:               0.1,
		MaxToolIterations:         3,
		MaxToolIterationContinues: 2,
		MaxParallelToolCalls:      1,
		Thinking:                  "low",
		PolicyPreset:              "default",
		PromptMode:                PromptModeAgent,
		SplitReplies:              boolPtr(false),
		AutoPersist:               boolPtr(false),
	}

	if model := ApplyAgentDefaults(&rc, AgentDefaults{}); model != "" {
		t.Fatalf("empty row returned model %q; want empty", model)
	}
	want := ResolvedAgent{
		MaxTokens:                 100,
		Temperature:               0.1,
		MaxToolIterations:         3,
		MaxToolIterationContinues: 2,
		MaxParallelToolCalls:      1,
		Thinking:                  "low",
		PolicyPreset:              "default",
		PromptMode:                PromptModeAgent,
		SplitReplies:              boolPtr(false),
		AutoPersist:               boolPtr(false),
	}
	if rc.MaxTokens != want.MaxTokens ||
		rc.Temperature != want.Temperature ||
		rc.MaxToolIterations != want.MaxToolIterations ||
		rc.MaxToolIterationContinues != want.MaxToolIterationContinues ||
		rc.MaxParallelToolCalls != want.MaxParallelToolCalls ||
		rc.Thinking != want.Thinking ||
		rc.PolicyPreset != want.PolicyPreset ||
		rc.PromptMode != want.PromptMode ||
		rc.SplitReplies == nil || *rc.SplitReplies ||
		rc.AutoPersist == nil || *rc.AutoPersist {
		t.Fatalf("an empty row changed the resolved agent: %+v", rc)
	}
}

// TestMergeAgentProvidersOverlaysWithoutDropping pins both halves of the merge:
// the agent's own rows win, and the rows below them survive.
func TestMergeAgentProvidersOverlaysWithoutDropping(t *testing.T) {
	rc := ResolvedAgent{Providers: map[string]ProviderConfig{
		"system": {APIKey: "sys"},
		"shared": {APIKey: "user"},
	}}

	MergeAgentProviders(&rc, map[string]ProviderConfig{
		"shared": {APIKey: "agent"},
		"own":    {APIKey: "agent-only"},
	})

	if got := rc.Providers["system"].APIKey; got != "sys" {
		t.Fatalf("system provider lost: %q", got)
	}
	if got := rc.Providers["shared"].APIKey; got != "agent" {
		t.Fatalf("agent provider did not win: %q", got)
	}
	if got := rc.Providers["own"].APIKey; got != "agent-only" {
		t.Fatalf("agent-only provider missing: %q", got)
	}
}

// TestMergeAgentProvidersCreatesTheMap covers an agent that has no provider row
// below it: the overlay must build the map rather than panic or drop the row.
func TestMergeAgentProvidersCreatesTheMap(t *testing.T) {
	var rc ResolvedAgent
	MergeAgentProviders(&rc, map[string]ProviderConfig{"own": {APIKey: "agent-only"}})
	if got := rc.Providers["own"].APIKey; got != "agent-only" {
		t.Fatalf("provider row dropped: %q", got)
	}
}
