package scope

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func openScopeDB(t *testing.T) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestGetValueScopePrecedence pins the configs_kv resolution order:
// system → user → agent → per-(user, agent), innermost wins.
func TestGetValueScopePrecedence(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	set := func(sc, sid, name, val string) {
		t.Helper()
		if err := db.SetConfigValue(ctx, store.KindSetting, sc, sid, name, val); err != nil {
			t.Fatalf("SetConfigValue(%s,%s,%s): %v", sc, sid, name, err)
		}
	}
	set(System, "", "theme", "system-theme")
	set(User, "user-a", "theme", "user-theme")
	set(Agent, "agent-x", "theme", "agent-theme")
	set(UserAgent, "user-a/agent-x", "theme", "per-theme")

	v, found, err := GetValue(ctx, db, store.KindSetting, "theme", "user-a", "agent-x")
	if err != nil || !found {
		t.Fatalf("GetValue full chain: v=%q found=%v err=%v", v, found, err)
	}
	if v != "per-theme" {
		t.Fatalf("per-(user,agent) should win, got %q", v)
	}

	// Without the per-(user,agent) row → agent wins.
	if err := db.DeleteConfigValue(ctx, store.KindSetting, UserAgent, "user-a/agent-x", "theme"); err != nil {
		t.Fatalf("delete per: %v", err)
	}
	v, _, _ = GetValue(ctx, db, store.KindSetting, "theme", "user-a", "agent-x")
	if v != "agent-theme" {
		t.Fatalf("agent should win without per layer, got %q", v)
	}

	// User only (agent empty) → user wins.
	v, _, _ = GetValue(ctx, db, store.KindSetting, "theme", "user-a", "")
	if v != "user-theme" {
		t.Fatalf("user should win when agent empty, got %q", v)
	}

	// Neither user nor agent → system.
	v, _, _ = GetValue(ctx, db, store.KindSetting, "theme", "", "")
	if v != "system-theme" {
		t.Fatalf("system should win when no user/agent, got %q", v)
	}
}

