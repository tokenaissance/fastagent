package scope

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/kvkeys"
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

// openScopeDBNamed opens a store on its own in-memory database. The unnamed
// DSN above is shared by every test in this package (an empty name is one
// database), so a test that seeds rows another test also reads needs its own.
func openScopeDBNamed(t *testing.T, name string) *store.DBStore {
	t.Helper()
	db, err := store.NewDBStore("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store %s: %v", name, err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate %s: %v", name, err)
	}
	return db
}

// legacyKV builds untagged rows — the shape a writer that predates
// value_kind left behind. Reconstruction tests use it to keep pinning the
// legacy guessing path (which still exists for those rows); the tagged path
// is covered in internal/store/config_value_test.go.
func legacyKV(kv map[string]string) map[string]store.ConfigValue {
	out := make(map[string]store.ConfigValue, len(kv))
	for k, v := range kv {
		out[k] = store.ConfigValue{Value: v}
	}
	return out
}

// TestSettingLargeIntThroughKVOnlyPath is the end-to-end form of the
// store-level number tests: a settings write shaped the way every HTTP
// write is shaped — struct → marshal → map[string]interface{}, which is
// where an int becomes a float64 — served from the KV mirror only.
//
// maxTokens: 2000000 used to be stored as "2e+06". That is a valid JSON
// number but not an integer literal, so projecting it onto
// AgentDefaults.MaxTokens failed and SettingInto had no blob left to fall
// back to. The integer form has to survive the round trip.
func TestSettingLargeIntThroughKVOnlyPath(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	// The shape setup.toMap produces.
	blob, err := json.Marshal(config.AgentDefaults{Model: "openai/gpt-5.5", MaxTokens: 2_000_000})
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(blob, &data); err != nil {
		t.Fatalf("unmarshal defaults: %v", err)
	}
	if _, ok := data["maxTokens"].(float64); !ok {
		t.Fatalf("expected the settings shape to carry maxTokens as float64, got %#v", data["maxTokens"])
	}

	if err := SaveSetting(ctx, db, "", "", "agents.defaults", data); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// Serve it from the mirror only: drop the authoritative blob row, which
	// is the state a KV-only writer leaves behind.
	rec, err := db.GetConfigByName(ctx, store.KindSetting, "", "", "agents.defaults")
	if err != nil {
		t.Fatalf("get blob row: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("delete blob row: %v", err)
	}
	rows, err := db.ListConfigValues(ctx, store.KindSetting, System, "", "agent.")
	if err != nil {
		t.Fatalf("list mirror rows: %v", err)
	}
	if got := rows["agent.max_tokens"]; got.Value != "2000000" || got.Kind != store.ValueKindNumber {
		t.Fatalf("mirror agent.max_tokens = %+v, want the tagged integer 2000000", got)
	}

	var got config.AgentDefaults
	if err := SettingInto(ctx, db, "agents.defaults", "", "", &got); err != nil {
		t.Fatalf("SettingInto from the KV mirror: %v", err)
	}
	if got.MaxTokens != 2_000_000 {
		t.Fatalf("MaxTokens = %d, want 2000000", got.MaxTokens)
	}
}

// TestProvidersConfigsKVFallbackKeepsLegacyStructure is the other half of the
// N2 rule. The provider mirror must not guess *scalars* from an untagged
// row (an all-digit api_key would become a number and vanish), but the
// pre-tag code did decode *structure* — an object/array row came back as a
// map/slice, because there is nothing to guess: the text starts with { or [
// and either parses as JSON or does not.
//
// Dropping that half turned every legacy "models" row into the raw string
// `[{...}]`, which then failed to unmarshal into []config.ModelEntry and took
// the whole provider down with it (slog.Warn + skip), even though the row was
// intact in the mirror.
func TestProvidersConfigsKVFallbackKeepsLegacyStructure(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	// A pre-tag provider: no value_kind anywhere, the array stored as one row
	// of JSON text.
	legacy := map[string]store.ConfigValue{
		"legacy.api_key": store.ConfigValue{Value: "sk-legacy"},
		"legacy.models":  store.ConfigValue{Value: `[{"id":"gpt-5.5","contextWindow":128000}]`},
	}
	for name, v := range legacy {
		if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", name, v); err != nil {
			t.Fatalf("SetConfigValue(%s): %v", name, err)
		}
	}

	provs, err := Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	got, ok := provs["legacy"]
	if !ok {
		t.Fatalf("legacy provider missing from the mirror mirror: %#v", provs)
	}
	if got.APIKey != "sk-legacy" {
		t.Fatalf("APIKey = %q, want sk-legacy", got.APIKey)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "gpt-5.5" || got.Models[0].ContextWindow != 128000 {
		t.Fatalf("Models = %#v, want the stored array decoded", got.Models)
	}
}

// TestValidateProviderName pins the charset rule that keeps a provider name
// representable in both places it is used: as the configs_kv key prefix
// ("<name>.<field>", split at the first dot) and as the left half of a
// "provider/model" reference.
func TestValidateProviderName(t *testing.T) {
	valid := []string{"openai", "azure-openai", "my_provider", "a", "A1", "x9_-y"}
	for _, name := range valid {
		if err := ValidateProviderName(name); err != nil {
			t.Errorf("ValidateProviderName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "my.provider", "a/b", "-lead", "_lead", "sp ace", "名字", "p:", strings.Repeat("a", 65)}
	for _, name := range invalid {
		if err := ValidateProviderName(name); err == nil {
			t.Errorf("ValidateProviderName(%q) = nil, want an error", name)
		}
	}
	// The write path is the choke point: HTTP create/update, the admin API,
	// onboarding and the CLI all reach storage through SaveProvider.
	if err := SaveProvider(context.Background(), openScopeDB(t), "", "", "my.provider", config.ProviderConfig{}); err == nil {
		t.Fatal("SaveProvider accepted a dotted name")
	}
}

// TestBatchSettingsFallsBackToConfigsKV pins the read-path promise for the one
// reader that did not implement it: a namespace with no blob row anywhere is
// served from configs_kv, exactly as Setting would.
func TestBatchSettingsFallsBackToConfigsKV(t *testing.T) {
	// Its own database, not openScopeDB's: that DSN names the *same* in-memory
	// database for every test in this package, and this test writes a "prefs"
	// row that the timezone tests read.
	db := openScopeDBNamed(t, "batchmirror")
	defer db.Close()
	ctx := context.Background()

	// One namespace with a blob row, one that exists only in the mirror.
	if err := SaveSetting(ctx, db, "", "", "prefs", map[string]interface{}{"timezone": "Asia/Shanghai"}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "probe.enabled",
		store.EncodeConfigValue(true)); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "probe.name",
		store.EncodeConfigValue("123")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	got, err := BatchSettings(ctx, db, []string{"prefs", "probe"}, "", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}
	if v := got["prefs"]["timezone"]; v != "Asia/Shanghai" {
		t.Errorf("prefs.timezone = %#v, want the blob value", v)
	}
	if v := got["probe"]["enabled"]; v != true {
		t.Errorf("probe.enabled = %#v, want the mirror value", v)
	}
	if v := got["probe"]["name"]; v != "123" {
		t.Errorf("probe.name = %#v, want the tagged string \"123\"", v)
	}

	// A namespace nobody wrote stays absent rather than becoming an empty map.
	got, err = BatchSettings(ctx, db, []string{"nothing-here"}, "", "")
	if err != nil {
		t.Fatalf("BatchSettings: %v", err)
	}
	if _, ok := got["nothing-here"]; ok {
		t.Errorf("unwritten namespace present in the result: %#v", got)
	}
}

// TestChannelsHaveNoConfigsKVHalf pins the documented exception instead of
// leaving it as an unstated asymmetry. configs_kv holds provider, setting and
// plugin_enabled rows; a channel lives in the configs blob plus the channels
// table, so Channels() has nothing to fall back to — and that is safe only
// while no code path writes channel rows into the mirror. If someone adds a
// partial dual-write, this fails and points at the reader that must learn the
// fallback first.
func TestChannelsHaveNoConfigsKVHalf(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveChannel(ctx, db, "user-a", "", "telegram", "bot-1", true,
		config.ChannelConfig{BotToken: "t-1"}); err != nil {
		t.Fatalf("SaveChannel: %v", err)
	}
	rows, err := db.ListConfigValues(ctx, store.KindChannel, User, "user-a", "")
	if err != nil {
		t.Fatalf("ListConfigValues: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("channel rows leaked into configs_kv: %#v", rows)
	}

	chans, err := Channels(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Channels: %v", err)
	}
	if got := chans["telegram"].BotToken; got != "t-1" {
		t.Fatalf("telegram.BotToken = %q, want the blob value", got)
	}
}

// TestGetValueScopePrecedence pins the configs_kv resolution order:
// system → user → agent → per-(user, agent), innermost wins.
//
// The precedence itself lives in GetValues (the reader the KV fallbacks go
// through) now that the single-key GetValue wrapper is gone. The tag rides
// with the winning row, so the assertion below is on the whole ConfigValue:
// an inner scope must replace the outer value *and* its type, not just the
// text.
func TestGetValuesScopePrecedence(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	set := func(sc, sid, name string, val store.ConfigValue) {
		t.Helper()
		if err := db.SetConfigValue(ctx, store.KindSetting, sc, sid, name, val); err != nil {
			t.Fatalf("SetConfigValue(%s,%s,%s): %v", sc, sid, name, err)
		}
	}
	// The innermost row is tagged a number on purpose: if scope merge
	// replaced only the text and kept the outer tag, this would come back as
	// the string "1".
	set(System, "", "theme", store.StringValue("system-theme"))
	set(User, "user-a", "theme", store.StringValue("user-theme"))
	set(Agent, "agent-x", "theme", store.StringValue("agent-theme"))
	set(UserAgent, "user-a/agent-x", "theme", store.EncodeConfigValue(1))

	got, err := GetValues(ctx, db, store.KindSetting, "theme", "user-a", "agent-x")
	if err != nil {
		t.Fatalf("GetValues full chain: %v", err)
	}
	if v := got["theme"]; v.Value != "1" || v.Kind != store.ValueKindNumber {
		t.Fatalf("per-(user,agent) should win, got %+v", v)
	}

	// Without the per-(user,agent) row → agent wins.
	if err := db.DeleteConfigValue(ctx, store.KindSetting, UserAgent, "user-a/agent-x", "theme"); err != nil {
		t.Fatalf("delete per: %v", err)
	}
	got, _ = GetValues(ctx, db, store.KindSetting, "theme", "user-a", "agent-x")
	if v := got["theme"]; v.Value != "agent-theme" || v.Kind != store.ValueKindString {
		t.Fatalf("agent should win without per layer, got %+v", v)
	}

	// User only (agent empty) → user wins.
	got, _ = GetValues(ctx, db, store.KindSetting, "theme", "user-a", "")
	if v := got["theme"]; v.Value != "user-theme" {
		t.Fatalf("user should win when agent empty, got %+v", v)
	}

	// Neither user nor agent → system.
	got, _ = GetValues(ctx, db, store.KindSetting, "theme", "", "")
	if v := got["theme"]; v.Value != "system-theme" {
		t.Fatalf("system should win when no user/agent, got %+v", v)
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
	if err != nil || v.Value != "sk-per" {
		t.Fatalf("configs_kv user-agent openai.api_key = %q err=%v", v, err)
	}
	if _, err := db.GetConfigValue(ctx, store.KindProvider, User, "user-a", "openai.api_key"); err == nil {
		t.Fatalf("per-(user,agent) key must not be stored at user layer")
	}
}

// TestProvidersDualWriteReadsFromBlob pins the read precedence: the legacy
// configs row is authoritative (exact JSON types, complete key set) and
// configs_kv is a mirror of it. Reading KV first is what produced the
// web_search incident — the mirror re-typed and re-cased values, and a
// partial mirror silently hid every key it did not carry. The mirror is
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
	if err != nil || v.Value != "sk-1" {
		t.Fatalf("configs_kv openai.api_key = %q err=%v", v, err)
	}

	// A mirror-only override does not win while the blob row exists.
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "openai.api_key", store.StringValue("sk-kv-override")); err != nil {
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
	if err != nil || v.Value != "deepseek/deepseek-v4-pro" {
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

// TestSnakeCamelRoundTrip pins the snake_case ↔ camelCase converters the
// flatten/reconstruct path relies on.
func TestSnakeCamelRoundTrip(t *testing.T) {
	for _, c := range []struct{ camel, snake string }{
		{"apiKey", "api_key"},
		{"apiBase", "api_base"},
		{"authType", "auth_type"},
		{"model", "model"},
	} {
		// The write direction has no local alias any more: it belongs to
		// kvkeys, and the scope package only borrows the read direction.
		if got := kvkeys.CamelToSnake(c.camel); got != c.snake {
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
	got := kvToSettingMap("tools.providers.", legacyKV(map[string]string{
		"tools.providers.searxng.endpoint":        "https://searxng.tokenaissance.com",
		"tools.providers.searxng.api_key":         "sk-x",
		"tools.providers.jina.options.extra.mode": "fast",
	}))
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
	got := kvToSettingMap("tools.providers.", legacyKV(map[string]string{
		"tools.providers.searxng.endpoint.": "https://x",
		"tools.providers.searxng..api_key":  "sk-x",
	}))
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
	if err != nil || v.Value != "https://searxng.tokenaissance.com" {
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
			if got := kvToSettingMap(c.prefix, legacyKV(c.kv)); !jsonEqual(got, c.want) {
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
			if got := kvToSettingMap(c.prefix, legacyKV(c.kv)); !jsonEqual(got, c.want) {
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
		"tools.categories.web_search.primary"); err != nil || v.Value != "searxng/default" {
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
		"skills.entries.web_search_skill.env.REPLICATE_API_TOKEN"); err != nil || v.Value != "r8_x" {
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

// TestSettingIntoFallsBackToLegacyBlob pins the number-looking-string
// guarantee from both sides. A bucket genuinely named "123" used to be a
// landmine: the mirror re-typed it to a number, the typed read failed,
// and in the gateway that error aborted the caller's whole UserSpace load.
//
// Behind a blob row (the always-available safety net), an untagged mirror row
// must still not take the caller down — Setting serves the blob.
//
// With no blob row (mirror-only namespaces, and anything written after the
// tag), the tag is what carries the type, so the read succeeds on its
// own instead of relying on the safety net.
func TestSettingIntoFallsBackToLegacyBlob(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := SaveSetting(ctx, db, "", "", "objectstore", map[string]interface{}{
		"s3": map[string]interface{}{"bucket": "123", "region": "us-east-1"},
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// A row written before value_kind existed: same text, no tag. The blob is
	// present, so the caller is safe either way.
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "objectstore.s3.bucket",
		store.ConfigValue{Value: "123"}); err != nil {
		t.Fatalf("rewrite mirror row untagged: %v", err)
	}
	var behindBlob config.ObjectStoreCfg
	if err := SettingInto(ctx, db, "objectstore", "", "", &behindBlob); err != nil {
		t.Fatalf("SettingInto behind the blob: %v", err)
	}
	if behindBlob.S3.Bucket != "123" || behindBlob.S3.Region != "us-east-1" {
		t.Fatalf("objectstore s3 = %+v, want the string values", behindBlob.S3)
	}
	// The string must survive verbatim, not as a float or an empty string.
	bucketType := reflect.TypeOf(behindBlob.S3.Bucket).Kind()
	if bucketType != reflect.String || behindBlob.S3.Bucket[0] != '1' {
		t.Fatalf("bucket = %q (%v), want the literal string 123", behindBlob.S3.Bucket, bucketType)
	}

	// Re-save to restore tagged mirror rows, then drop the blob row: now the
	// tag is the only thing carrying the type, and the mirror must still
	// land the string.
	if err := SaveSetting(ctx, db, "", "", "objectstore", map[string]interface{}{
		"s3": map[string]interface{}{"bucket": "123", "region": "us-east-1"},
	}); err != nil {
		t.Fatalf("re-SaveSetting: %v", err)
	}
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "objectstore.s3.bucket"); err != nil ||
		v.Value != "123" || v.Kind != store.ValueKindString {
		t.Fatalf("configs_kv leaf = %+v err=%v, want the tagged string 123", v, err)
	}
	rec, err := db.GetConfigByName(ctx, store.KindSetting, "", "", "objectstore")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	var mirrorOnly config.ObjectStoreCfg
	if err := SettingInto(ctx, db, "objectstore", "", "", &mirrorOnly); err != nil {
		t.Fatalf("SettingInto from a tagged mirror with no blob row: %v", err)
	}
	if mirrorOnly.S3.Bucket != "123" {
		t.Fatalf("mirror-only bucket = %q, want the literal string 123", mirrorOnly.S3.Bucket)
	}
}

// TestSettingPartialConfigsKVDoesNotShadowBlob is the F3 regression: a mirror
// that is missing keys the blob still has (dualWriteSettingKV deletes the
// prefix and re-inserts row by row, so a crash in between, a legacy row set,
// or a hand-written row all leave a subset) must not decide what the runtime
// sees. Reading KV first returned exactly that subset, so the namespace
// silently lost every category the mirror happened not to carry.
func TestSettingPartialConfigsKVDoesNotShadowBlob(t *testing.T) {
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
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "tools.categories.tts.primary", store.StringValue("openai/tts-1")); err != nil {
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
	if err := db.SetConfigValue(ctx, store.KindSetting, System, "", "tools.categories.webSearch.primary", store.StringValue("legacy")); err != nil {
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

// TestProvidersConfigsKVFallbackKeepsNumericKey is the N2 regression. With no
// blob row to fall back to the mirror is authoritative, and there the old
// code let the legacy guesser turn an all-digit apiKey into a number, failed
// to unmarshal it into a string field, and dropped the field silently.
//
// The first provider is seeded the way the writer seeds it now (a tagged
// string, so the type is recorded rather than guessed). The second is seeded
// untagged — the pre-tag shape — and must still keep the digits as text,
// because that was the N2 fix and untagged rows are exactly the ones it
// protects.
func TestProvidersConfigsKVFallbackKeepsNumericKey(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "alpha.api_key", store.StringValue("123456")); err != nil {
		t.Fatalf("SetConfigValue api_key: %v", err)
	}
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "alpha.api_base", store.StringValue("https://api.example")); err != nil {
		t.Fatalf("SetConfigValue api_base: %v", err)
	}
	// Untagged: same digits, no tag to say they are a string.
	if err := db.SetConfigValue(ctx, store.KindProvider, User, "user-a", "beta.api_key", store.ConfigValue{Value: "654321"}); err != nil {
		t.Fatalf("SetConfigValue untagged api_key: %v", err)
	}

	provs, err := Providers(ctx, db, "user-a", "")
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	got := provs["alpha"]
	if got.APIKey != "123456" || got.APIBase != "https://api.example" {
		t.Fatalf("mirror-only provider = %+v, want the stored strings", got)
	}
	if untagged := provs["beta"]; untagged.APIKey != "654321" {
		t.Fatalf("untagged mirror provider = %+v, want the digits kept as text", untagged)
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

// typedProbe is the struct for TestConfigsKVFallbackRestoresValueTypes: mixed
// JSON types in one namespace, including the two pairs the tag exists to keep
// apart (the string "123" vs the number 123, and a 19-digit int64).
type typedProbe struct {
	Port    int64   `json:"port"`
	Name    string  `json:"name"`
	Enabled bool    `json:"enabled"`
	Ratio   float64 `json:"ratio"`
	Big     int64   `json:"big"`
	Empty   string  `json:"empty"`
}

// TestConfigsKVFallbackRestoresValueTypes is the end-to-end regression for the
// value_kind change. The mirror is read only when the blob has no row, and
// before the tag that path had to guess every type from the text:
//
//   - Name ("123", a string) came back as a number, failed to unmarshal into
//     the string field, and took the whole namespace down — with the blob row
//     deleted there is nothing to fall back to, so SettingInto returned an
//     error and the caller (in the gateway, a whole UserSpace load) failed.
//   - Big (math.MaxInt64) did not survive the %g round trip at all.
//
// With the tag the writer's types come back exactly as written.
func TestConfigsKVFallbackRestoresValueTypes(t *testing.T) {
	db := openScopeDB(t)
	defer db.Close()
	ctx := context.Background()

	const bigLiteral = "9223372036854775807"
	if err := SaveSetting(ctx, db, "", "", "probe", map[string]interface{}{
		"port":    float64(8080),
		"name":    "123",
		"enabled": true,
		"ratio":   1.5,
		"big":     json.Number(bigLiteral),
		"empty":   "",
	}); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	// Drop the blob row so the read can only be served by the mirror.
	rec, err := db.GetConfigByName(ctx, store.KindSetting, "", "", "probe")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if err := db.DeleteConfig(ctx, rec.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}

	var got typedProbe
	if err := SettingInto(ctx, db, "probe", "", "", &got); err != nil {
		t.Fatalf("SettingInto from the mirror: %v", err)
	}
	if got.Port != 8080 {
		t.Errorf("Port = %d, want 8080", got.Port)
	}
	if got.Name != "123" {
		t.Errorf("Name = %q, want the string \"123\" (not the number 123)", got.Name)
	}
	if !got.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if got.Ratio != 1.5 {
		t.Errorf("Ratio = %v, want 1.5", got.Ratio)
	}
	if got.Big != math.MaxInt64 {
		t.Errorf("Big = %d, want %d (exact)", got.Big, int64(math.MaxInt64))
	}
	if got.Empty != "" {
		t.Errorf("Empty = %q, want the empty string", got.Empty)
	}

	// And the tag itself is in the column, not just the text.
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "probe.name"); err != nil || v.Kind != store.ValueKindString {
		t.Fatalf("probe.name row = %+v err=%v, want a tagged string", v, err)
	}
	if v, err := db.GetConfigValue(ctx, store.KindSetting, System, "", "probe.big"); err != nil || v.Value != bigLiteral {
		t.Fatalf("probe.big row = %+v err=%v, want the literal %s", v, err, bigLiteral)
	}
}

// TestFlattenDescendsIntoAnyJSONObject pins the collapse fix: the flattener
// picks the leaf boundary by structure, not by concrete Go type. An earlier
// version tested `v.(map[string]interface{})`, so a nested map[string]string,
// a map[string]SomeCfg or a plain struct was stored as one object-valued leaf
// — `tools.providers.searxng` where the mirror (flattenJSON) expects
// `tools.providers.searxng.endpoint`. That is the row reconcile reported as a
// gap on dev, and the same shape prod has as
// `skills.entries.<id>.env.app_i_d` for AppID.
func TestFlattenDescendsIntoAnyJSONObject(t *testing.T) {
	type providerCfg struct {
		Endpoint string `json:"endpoint"`
	}
	cases := []struct {
		name string
		data map[string]interface{}
		want []string
	}{
		{"nested map[string]string",
			map[string]interface{}{"searxng": map[string]string{"endpoint": "https://x"}},
			[]string{"tools.providers.searxng.endpoint"}},
		{"nested struct",
			map[string]interface{}{"searxng": providerCfg{Endpoint: "https://x"}},
			[]string{"tools.providers.searxng.endpoint"}},
		{"nested map of map",
			map[string]interface{}{"searxng": map[string]map[string]interface{}{"default": {"endpoint": "https://x"}}},
			[]string{"tools.providers.searxng.default.endpoint"}},
	}
	for i, c := range cases {
		db := openScopeDBNamed(t, fmt.Sprintf("flattendepth%d", i))
		if err := SaveSetting(context.Background(), db, "", "", "tools.providers", c.data); err != nil {
			t.Fatalf("%s: SaveSetting: %v", c.name, err)
		}
		kv, err := GetValues(context.Background(), db, store.KindSetting, "tools.providers.", "", "")
		if err != nil {
			t.Fatalf("%s: GetValues: %v", c.name, err)
		}
		got := make([]string, 0, len(kv))
		for k := range kv {
			got = append(got, k)
		}
		sort.Strings(got)
		sort.Strings(c.want)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: leaves = %v, want %v", c.name, got, c.want)
		}
		db.Close()
	}
}
