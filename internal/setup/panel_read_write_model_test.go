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
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// parityNamespaces is the panel-visible slice of the settings table: the
// storage namespace and the wire path the dashboard reads it from. The paths
// are written out here rather than taken from settingNamespaces so a handler
// that starts answering a namespace under the wrong JSON key fails this
// test instead of agreeing with itself.
var parityNamespaces = []struct {
	namespace string
	path      []string
}{
	{"agents.defaults", []string{"agents", "defaults"}},
	{"sandbox", []string{"sandbox"}},
	{"tools.categories", []string{"tools"}},
	{"memory", []string{"memory"}},
	{"prefs", []string{"prefs"}},
}

// TestPanelReadModelMatchesRuntimeResolver is the two-paths-one-answer test:
// the panel handler is path A, an independent assembly that resolves each
// namespace with the single-namespace readers (scope.Setting, not the batch
// form the handler calls) is path B. Every case below seeds a different
// storage situation — layering, veto, mirror-only, stale mirror, disabled
// provider/channel — and both paths must produce the same view.
//
// A case is added by seeding rows; the comparison itself is not per-case, so
// a new combination cannot accidentally get a weaker check than the others.
func TestPanelReadModelMatchesRuntimeResolver(t *testing.T) {
	seedSystemView := func(t *testing.T, s *Server, uid string) {
		t.Helper()
		ctx := context.Background()
		if err := scope.SaveSetting(ctx, s.dataStore, "", "", "agents.defaults", map[string]interface{}{
			"model": "sys-model", "maxTokens": 111, "temperature": 0.5, "maxToolIterations": 7,
		}); err != nil {
			t.Fatalf("seed system defaults: %v", err)
		}
		if err := scope.SaveSetting(ctx, s.dataStore, "", "", "sandbox", map[string]interface{}{
			"enabled": true, "backend": "boxlite",
		}); err != nil {
			t.Fatalf("seed system sandbox: %v", err)
		}
		// The key is the point: a data segment with an underscore has to
		// survive both the blob and the mirror byte-for-byte.
		if err := scope.SaveSetting(ctx, s.dataStore, "", "", "tools.categories", map[string]interface{}{
			"web_search": map[string]interface{}{"primary": "searxng"},
		}); err != nil {
			t.Fatalf("seed system tools.categories: %v", err)
		}
		if err := scope.SaveProvider(ctx, s.dataStore, "", "", "openai",
			config.ProviderConfig{APIKey: "sk-system", APIBase: "https://system.example"}); err != nil {
			t.Fatalf("seed system provider: %v", err)
		}
		if err := scope.SaveChannel(ctx, s.dataStore, "", "", "telegram", "bot-system", true,
			config.ChannelConfig{BotToken: "tok-system"}); err != nil {
			t.Fatalf("seed system channel: %v", err)
		}
	}

	cases := []struct {
		name string
		seed func(t *testing.T, s *Server, uid string)
		// check states the outcome this case exists for, independently of
		// the two-path comparison above. The comparison catches the panel
		// and the resolvers disagreeing; this catches both of them being
		// wrong together.
		check func(t *testing.T, panel map[string]any)
	}{
		{"system only", seedSystemView, func(t *testing.T, panel map[string]any) {
			if got := digPath(t, panel, []string{"agents", "defaults", "model"}); got != "sys-model" {
				t.Errorf("system model = %#v, want sys-model", got)
			}
			primary := digPath(t, panel, []string{"tools", "web_search", "primary"})
			if primary != "searxng" {
				t.Errorf("tools.web_search.primary = %#v, want searxng", primary)
			}
			if _, ok := providerNames(t, panel)["openai"]; !ok {
				t.Errorf("openai missing from providers: %#v", providerNames(t, panel))
			}
			if _, ok := channelNames(t, panel)["telegram"]; !ok {
				t.Errorf("telegram missing from channels: %#v", channelNames(t, panel))
			}
		}},
		{"user overrides the system layer", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			ctx := context.Background()
			if err := scope.SaveSetting(ctx, s.dataStore, uid, "", "agents.defaults", map[string]interface{}{
				"model": "user-model", "maxTokens": 222, "temperature": 0.9, "maxToolIterations": 9,
			}); err != nil {
				t.Fatalf("seed user defaults: %v", err)
			}
			if err := scope.SaveSetting(ctx, s.dataStore, uid, "", "prefs", map[string]interface{}{
				"timezone": "Asia/Shanghai",
			}); err != nil {
				t.Fatalf("seed user prefs: %v", err)
			}
			if err := scope.SaveProvider(ctx, s.dataStore, uid, "", "openai",
				config.ProviderConfig{APIKey: "sk-user"}); err != nil {
				t.Fatalf("seed user provider: %v", err)
			}
		}, func(t *testing.T, panel map[string]any) {
			if got := digPath(t, panel, []string{"agents", "defaults", "model"}); got != "user-model" {
				t.Errorf("model = %#v, want the user layer to win", got)
			}
			if got := digPath(t, panel, []string{"prefs", "timezone"}); got != "Asia/Shanghai" {
				t.Errorf("prefs.timezone = %#v", got)
			}
		}},
		{"user layer vetoes a namespace", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			// A disabled row is the layer's decision that the namespace is
			// empty — the outer layer's sys-model must not come back. Written
			// through the dual-write so the marker carries the decision, the
			// way a real caller writes it.
			if err := scope.SaveSettingState(context.Background(), s.dataStore, uid, "", "agents.defaults", nil, false); err != nil {
				t.Fatalf("veto agents.defaults: %v", err)
			}
		}, func(t *testing.T, panel map[string]any) {
			if got := digPath(t, panel, []string{"agents", "defaults", "model"}); got != nil {
				t.Errorf("vetoed namespace came back with model = %#v", got)
			}
		}},
		{"namespace lives only in the mirror", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			// A live namespace (the `memory` row still exists after the FTS
			// removal, register #52) seeded ONLY as a mirror row: the panel
			// must serve it from the mirror after the blob layer.
			seedMirrorValue(t, s.dataStore, store.KindSetting, "system", "", "memory.auto_persist.enabled", true)
		}, func(t *testing.T, panel map[string]any) {
			if got := digPath(t, panel, []string{"memory", "autoPersist", "enabled"}); got != true {
				t.Errorf("mirror-only namespace not served: memory.autoPersist.enabled = %#v", got)
			}
		}},
		{"blob wins over a stale mirror row", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			// The mirror disagrees with the blob on purpose: the blob is
			// authoritative, so both paths must answer "enabled".
			seedMirrorValue(t, s.dataStore, store.KindSetting, "system", "", "sandbox.enabled", false)
		}, func(t *testing.T, panel map[string]any) {
			if got := digPath(t, panel, []string{"sandbox", "enabled"}); got != true {
				t.Errorf("sandbox.enabled = %#v; the stale mirror row overrode the blob", got)
			}
		}},
		{"provider lives only in the mirror", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			seedMirrorValue(t, s.dataStore, store.KindProvider, "user", uid, "mirror_only.api_key", "sk-mirror")
			seedMirrorValue(t, s.dataStore, store.KindProvider, "user", uid, "mirror_only.api_base", "https://mirror.example")
		}, func(t *testing.T, panel map[string]any) {
			got, ok := providerNames(t, panel)["mirror_only"]
			if !ok {
				t.Fatalf("mirror-only provider missing: %#v", providerNames(t, panel))
			}
			if got.APIKey != maskAPIKey("sk-mirror") || got.APIBase != "https://mirror.example" {
				t.Errorf("mirror-only provider = %#v", got)
			}
		}},
		{"user layer disables a provider", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			if err := scope.SaveProviderState(context.Background(), s.dataStore, uid, "", "openai",
				config.ProviderConfig{}, false); err != nil {
				t.Fatalf("disable provider: %v", err)
			}
		}, func(t *testing.T, panel map[string]any) {
			if _, ok := providerNames(t, panel)["openai"]; ok {
				t.Errorf("disabled provider still offered: %#v", providerNames(t, panel))
			}
		}},
		{"user layer disables a channel", func(t *testing.T, s *Server, uid string) {
			seedSystemView(t, s, uid)
			if err := scope.SaveChannel(context.Background(), s.dataStore, uid, "", "telegram", "bot-system", false,
				config.ChannelConfig{}); err != nil {
				t.Fatalf("disable channel: %v", err)
			}
		}, func(t *testing.T, panel map[string]any) {
			if _, ok := channelNames(t, panel)["telegram"]; ok {
				t.Errorf("disabled channel still offered: %#v", channelNames(t, panel))
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, uid, _ := setupFileUploadTest(t)
			tc.seed(t, s, uid)
			tc.check(t, assertPanelMatchesRuntime(t, s, uid))
		})
	}
}

