package setup

// GET /api/providers is the scope-keyed editor view: it lists the rows owned by
// exactly one (scope, scopeId), reading the legacy blob (listConfigsByScope →
// store.ListConfigs) with no merge and no configs_kv fallback. These tests pin
// both halves of that decision, because they point in opposite directions:
//
//   - the enabled flag MUST be reported. Since the "one enabled semantics" round,
//     a disabled provider row is a real statement the runtime honours (it drops
//     the provider and vetoes every outer entry of the same name), so an editor
//     that cannot show the flag would present a state it cannot represent — and
//     the channels list next door already reports it.
//   - a mirror-only provider MUST NOT be invented as a row. This endpoint is a
//     CRUD surface: callers PUT/DELETE by the returned `id`, which is a blob-row
//     attribute a mirror row does not have. The resolved, merged view is
//     /api/config's job (and scope.Providers' for the runtime); see
//     TestPanelReadModelMatchesRuntimeResolver for the mirror-only provider.

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

// listScopedProviders drives the handler and returns name → enabled.
func listScopedProviders(t *testing.T, s *Server, uid string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/providers?scope="+scope.User+"&scopeId="+uid, nil)
	req = stampAuthAndUserID(req, uid)
	rec := httptest.NewRecorder()
	s.handleListProviders(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/providers = %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make(map[string]any, len(resp.Providers))
	for _, p := range resp.Providers {
		name, _ := p["name"].(string)
		enabled, ok := p["enabled"]
		if !ok {
			t.Fatalf("provider %q has no enabled field: %#v", name, p)
		}
		out[name] = enabled
	}
	return out
}

func TestListProvidersReportsEnabled(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	ctx := context.Background()

	if err := scope.SaveProvider(ctx, s.dataStore, uid, "", "openai",
		config.ProviderConfig{APIKey: "sk-on"}); err != nil {
		t.Fatalf("seed enabled provider: %v", err)
	}
	// A disabled row is what the audit's "provider enabled semantics" round made
	// meaningful; no HTTP path writes one yet, so seed it directly.
	if err := scope.SaveProviderState(ctx, s.dataStore, uid, "", "parked",
		config.ProviderConfig{APIKey: "sk-off"}, false); err != nil {
		t.Fatalf("seed disabled provider: %v", err)
	}

	got := listScopedProviders(t, s, uid)
	if got["openai"] != true {
		t.Errorf("openai enabled = %#v, want true", got["openai"])
	}
	if got["parked"] != false {
		t.Errorf("parked enabled = %#v, want false — the editor must be able to show a row the runtime ignores", got["parked"])
	}

	// The runtime's answer for the same scope, for contrast: the disabled row is
	// gone from the resolved view while the editor still lists it.
	provs, err := scope.Providers(ctx, s.dataStore, uid, "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := provs["parked"]; ok {
		t.Errorf("runtime resolved a disabled provider: %#v", provs)
	}
}

func TestListProvidersDoesNotInventMirrorOnlyRows(t *testing.T) {
	s, uid, _ := setupFileUploadTest(t)
	ctx := context.Background()

	// A provider that exists only in the mirror (the state a KV-only writer, or
	// a pre-transaction half-write, leaves behind).
	for name, val := range map[string]interface{}{
		"mirror_only.api_key":  "sk-mirror",
		"mirror_only.api_base": "https://mirror.example",
	} {
		if err := s.dataStore.SetConfigValue(ctx, store.KindProvider, scope.User, uid, name,
			store.EncodeConfigValue(val)); err != nil {
			t.Fatalf("seed mirror row %s: %v", name, err)
		}
	}

	if got := listScopedProviders(t, s, uid); len(got) != 0 {
		t.Errorf("editor listed mirror-only rows it cannot address by id: %#v", got)
	}

	// ...while the resolved view the runtime uses does see it.
	provs, err := scope.Providers(ctx, s.dataStore, uid, "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if _, ok := provs["mirror_only"]; !ok {
		t.Errorf("scope.Providers lost the mirror-only provider: %#v", provs)
	}
}
