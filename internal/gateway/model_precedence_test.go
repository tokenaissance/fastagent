package gateway

import "testing"

// The model precedence contract, as a test instead of a sentence.
//
// Why this exists: the settings model page's "switch did nothing" incident came
// from writing a layer the runtime does not use for that agent. The rule that
// decides it — agent-scope beats the user layers, and only a FOREIGN viewer's own
// explicit row is pinned above it — lived only in statement order inside
// loadUserSpace/EnsureAgent and in a prose table in the docs. Nothing failed if
// someone reordered those overlays.
//
// The cases below are exactly that table. If a change to the overlay order makes
// one of them fail, the failure names the layer that moved.
func TestResolveModelPrecedence(t *testing.T) {
	const (
		systemDefault = "system/model"
		userChoice    = "user/model"
		ownerChoice   = "owner/model"
		agentChoice   = "agent/model"
		viewerChoice  = "viewer/model"
	)
	cases := []struct {
		name                             string
		base, ownerRow, agentRow, pinRow string
		want                             string
	}{
		{
			name: "owner with no agent row inherits their user choice",
			base: userChoice, want: userChoice,
		},
		{
			name: "owner with no user row inherits the system default",
			base: systemDefault, want: systemDefault,
		},
		{
			// This is the case the settings page needed: for the OWNER the agent's
			// own row is the winner, which is why the page must write it.
			name: "owner: the agent row beats the caller's user row",
			base: userChoice, agentRow: agentChoice, want: agentChoice,
		},
		{
			name: "foreign viewer: the agent row beats the owner's user row",
			base: systemDefault, ownerRow: ownerChoice, agentRow: agentChoice, want: agentChoice,
		},
		{
			name: "foreign viewer: no agent row falls back to the owner's choice",
			base: systemDefault, ownerRow: ownerChoice, want: ownerChoice,
		},
		{
			// "MY tokens, MY model": the one layer that outranks the agent's own
			// configuration, and only because the viewer is paying for it.
			name: "foreign viewer: their own explicit row is pinned last",
			base: systemDefault, ownerRow: ownerChoice, agentRow: agentChoice, pinRow: viewerChoice, want: viewerChoice,
		},
		{
			// Preserved from EnsureAgent, where the pin is applied unconditionally
			// (it is empty unless the viewer set a row). Pinned here so the
			// extraction that introduced resolveModel did not quietly change it.
			name: "the pin is applied regardless of ownership",
			base: userChoice, agentRow: agentChoice, pinRow: viewerChoice, want: viewerChoice,
		},
		{
			name: "empty layers fall through to the base",
			base: systemDefault, want: systemDefault,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveModel(tc.base, tc.ownerRow, tc.agentRow, tc.pinRow); got != tc.want {
				t.Fatalf("resolveModel(base=%q owner=%q agent=%q pin=%q) = %q, want %q",
					tc.base, tc.ownerRow, tc.agentRow, tc.pinRow, got, tc.want)
			}
		})
	}
}