// assertPanelMatchesRuntime runs both paths and compares every namespace,
// the providers map and the channels map.
func assertPanelMatchesRuntime(t *testing.T, s *Server, uid string) map[string]any {
	t.Helper()
	ctx := context.Background()

	rec := httptest.NewRecorder()
	s.handleGetConfig(rec, cfgReq(t, http.MethodGet, uid, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d (%s)", rec.Code, rec.Body.String())
	}
	var panel map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &panel); err != nil {
		t.Fatalf("decode panel body: %v", err)
	}

	// Path B: resolve one namespace at a time and run the same post-processing
	// the handler documents (env overlay, then defaults).
	want := &config.Config{
		Providers: map[string]config.ProviderConfig{},
		Channels:  map[string]config.ChannelConfig{},
	}
	for _, n := range parityNamespaces {
		data, err := scope.Setting(ctx, s.dataStore, n.namespace, uid, "")
		if err != nil {
			t.Fatalf("Setting(%s): %v", n.namespace, err)
		}
		if len(data) == 0 {
			continue
		}
		blob, err := json.Marshal(data)
		if err != nil {
			t.Fatalf("marshal %s: %v", n.namespace, err)
		}
		if err := json.Unmarshal(blob, namespaceDst(t, n.namespace)(want)); err != nil {
			t.Fatalf("project %s: %v", n.namespace, err)
		}
	}
	wantProvs, err := scope.Providers(ctx, s.dataStore, uid, "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	wantChans, err := scope.Channels(ctx, s.dataStore, uid, "")
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	want.Providers, want.Channels = wantProvs, wantChans
	config.LoadEnv().ApplyToConfig(want)
	config.ApplyDefaults(want)
	// The handler masks secrets on the way out; mask the expected view the
	// same way so the comparison is about resolution, not about redaction.
	maskedProvs := make(map[string]config.ProviderConfig, len(want.Providers))
	for k, v := range want.Providers {
		v.APIKey = maskAPIKey(v.APIKey)
		maskedProvs[k] = v
	}

	for _, n := range parityNamespaces {
		blob, err := json.Marshal(namespaceDst(t, n.namespace)(want))
		if err != nil {
			t.Fatalf("marshal expected %s: %v", n.namespace, err)
		}
		wantMap, err := store.JSONToMap(blob)
		if err != nil {
			t.Fatalf("expected %s to map: %v", n.namespace, err)
		}
		// Compare as JSON text, not as Go values: the two paths carry
		// numbers as different Go types (the blob reader keeps json.Number,
		// the HTTP body has float64) and both are the same JSON number.
		got, wantJSON := digPath(t, panel, n.path), any(wantMap)
		if canonicalJSON(t, got) != canonicalJSON(t, wantJSON) {
			t.Errorf("%s: panel = %s, resolvers = %s", n.namespace, canonicalJSON(t, got), canonicalJSON(t, wantJSON))
		}
	}

	gotProvs := decodeJSON[map[string]config.ProviderConfig](t, panel["providers"])
	if !reflect.DeepEqual(normProviders(gotProvs), normProviders(maskedProvs)) {
		t.Errorf("providers: panel = %#v, resolvers = %#v", gotProvs, maskedProvs)
	}
	gotChans := decodeJSON[map[string]config.ChannelConfig](t, panel["channels"])
	if !reflect.DeepEqual(normChannels(gotChans), normChannels(want.Channels)) {
		t.Errorf("channels: panel = %#v, resolvers = %#v", gotChans, want.Channels)
	}
	return panel
}

// providerNames / channelNames decode just the key set of the two scope-aware
// maps, for the case-level checks that only care whether an entry is present.
func providerNames(t *testing.T, panel map[string]any) map[string]config.ProviderConfig {
	t.Helper()
	return decodeJSON[map[string]config.ProviderConfig](t, panel["providers"])
}

func channelNames(t *testing.T, panel map[string]any) map[string]config.ChannelConfig {
	t.Helper()
	return decodeJSON[map[string]config.ChannelConfig](t, panel["channels"])
}

// namespaceDst finds the typed destination the panel adapter decodes a
// namespace into. Reusing it is the point: the comparison asks whether the
// two paths resolved the same data, not whether the struct has the fields.
func namespaceDst(t *testing.T, namespace string) func(*config.Config) interface{} {
	t.Helper()
	for _, ns := range settingNamespaces {
		if ns.namespace == namespace {
			return ns.dst
		}
	}
	t.Fatalf("no settingNamespace %q", namespace)
	return nil
}

// seedMirrorValue writes a single configs_kv row without its blob — the state
// a mirror-only writer (or a partially migrated row) leaves behind.
func seedMirrorValue(t *testing.T, st store.Store, kind, scopeName, scopeID, name string, value interface{}) {
	t.Helper()
	if err := st.SetConfigValue(context.Background(), kind, scopeName, scopeID, name, store.EncodeConfigValue(value)); err != nil {
		t.Fatalf("seed mirror row %s: %v", name, err)
	}
}

func digPath(t *testing.T, m map[string]any, path []string) any {
	t.Helper()
	var cur any = m
	for _, seg := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[seg]
	}
	return cur
}

