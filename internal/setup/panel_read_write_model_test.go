package setup

// The dashboard does not merge layers itself — the Go handlers do, through
// the same scope resolvers the runtime uses. These tests pin both halves of
// that: a PATCH writes only the namespaces it mentions, and a panel read
// returns exactly what the shared resolver says (so "what the panel shows"
// and "what runs" cannot diverge without failing here).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestUpdateConfigWritesOnlyTouchedNamespaces(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	ctx := context.Background()

	rec := httptest.NewRecorder()
	s.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, `{"sandbox":{"enabled":true}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config = %d (%s)", rec.Code, rec.Body.String())
	}

	rows, err := s.dataStore.ListConfigs(ctx, store.KindSetting, uid, "")
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "sandbox" {
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			names = append(names, r.Name)
		}
		t.Fatalf("PATCH wrote %v, want only [sandbox]", names)
	}

	// Mirror side: only the sandbox prefix may have rows, and the untouched
	// namespaces must not have been cleared either.
	kv, err := s.dataStore.ListConfigValues(ctx, store.KindSetting, "user", uid, "")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	for name := range kv {
		if len(name) < len("sandbox.") || name[:len("sandbox.")] != "sandbox." {
			t.Fatalf("unexpected mirror row %q", name)
		}
	}
}

// A namespace the caller did not mention keeps its existing value — the
// failure mode the sweep had was the opposite of this test's name.
func TestUpdateConfigLeavesUnmentionedNamespacesAlone(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	ctx := context.Background()

	if err := scope.SaveSetting(ctx, s.dataStore, uid, "", "memory",
		map[string]interface{}{"fts": map[string]interface{}{"enabled": true}}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleUpdateConfig(rec, cfgReq(t, http.MethodPost, uid, `{"sandbox":{"enabled":true}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/config = %d (%s)", rec.Code, rec.Body.String())
	}

	got, err := scope.Setting(ctx, s.dataStore, "memory", uid, "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	fts, ok := got["fts"].(map[string]interface{})
	if !ok || fts["enabled"] != true {
		t.Fatalf("unmentioned namespace was clobbered: %#v", got)
	}
}

// The panel's GET must be the resolver's answer, for both the merged fields
// and the enabled veto.
func TestPanelConfigMatchesRuntimeResolver(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	ctx := context.Background()

	if err := scope.SaveSetting(ctx, s.dataStore, "", "", "agents.defaults",
		map[string]interface{}{"model": "sys-model", "maxTokens": 111}); err != nil {
		t.Fatalf("seed system defaults: %v", err)
	}
	if err := scope.SaveSetting(ctx, s.dataStore, uid, "", "agents.defaults",
		map[string]interface{}{"model": "user-model"}); err != nil {
		t.Fatalf("seed user defaults: %v", err)
	}
	if err := scope.SaveProvider(ctx, s.dataStore, uid, "", "openai",
		config.ProviderConfig{APIKey: "sk-panel"}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleGetConfig(rec, cfgReq(t, http.MethodGet, uid, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Runtime resolver, same (user, agent) the panel is scoped to.
	want, err := scope.Setting(ctx, s.dataStore, "agents.defaults", uid, "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	agents, _ := body["agents"].(map[string]any)
	defaults, _ := agents["defaults"].(map[string]any)
	if defaults["model"] != want["model"] {
		t.Fatalf("panel model = %#v, resolver = %#v", defaults["model"], want["model"])
	}
	// The field only the system layer carries must survive the user layer.
	if int(defaults["maxTokens"].(float64)) != 111 {
		t.Fatalf("panel lost the inherited maxTokens: %#v", defaults)
	}
	wantProvs, err := scope.Providers(ctx, s.dataStore, uid, "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	provs, _ := body["providers"].(map[string]any)
	if _, ok := provs["openai"]; !ok || len(wantProvs) != len(provs) {
		t.Fatalf("panel providers = %#v, resolver = %#v", provs, wantProvs)
	}

	// Now veto the namespace at the user layer: the panel must show the same
	// empty answer the runtime would resolve.
	if err := s.dataStore.SaveConfig(ctx, &store.ConfigRecord{
		Kind: store.KindSetting, UserID: uid, Name: "agents.defaults",
		Enabled: false, Data: nil,
	}); err != nil {
		t.Fatalf("veto: %v", err)
	}
	want, err = scope.Setting(ctx, s.dataStore, "agents.defaults", uid, "")
	if err != nil {
		t.Fatalf("Setting after veto: %v", err)
	}
	if len(want) != 0 {
		t.Fatalf("veto did not clear the namespace: %#v", want)
	}
	rec = httptest.NewRecorder()
	s.handleGetConfig(rec, cfgReq(t, http.MethodGet, uid, ""))
	var after map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	afterDefaults, _ := after["agents"].(map[string]any)["defaults"].(map[string]any)
	if afterDefaults["model"] == "sys-model" {
		t.Fatalf("panel resurrected a vetoed namespace: %#v", afterDefaults)
	}
}