// TestProviderPerUserAgentIsolation is the fork-adaptation regression
// guard: a provider key bound at per-(user, agent) scope must stay
// isolated to that agent and never leak to the user's other agents.
func TestProviderPerUserAgentIsolation(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	// Bind a provider key at per-(user-a, agent-x) scope.
	if err := SaveProvider(ctx, db, "user-a", "agent-x", "openai", config.ProviderConfig{
		APIKey:  "sk-per",
		APIBase: "https://per.agent",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	// The bound agent sees it…
	provs, err := Providers(ctx, db, "user-a", "agent-x")
	if err != nil {
		t.Fatalf("Providers(bound): %v", err)
	}
	if got := provs["openai"].APIKey; got != "sk-per" {
		t.Fatalf("bound agent openai.APIKey = %q, want sk-per", got)
	}

	// …a sibling agent of the same user must NOT.
	provs, err = Providers(ctx, db, "user-a", "agent-y")
	if err != nil {
		t.Fatalf("Providers(sibling): %v", err)
	}
	if _, ok := provs["openai"]; ok {
		t.Fatalf("sibling agent leaked per-(user,agent) provider: %+v", provs)
	}

	// The value was written at the user-agent layer, not the user layer.
	v, err := db.GetConfigValue(ctx, store.KindProvider, UserAgent, "user-a/agent-x", "openai.api_key")
	if err != nil || v != "sk-per" {
		t.Fatalf("configs_kv user-agent openai.api_key = %q err=%v", v, err)
	}
	if _, err := db.GetConfigValue(ctx, store.KindProvider, User, "user-a", "openai.api_key"); err == nil {
		t.Fatalf("per-(user,agent) key must not be stored at user layer")
	}
}

// TestProvidersDualWriteReadsFromBlob pins the read precedence: the legacy
// configs row is authoritative (exact JSON types, complete key set) and
// configs_kv is a projection of it. Reading KV first is what produced the
// web_search incident — the projection re-typed and re-cased values, and a
// partial projection silently hid every key it did not carry. The mirror is
// still consulted, but only for rows that have no blob counterpart.
func TestProvidersDualWriteReadsFromBlob(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	// SaveProvider dual-writes to configs_kv.
	if err := SaveProvider(ctx, db, "user-a", "", "openai", config.ProviderConfig{
		APIKey:  "sk-1",
		APIBase: "https://api.openai.com",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	// configs_kv now holds the flattened row.
	v, err := db.GetConfigValue(ctx, store.KindProvider, User, "user-a", "openai.api_key")
	if err != nil || v != "sk-1" {
		t.Fatalf("configs_kv openai.api_key = %q err=%v", v, err)
	}

	// A mirror-only override does not win while the blob row exists.
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "openai.api_key", "sk-kv-override"); err != nil {
		t.Fatalf("SetConfigValue override: %v", err)
	}
	provs, err := Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if got := provs["openai"].APIKey; got != "sk-1" {
		t.Fatalf("Providers = %q, want the blob's sk-1", got)
	}

	// Drop the blob row: the mirror is now the only source.
	rec, err := db.GetConfigByName(ctx, store.KindProvider, "user-a", "", "openai")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	provs, err = Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers mirror fallback: %v", err)
	}
	if got := provs["openai"].APIKey; got != "sk-kv-override" {
		t.Fatalf("Providers mirror fallback = %q, want sk-kv-override", got)
	}
}

// TestSettingDualWriteReadsFromBlob covers the same precedence for settings
// namespaces: blob first, mirror only when the blob row is gone.
func TestSettingDualWriteReadsFromBlob(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "user-a", "", "agents.defaults", map[string]interface{}{
		"model": "deepseek/deepseek-v4-pro",
		"temp":  0.7,
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// agents.defaults maps to the "agent." KV prefix (upstream contract).
	v, err := db.GetConfigValue(ctx, store.KindSetting, User, "user-a", "agent.model")
	if err != nil || v != "deepseek/deepseek-v4-pro" {
		t.Fatalf("configs_kv agent.model = %q err=%v", v, err)
	}

	got, err := Setting(ctx, db, "agents.defaults", "user-a", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if m, _ := got["model"].(string); m != "deepseek/deepseek-v4-pro" {
		t.Fatalf("Setting model = %v, want deepseek/deepseek-v4-pro", got["model"])
	}

	// Fallback to legacy configs after wiping configs_kv.
	if err := db.DeleteConfigPrefix(ctx, store.KindSetting, User, "user-a", "agent."); err != nil {
		t.Fatalf("DeleteConfigPrefix: %v", err)
	}
	got, err = Setting(ctx, db, "agents.defaults", "user-a", "")
	if err != nil {
		t.Fatalf("Setting fallback: %v", err)
	}
	if m, _ := got["model"].(string); m != "deepseek/deepseek-v4-pro" {
		t.Fatalf("Setting fallback model = %v", got["model"])
	}
}

// TestParseKVValue pins value type inference: bools, numbers, JSON
// arrays/objects, and plain strings.
func TestParseKVValue(t *testing.T) {
	cases := []struct {
		in   string
		want interface{}
	}{
		{"true", true},
		{"false", false},
		{"42", float64(42)},
		{"[1,2]", []interface{}{float64(1), float64(2)}},
		{`{"a":1}`, map[string]interface{}{"a": float64(1)}},
		{"hello", "hello"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseKVValue(c.in); !jsonEqual(got, c.want) {
			t.Fatalf("parseKVValue(%q) = %#v (%T), want %#v", c.in, got, got, c.want)
		}
	}
}

// TestSnakeCamelRoundTrip pins the snake_case ↔ camelCase converters the
// flatten/reconstruct path relies on.
func TestSnakeCamelRoundTrip(t *testing.T) {
	for _, c := range []struct{ camel, snake string }{
		{"apiKey", "api_key"},
		{"apiBase", "api_base"},
		{"authType", "auth_type"},
		{"model", "model"},
	} {
		if got := camelToSnake(c.camel); got != c.snake {
			t.Fatalf("camelToSnake(%q) = %q, want %q", c.camel, got, c.snake)
		}
		if got := snakeToCamel(c.snake); got != c.camel {
			t.Fatalf("snakeToCamel(%q) = %q, want %q", c.snake, got, c.camel)
		}
	}
}

// TestKvToSettingMapNested pins that dotted relative keys reconstruct into
// nested maps (with snake_case→camelCase per segment). Regression for the
// flat-key bug: tools.providers.searxng.endpoint used to yield the literal
// key "searxng.endpoint", breaking SettingInto into typed provider configs.
func TestKvToSettingMapNested(t *testing.T) {
	got := kvToSettingMap("tools.providers.", map[string]string{
		"tools.providers.searxng.endpoint":        "https://searxng.tokenaissance.com",
		"tools.providers.searxng.api_key":         "sk-x",
		"tools.providers.jina.options.extra.mode": "fast",
	})
	want := map[string]interface{}{
		"searxng": map[string]interface{}{
			"endpoint": "https://searxng.tokenaissance.com",
			"apiKey":   "sk-x",
		},
		"jina": map[string]interface{}{
			"options": map[string]interface{}{
				"extra": map[string]interface{}{"mode": "fast"},
			},
		},
	}
	if !jsonEqual(got, want) {
		t.Fatalf("kvToSettingMap = %#v, want %#v", got, want)
	}
}

// TestKvToSettingMapToleratesBadDots guards against stray/trailing dots in
// stored keys — they must be dropped, not turned into empty map keys.
func TestKvToSettingMapToleratesBadDots(t *testing.T) {
	got := kvToSettingMap("tools.providers.", map[string]string{
		"tools.providers.searxng.endpoint.": "https://x",
		"tools.providers.searxng..api_key":  "sk-x",
	})
	want := map[string]interface{}{
		"searxng": map[string]interface{}{
			"endpoint": "https://x",
			"apiKey":   "sk-x",
		},
	}
	if !jsonEqual(got, want) {
		t.Fatalf("kvToSettingMap = %#v, want %#v", got, want)
	}
}

// TestSettingNestedNamespaceRoundTrip reproduces the dev regression
// end-to-end: SaveSetting dual-writes tools.providers to a dotted-leaf KV
// row, and Setting/SettingInto must rebuild the nested map so assembleConfig
// can unmarshal into map[string]config.ToolProviderCfg.
func TestSettingNestedNamespaceRoundTrip(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "tools.providers", map[string]interface{}{
		"searxng": map[string]interface{}{
			"endpoint": "https://searxng.tokenaissance.com",
		},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// The dual-write stores the flattened dotted leaf (exactly the dev shape).
	v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "tools.providers.searxng.endpoint")
	if err != nil || v != "https://searxng.tokenaissance.com" {
		t.Fatalf("configs_kv leaf = %q err=%v", v, err)
	}

	// Setting must reconstruct the nested map.
	got, err := Setting(ctx, db, "tools.providers", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	searxng, ok := got["searxng"].(map[string]interface{})
	if !ok {
		t.Fatalf("Setting searxng = %#v (%T), want nested map", got["searxng"], got["searxng"])
	}
	if ep, _ := searxng["endpoint"].(string); ep != "https://searxng.tokenaissance.com" {
		t.Fatalf("searxng.endpoint = %v, want https://searxng.tokenaissance.com", searxng["endpoint"])
	}

	// Full regression: typed unmarshal must succeed. This is what
	// assembleConfig does; it used to fail with "cannot unmarshal string into
	// Go value of type config.ToolProviderCfg".
	providers := map[string]config.ToolProviderCfg{}
	if err := SettingInto(ctx, db, "tools.providers", "", "", &providers); err != nil {
		t.Fatalf("SettingInto tools.providers: %v", err)
	}
	if got := providers["searxng"].Endpoint; got != "https://searxng.tokenaissance.com" {
		t.Fatalf("SettingInto searxng.endpoint = %q, want https://searxng.tokenaissance.com", got)
	}
}

// TestKvToSettingMapPreservesDataKeys is the regression guard for the dev
// incident: KV segments that are user-supplied *map keys* (tool category
// ids, provider names, skill ids, team ids, env var names) must come back
// verbatim. They used to be re-cased ("web_search" → "webSearch"), which
// made gateway.registerAgentToolChains miss cfg.Tools["web_search"] and
// register no web_search tool at all.
func TestKvToSettingMapPreservesDataKeys(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		kv     map[string]string
		want   map[string]interface{}
	}{
		{
			name:   "tool category id",
			prefix: "tools.categories.",
			kv:     map[string]string{"tools.categories.web_search.primary": "searxng/default"},
			want: map[string]interface{}{
				"web_search": map[string]interface{}{"primary": "searxng/default"},
			},
		},
		{
			name:   "tool provider name plus options map",
			prefix: "tools.providers.",
			kv: map[string]string{
				"tools.providers.my_vendor.endpoint":          "https://x",
				"tools.providers.my_vendor.options.extra_key": "fast",
			},
			want: map[string]interface{}{
				"my_vendor": map[string]interface{}{
					"endpoint": "https://x",
					"options":  map[string]interface{}{"extra_key": "fast"},
				},
			},
		},
		{
			name:   "skill id plus env var name",
			prefix: "skills.entries.",
			kv: map[string]string{
				"skills.entries.web_search_skill.enabled":          "true",
				"skills.entries.web_search_skill.env.user_agent_x": "ua",
			},
			want: map[string]interface{}{
				"web_search_skill": map[string]interface{}{
					"enabled": true,
					"env":     map[string]interface{}{"user_agent_x": "ua"},
				},
			},
		},
		{
			name:   "team id",
			prefix: "teams.",
			kv:     map[string]string{"teams.ops_team.default_agent": "agt_1"},
			want: map[string]interface{}{
				"ops_team": map[string]interface{}{"defaultAgent": "agt_1"},
			},
		},
		{
			name:   "plugin id plus plugin config key",
			prefix: "plugins.",
			kv: map[string]string{
				"plugins.entries.my_plugin.enabled":                  "true",
				"plugins.entries.my_plugin.config.poll_interval_sec": "30",
			},
			want: map[string]interface{}{
				"entries": map[string]interface{}{
					"my_plugin": map[string]interface{}{
						"enabled": true,
						"config":  map[string]interface{}{"poll_interval_sec": float64(30)},
					},
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kvToSettingMap(c.prefix, c.kv); !jsonEqual(got, c.want) {
				t.Fatalf("kvToSettingMap(%q) = %#v, want %#v", c.prefix, got, c.want)
			}
		})
	}
}

// TestKvToSettingMapCamelCasesFieldSegments pins the other half of the
// contract: *struct-field* segments — including nested ones — must still
// come back camelCase. Converting only the leaf segment would break the
// live dev rows memory.auto_persist.enabled, privacy.pii_scrubbing.enabled
// and objectstore.s3.access_key.
func TestKvToSettingMapCamelCasesFieldSegments(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		kv     map[string]string
		want   map[string]interface{}
	}{
		{
			name:   "nested struct field",
			prefix: "memory.",
			kv:     map[string]string{"memory.auto_persist.enabled": "true"},
			want: map[string]interface{}{
				"autoPersist": map[string]interface{}{"enabled": true},
			},
		},
		{
			name:   "nested struct field under a lowercase map key",
			prefix: "objectstore.",
			kv:     map[string]string{"objectstore.s3.access_key": "ak"},
			want: map[string]interface{}{
				"s3": map[string]interface{}{"accessKey": "ak"},
			},
		},
		{
			name:   "leaf struct field beside a data key",
			prefix: "tools.categories.",
			kv:     map[string]string{"tools.categories.web_search.auto_fallback": "false"},
			want: map[string]interface{}{
				"web_search": map[string]interface{}{"autoFallback": false},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kvToSettingMap(c.prefix, c.kv); !jsonEqual(got, c.want) {
				t.Fatalf("kvToSettingMap(%q) = %#v, want %#v", c.prefix, got, c.want)
			}
		})
	}
}

// TestSettingCategoryDataKeyRoundTrip reproduces the dev regression end to
// end: the system-scope tools.categories row holds {"web_search": {...}},
// the dual-write flattens it to tools.categories.web_search.primary, and
// SettingInto must rebuild the *same* id so the tool chain registers.
func TestSettingCategoryDataKeyRoundTrip(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// The dual-write flattens to the dev KV shape.
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "",
		"tools.categories.web_search.primary"); err != nil || v != "searxng/default" {
		t.Fatalf("configs_kv leaf = %q err=%v", v, err)
	}

	// Read side: the category id must survive the round trip.
	got, err := Setting(ctx, db, "tools.categories", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if _, ok := got["web_search"]; !ok {
		t.Fatalf("Setting tools.categories = %#v, want the %q key", got, "web_search")
	}

	// Full regression: typed unmarshal — what userspace.go does.
	cats := map[string]config.ToolCategoryCfg{}
	if err := SettingInto(ctx, db, "tools.categories", "", "", &cats); err != nil {
		t.Fatalf("SettingInto tools.categories: %v", err)
	}
	if cats["web_search"].Primary != "searxng/default" {
		t.Fatalf("SettingInto web_search.primary = %q, want searxng/default", cats["web_search"].Primary)
	}
	if _, ok := cats["webSearch"]; ok {
		t.Fatalf("SettingInto produced a re-cased category id webSearch: %v", cats)
	}
}

// TestSettingAllCapsEnvKeyRoundTrip covers the write half of the same bug:
// camelToSnake lowercased ALL_CAPS data keys (REPLICATE_API_TOKEN →
// replicate_api_token) and the read side then camelCased them
// (replicateApiToken), so a skill's env var name could never survive a
// SaveSetting → Setting round trip. Data-key segments are now written
// verbatim.
func TestSettingAllCapsEnvKeyRoundTrip(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "skills.entries", map[string]interface{}{
		"web_search_skill": map[string]interface{}{
			"enabled": true,
			"env":     map[string]interface{}{"REPLICATE_API_TOKEN": "r8_x"},
		},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// Stored verbatim (the dev/legacy spelling any operator can read).
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "",
		"skills.entries.web_search_skill.env.REPLICATE_API_TOKEN"); err != nil || v != "r8_x" {
		t.Fatalf("configs_kv leaf = %q err=%v, want REPLICATE_API_TOKEN=r8_x", v, err)
	}
	// The pre-fix lowercased row must not be written.
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "",
		"skills.entries.web_search_skill.env.replicate_api_token"); err == nil {
		t.Fatalf("lowercased data key still written: %q", v)
	}

	// …and the typed read (what the skill loader consumes) sees it back.
	entries := map[string]config.SkillEntryCfg{}
	if err := SettingInto(ctx, db, "skills.entries", "", "", &entries); err != nil {
		t.Fatalf("SettingInto skills.entries: %v", err)
	}
	got := entries["web_search_skill"].Env["REPLICATE_API_TOKEN"]
	if got != "r8_x" {
		t.Fatalf("SettingInto env = %#v, want REPLICATE_API_TOKEN=r8_x", entries["web_search_skill"].Env)
	}
}

// TestSettingIntoFallsBackToLegacyBlob pins the configs_kv projection
// fallback. parseKVValue re-types every stored string ("123" → number,
// "true" → bool, "[…]" → array), so a number-looking value living in a
// string field makes the typed projection fail. The legacy blob keeps the
// exact JSON type, and SettingInto must serve that instead of returning the
// error — in the gateway the error aborts the caller's whole UserSpace load,
// so one bucket named "123" would have locked the user out.
func TestSettingIntoFallsBackToLegacyBlob(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "objectstore", map[string]interface{}{
		"s3": map[string]interface{}{"bucket": "123", "region": "us-east-1"},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	// Precondition: the KV row really is the string that breaks the typed
	// projection (as opposed to the blob, which keeps bucket as a string).
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "objectstore.s3.bucket"); err != nil || v != "123" {
		t.Fatalf("configs_kv leaf = %q err=%v, want 123", v, err)
	}

	var got config.ObjectStoreCfg
	if err := SettingInto(ctx, db, "objectstore", "", "", &got); err != nil {
		t.Fatalf("SettingInto objectstore: %v", err)
	}
	if got.S3.Bucket != "123" || got.S3.Region != "us-east-1" {
		t.Fatalf("objectstore s3 = %+v, want the legacy blob's string values", got.S3)
	}

	// The string must survive verbatim, not as a float or an empty string.
	bucketType := reflect.TypeOf(got.S3.Bucket).Kind()
	if bucketType != reflect.String || got.S3.Bucket[0] != '1' {
		t.Fatalf("bucket = %q (%v), want the literal string 123", got.S3.Bucket, bucketType)
	}
}

// TestSettingPartialMirrorDoesNotShadowBlob is the F3 regression: a mirror
// that is missing keys the blob still has (dualWriteSettingKV deletes the
// prefix and re-inserts row by row, so a crash in between, a legacy row set,
// or a hand-written row all leave a subset) must not decide what the runtime
// sees. Reading KV first returned exactly that subset, so the namespace
// silently lost every category the mirror happened not to carry.
func TestSettingPartialMirrorDoesNotShadowBlob(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
		"image_gen":  map[string]interface{}{"primary": "openai/gpt-image-1"},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := db.DeleteConfigPrefix(ctx, store.KindSetting, System, "", "tools.categories."); err != nil {
		t.Fatalf("DeleteConfigPrefix: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "tools.categories.tts.primary", "openai/tts-1"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	cats := map[string]config.ToolCategoryCfg{}
	if err := SettingInto(ctx, db, "tools.categories", "", "", &cats); err != nil {
		t.Fatalf("SettingInto: %v", err)
	}
	for _, want := range []string{"web_search", "image_gen"} {
		if _, ok := cats[want]; !ok {
			t.Fatalf("partial mirror shadowed the blob: %#v", cats)
		}
	}
	if cats["web_search"].Primary != "searxng/default" {
		t.Fatalf("web_search = %+v, want the blob's chain", cats["web_search"])
	}
}

// TestDashboardAndRuntimeAgreeOnSettings pins the F6 fix: the dashboard
// (BatchSettings over the legacy blobs) and the runtime (Setting) must serve
// the same object, even when the mirror is stale or mangled. Disagreement is
// what made the original incident so hard to see — the dashboard showed
// web_search configured while the runtime had never registered it.
func TestDashboardAndRuntimeAgreeOnSettings(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "tools.categories", map[string]interface{}{
		"web_search": map[string]interface{}{"primary": "searxng/default"},
		"image_gen":  map[string]interface{}{"primary": "openai/gpt-image-1"},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	// Leave only the pre-fix spelling in the mirror (the re-cased key the
	// registry used to produce) — neither reader may serve it.
	if err := db.DeleteConfigPrefix(ctx, store.KindSetting, System, "", "tools.categories."); err != nil {
		t.Fatalf("DeleteConfigPrefix: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "tools.categories.webSearch.primary", "legacy"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	batch, err := BatchSettings(ctx, db, []string{"tools.categories"}, "", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}
	runtime, err := Setting(ctx, db, "tools.categories", "", "")
	if err != nil {
		t.Fatalf("Setting: %v", err)
	}
	if !jsonEqual(batch["tools.categories"], runtime) {
		t.Fatalf("dashboard sees %#v, runtime sees %#v", batch["tools.categories"], runtime)
	}
	if _, ok := runtime["web_search"]; !ok {
		t.Fatalf("runtime lost web_search to a stale mirror: %#v", runtime)
	}
	if _, ok := runtime["webSearch"]; ok {
		t.Fatalf("runtime served the mirror's re-cased key: %#v", runtime)
	}
}

// TestProvidersMirrorFallbackKeepsNumericKey is the N2 regression. With no
// blob row to fall back to, the mirror is authoritative — and there the old
// code let parseKVValue turn an all-digit apiKey into a number, failed to
// unmarshal it into a string field, and dropped the field silently.
func TestProvidersMirrorFallbackKeepsNumericKey(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "alpha.api_key", "123456"); err != nil {
		t.Fatalf("SetConfigValue api_key: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "alpha.api_base", "https://api.example"); err != nil {
		t.Fatalf("SetConfigValue api_base: %v", err)
	}

	provs, err := Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	got := provs["alpha"]
	if got.APIKey != "123456" || got.APIBase != "https://api.example" {
		t.Fatalf("mirror-only provider = %+v, want the stored strings", got)
	}
}

// TestSettingNestedPluginConfigRoundTrip covers the free-form map under
// plugins.entries.*.config: the whitelist used to stop at config's first
// level, so anything deeper was re-cased as if it were a struct field
// (headers.X_API_Key → xAPIKey, mcpServers.my_server → myServer,
// env.API_KEY → apiKey). PluginEntryCfg.Config is map[string]any handed
// straight to the plugin's initialize call, so the renamed keys simply
// disappear from the plugin's view. Reached in production through
// PUT /api/plugins/{id}, which accepts arbitrary nested config.
func TestSettingNestedPluginConfigRoundTrip(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	in := map[string]interface{}{
		"my-hook": map[string]interface{}{
			"enabled": true,
			"config": map[string]interface{}{
				"poll_interval_sec": 30,
				"headers":           map[string]interface{}{"X_API_Key": "v", "plain_key": "w"},
				"mcpServers": map[string]interface{}{
					"my_server": map[string]interface{}{
						"command": "run",
						"env":     map[string]interface{}{"API_KEY": "k"},
					},
				},
			},
		},
		// An empty config object must survive too: the blob is the read source
		// now, and flattening it into rows had no row to represent "{}".
		"empty-hook": map[string]interface{}{
			"enabled": false,
			"config":  map[string]interface{}{},
		},
	}
	if err := SaveSetting(ctx, db, "", "", "plugins", map[string]interface{}{
		"entries": in,
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// On-disk spelling: data keys verbatim at every depth.
	for _, name := range []string{
		"plugins.entries.my-hook.config.poll_interval_sec",
		"plugins.entries.my-hook.config.headers.X_API_Key",
		"plugins.entries.my-hook.config.headers.plain_key",
		"plugins.entries.my-hook.config.mcpServers.my_server.env.API_KEY",
	} {
		if _, err := db.GetConfigValue(ctx, store.KindSetting, System, "", name); err != nil {
			t.Fatalf("configs_kv row %q missing: %v", name, err)
		}
	}

	var got config.PluginsCfg
	if err := SettingInto(ctx, db, "plugins", "", "", &got); err != nil {
		t.Fatalf("SettingInto plugins: %v", err)
	}
	entry, ok := got.Entries["my-hook"]
	if !ok || entry.Config == nil {
		t.Fatalf("plugin entry lost its config: %#v", got.Entries)
	}
	cfgMap := entry.Config
	if !jsonEqual(cfgMap, in["my-hook"].(map[string]interface{})["config"]) {
		t.Fatalf("plugin config = %#v, want %#v", cfgMap, in["my-hook"].(map[string]interface{})["config"])
	}
	if empty, ok := got.Entries["empty-hook"]; !ok || empty.Config == nil || len(empty.Config) != 0 {
		t.Fatalf("empty plugin config = %#v, want an empty object", empty.Config)
	}
}

func jsonEqual(a, b interface{}) bool {
	as, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bs, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(as) == string(bs)
}