func decodeJSON[T any](t *testing.T, v any) T {
	t.Helper()
	var out T
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("unmarshal %T: %v", out, err)
	}
	return out
}

// normProviders / normChannels collapse the nil-versus-empty
// difference that JSON encoding introduces: an absent map and an empty object
// are the same statement, and the two paths reach them by different routes.
// canonicalJSON renders v as JSON text with sorted keys, so two values that
// differ only in Go representation (json.Number("111") vs float64(111))
// compare equal — the reader's answer and the wire's answer are the same
// number, and that is what parity means here.
func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal for comparison: %v", err)
	}
	return string(blob)
}

func normProviders(m map[string]config.ProviderConfig) map[string]config.ProviderConfig {
	if m == nil {
		return map[string]config.ProviderConfig{}
	}
	return m
}

func normChannels(m map[string]config.ChannelConfig) map[string]config.ChannelConfig {
	if m == nil {
		return map[string]config.ChannelConfig{}
	}
	return m
}

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
		map[string]interface{}{"autoPersist": map[string]interface{}{"enabled": true, "everyNTurns": 7}}); err != nil {
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
	ap, ok := got["autoPersist"].(map[string]interface{})
	if !ok || ap["enabled"] != true {
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
	//
	// The veto goes through the same dual-write every other settings write
	// uses, not a bare SaveConfig: a row written straight into the blob leaves
	// the mirror's marker certifying the old leaves, and once reads prefer the
	// mirror that stale "enabled=true" is what answers. Writing it the way a
	// real caller would is the point of the assertion.
	if err := scope.SaveSettingState(ctx, s.dataStore, uid, "", "agents.defaults", nil, false); err != nil {
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
