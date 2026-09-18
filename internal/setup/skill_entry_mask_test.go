package setup

// The panel renders secrets masked (`maskSkillEntry`), the dialog posts the
// whole entry back, and the mask must therefore never reach storage: a saved
// but untouched secret stays the real key. These tests pin the rule on BOTH
// write paths — the global skills.entries namespace sweep and the per-agent
// override row — because they are two different write mechanisms (docs 10 §4,
// G16: before this, neither of them had the guard, and the per-agent helper
// written for exactly this job had no caller).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
)

func postConfig(t *testing.T, s *Server, uid, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config = %d (%s)", rec.Code, rec.Body.String())
	}
	return rec
}

func storedEntries(t *testing.T, s *Server, uid, agentID string) map[string]config.SkillEntryCfg {
	t.Helper()
	out := map[string]config.SkillEntryCfg{}
	if err := scope.SettingInto(context.Background(), s.dataStore, "skills.entries", uid, agentID, &out); err != nil {
		t.Fatalf("read skills.entries (uid=%q agent=%q): %v", uid, agentID, err)
	}
	return out
}

// The global path: POST {"skills":{"entries":…}} goes through the namespace
// sweep, where the body overlays the loaded config before it is written.
func TestMaskedGlobalSkillSecretKeepsTheStoredValue(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)

	postConfig(t, s, uid, `{"skills":{"entries":{"arc":{"enabled":true,"apiKey":"sk-real-key","env":{"ARC_TOKEN":"tok-real"}}}}}`)
	before := storedEntries(t, s, uid, "")
	if before["arc"].APIKey != "sk-real-key" || before["arc"].Env["ARC_TOKEN"] != "tok-real" {
		t.Fatalf("seed did not land: %+v", before["arc"])
	}

	// The operator re-saves the dialog without retyping the secret: the body
	// carries the mask.
	postConfig(t, s, uid, `{"skills":{"entries":{"arc":{"enabled":true,"apiKey":"****-key","env":{"ARC_TOKEN":"****real"}}}}}`)
	after := storedEntries(t, s, uid, "")
	if after["arc"].APIKey != "sk-real-key" {
		t.Fatalf("a masked API key overwrote the stored one: %q", after["arc"].APIKey)
	}
	if after["arc"].Env["ARC_TOKEN"] != "tok-real" {
		t.Fatalf("a masked env secret overwrote the stored one: %q", after["arc"].Env["ARC_TOKEN"])
	}

	// A real new value still wins (the guard must not freeze the field).
	postConfig(t, s, uid, `{"skills":{"entries":{"arc":{"enabled":true,"apiKey":"sk-rotated","env":{"ARC_TOKEN":"tok-rotated"}}}}}`)
	rotated := storedEntries(t, s, uid, "")
	if rotated["arc"].APIKey != "sk-rotated" || rotated["arc"].Env["ARC_TOKEN"] != "tok-rotated" {
		t.Fatalf("a rotated secret was not stored: %+v", rotated["arc"])
	}
}

// The per-agent path: POST {"skills":{"agentEntries":{"<agent>":…}}} is written
// by saveAgentSkillEntries, a second mechanism with the same rule.
func TestMaskedAgentSkillSecretKeepsTheStoredValue(t *testing.T) {
	s, uid, aid := setupFileUploadTest(t)

	seed := fmt.Sprintf(`{"skills":{"agentEntries":{%q:{"arc":{"enabled":true,"apiKey":"sk-agent-real"}}}}}`, aid)
	postConfig(t, s, uid, seed)
	if got := storedEntries(t, s, uid, aid)["arc"].APIKey; got != "sk-agent-real" {
		t.Fatalf("per-agent seed did not land: %q", got)
	}

	masked := fmt.Sprintf(`{"skills":{"agentEntries":{%q:{"arc":{"enabled":true,"apiKey":"****real"}}}}}`, aid)
	postConfig(t, s, uid, masked)
	if got := storedEntries(t, s, uid, aid)["arc"].APIKey; got != "sk-agent-real" {
		t.Fatalf("a masked per-agent key overwrote the stored one: %q", got)
	}

	rotated := fmt.Sprintf(`{"skills":{"agentEntries":{%q:{"arc":{"enabled":true,"apiKey":"sk-agent-rotated"}}}}}`, aid)
	postConfig(t, s, uid, rotated)
	if got := storedEntries(t, s, uid, aid)["arc"].APIKey; got != "sk-agent-rotated" {
		t.Fatalf("a rotated per-agent secret was not stored: %q", got)
	}
}

// The rule itself, as a pure function: masked keeps what storage had, a plain
// value overwrites, and an explicitly cleared field stays cleared.
func TestMergeSkillEntriesSemantics(t *testing.T) {
	existing := map[string]config.SkillEntryCfg{
		"arc": {Enabled: true, APIKey: "sk-real", Env: map[string]string{"TOKEN": "tok"}},
	}
	got := mergeSkillEntries(existing, map[string]config.SkillEntryCfg{
		"arc": {Enabled: true, APIKey: "****real", Env: map[string]string{"TOKEN": "tok-new"}},
	})
	if got["arc"].APIKey != "sk-real" {
		t.Fatalf("masked apiKey = %q; want the stored key", got["arc"].APIKey)
	}
	if got["arc"].Env["TOKEN"] != "tok-new" {
		t.Fatalf("a plain env value must overwrite: %q", got["arc"].Env["TOKEN"])
	}

	plain := mergeSkillEntries(existing, map[string]config.SkillEntryCfg{"arc": {APIKey: "sk-new"}})
	if plain["arc"].APIKey != "sk-new" {
		t.Fatalf("plain apiKey = %q; want the incoming key", plain["arc"].APIKey)
	}

	cleared := mergeSkillEntries(existing, map[string]config.SkillEntryCfg{"arc": {APIKey: ""}})
	if cleared["arc"].APIKey != "" {
		t.Fatalf("clearing a key must stay possible: %q", cleared["arc"].APIKey)
	}

	// A brand-new entry has nothing to fall back to.
	fresh := mergeSkillEntries(existing, map[string]config.SkillEntryCfg{"new": {APIKey: "sk-fresh"}})
	if fresh["new"].APIKey != "sk-fresh" {
		t.Fatalf("new entry lost its key: %q", fresh["new"].APIKey)
	}
}

// Sanity: the mask the rule keys on is the one the GET path actually produces.
func TestMaskedValueIsWhatThePanelSends(t *testing.T) {
	entry := maskSkillEntry(config.SkillEntryCfg{Enabled: true, APIKey: "sk-real-key"})
	if !strings.Contains(entry.APIKey, "****") {
		t.Fatalf("maskSkillEntry produced %q; the guard keys on '****'", entry.APIKey)
	}
	if !isMaskedSecret(entry.APIKey) {
		t.Fatalf("the GET mask %q is not recognised by isMaskedSecret", entry.APIKey)
	}
}
