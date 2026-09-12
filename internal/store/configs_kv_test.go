package store

import (
	"context"
	"testing"
)

// TestConfigsKVCRUD covers the single-value configs_kv table operations:
// upsert, point read, prefix scan, single delete, prefix delete.
func TestConfigsKVCRUD(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.SetConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_key", StringValue("sk-1")); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	// Upsert overwrites.
	if err := db.SetConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_key", StringValue("sk-2")); err != nil {
		t.Fatalf("SetConfigValue upsert: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_base", StringValue("https://api.openai.com")); err != nil {
		t.Fatalf("SetConfigValue base: %v", err)
	}

	v, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_key")
	if err != nil {
		t.Fatalf("GetConfigValue: %v", err)
	}
	if v.Value != "sk-2" {
		t.Fatalf("GetConfigValue = %q, want %q", v, "sk-2")
	}

	// Prefix scan.
	m, err := db.ListConfigValues(ctx, KindProvider, "user", "user-a", "openai.")
	if err != nil {
		t.Fatalf("ListConfigValues prefix: %v", err)
	}
	if len(m) != 2 || m["openai.api_key"].Value != "sk-2" {
		t.Fatalf("ListConfigValues prefix = %v", m)
	}

	// Empty prefix scans the whole (kind, scope, scope_id) set.
	m, err = db.ListConfigValues(ctx, KindProvider, "user", "user-a", "")
	if err != nil {
		t.Fatalf("ListConfigValues all: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("ListConfigValues all len = %d, want 2", len(m))
	}

	// Delete a single value.
	if err := db.DeleteConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_base"); err != nil {
		t.Fatalf("DeleteConfigValue: %v", err)
	}
	if _, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a", "openai.api_base"); err == nil {
		t.Fatalf("expected ErrNotFound after DeleteConfigValue")
	}

	// Prefix delete removes the rest.
	if err := db.DeleteConfigPrefix(ctx, KindProvider, "user", "user-a", "openai."); err != nil {
		t.Fatalf("DeleteConfigPrefix: %v", err)
	}
	m, err = db.ListConfigValues(ctx, KindProvider, "user", "user-a", "")
	if err != nil {
		t.Fatalf("ListConfigValues after prefix delete: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("ListConfigValues after prefix delete len = %d, want 0", len(m))
	}
}

// TestMigrateConfigsToKV flattens legacy configs JSON rows into
// configs_kv single-value rows. It must preserve the per-(user, agent)
// layer as a distinct scope (fork adaptation — collapsing it onto the
// user layer would leak one agent's key across the user's other agents)
// and be idempotent.
func TestMigrateConfigsToKV(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Isolate: clear configs_kv so the migration actually runs even if an
	// earlier test left rows in the shared in-memory DB.
	if _, err := db.db.ExecContext(ctx, `DELETE FROM configs_kv`); err != nil {
		t.Fatalf("clear configs_kv: %v", err)
	}

	// Seed configs rows across all four ownership layers.
	rows := []*ConfigRecord{
		{Kind: KindProvider, UserID: "", AgentID: "", Name: "openai", Enabled: true,
			Data: map[string]interface{}{"apiKey": "sk-sys", "apiBase": "https://sys"}},
		{Kind: KindProvider, UserID: "user-a", AgentID: "", Name: "openai", Enabled: true,
			Data: map[string]interface{}{"apiKey": "sk-user"}},
		{Kind: KindProvider, UserID: "", AgentID: "agent-x", Name: "anthropic", Enabled: true,
			Data: map[string]interface{}{"apiKey": "sk-agent", "apiBase": "https://anthropic"}},
		{Kind: KindProvider, UserID: "user-a", AgentID: "agent-x", Name: "peragent", Enabled: true,
			Data: map[string]interface{}{"apiKey": "sk-per"}},
		{Kind: KindSetting, UserID: "user-a", AgentID: "", Name: "sandbox", Enabled: true,
			Data: map[string]interface{}{"enabled": true, "timeout": 60}},
		// Data-key segments (map keys) must survive migration verbatim:
		// flattening them through camelToSnake renamed web_search and
		// ALL_CAPS skill env vars, and the read path could not restore them.
		{Kind: KindSetting, UserID: "", AgentID: "", Name: "tools.categories", Enabled: true,
			Data: map[string]interface{}{"web_search": map[string]interface{}{"primary": "searxng/default"}}},
		{Kind: KindSetting, UserID: "", AgentID: "", Name: "skills.entries", Enabled: true,
			Data: map[string]interface{}{"web_search_skill": map[string]interface{}{
				"enabled": true,
				"env":     map[string]interface{}{"REPLICATE_API_TOKEN": "r8_x"},
			}}},
	}
	for _, r := range rows {
		if err := db.SaveConfig(ctx, r); err != nil {
			t.Fatalf("SaveConfig: %v", err)
		}
	}

	if err := db.migrateConfigsToKV(ctx); err != nil {
		t.Fatalf("migrateConfigsToKV: %v", err)
	}

	cases := []struct {
		kind, scope, scopeID, name string
		want                       string
	}{
		{KindProvider, "system", "", "openai.api_key", "sk-sys"},
		{KindProvider, "user", "user-a", "openai.api_key", "sk-user"},
		{KindProvider, "agent", "agent-x", "anthropic.api_key", "sk-agent"},
		{KindProvider, "user-agent", "user-a/agent-x", "peragent.api_key", "sk-per"},
		{KindSetting, "user", "user-a", "sandbox.enabled", "true"},
		{KindSetting, "user", "user-a", "sandbox.timeout", "60"},
		{KindSetting, "system", "", "tools.categories.web_search.primary", "searxng/default"},
		{KindSetting, "system", "", "skills.entries.web_search_skill.enabled", "true"},
		{KindSetting, "system", "", "skills.entries.web_search_skill.env.REPLICATE_API_TOKEN", "r8_x"},
	}
	for _, c := range cases {
		v, err := db.GetConfigValue(ctx, c.kind, c.scope, c.scopeID, c.name)
		if err != nil {
			t.Fatalf("GetConfigValue(%s,%s,%s,%s): %v", c.kind, c.scope, c.scopeID, c.name, err)
		}
		if v.Value != c.want {
			t.Fatalf("migrated %s/%s/%s/%s = %q, want %q", c.kind, c.scope, c.scopeID, c.name, v, c.want)
		}
	}

	// Idempotent: a second run skips because configs_kv already has data
	// and leaves existing rows untouched.
	if err := db.migrateConfigsToKV(ctx); err != nil {
		t.Fatalf("second migrateConfigsToKV: %v", err)
	}
	v, err := db.GetConfigValue(ctx, KindProvider, "system", "", "openai.api_key")
	if err != nil || v.Value != "sk-sys" {
		t.Fatalf("after second migrate: v=%q err=%v", v, err)
	}
}

// TestCamelToSnakeAllCaps pins the a49f9d4 fix: ALL_CAPS and already_snake
// strings pass through (lowercased only) instead of getting a per-char
// underscore. REPLICATE_API_TOKEN was becoming r_e_p_l_i_c_a_t_e__a_p_i__t_o_k_e_n.
// TestMigrateConfigsToKV_AllCapsKey is the migration-path regression guard
// for a49f9d4: a provider whose Data contains an ALL_CAPS key (e.g. an env
// token like REPLICATE_API_TOKEN) must flatten to a clean dotted key, not
// the per-char-underscore mangle the old camelToSnake produced.
func TestMigrateConfigsToKV_AllCapsKey(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.db.ExecContext(ctx, `DELETE FROM configs_kv`); err != nil {
		t.Fatalf("clear configs_kv: %v", err)
	}
	if err := db.SaveConfig(ctx, &ConfigRecord{
		Kind: KindProvider, UserID: "user-a", AgentID: "", Name: "replicate", Enabled: true,
		Data: map[string]interface{}{
			"apiBase":             "https://api.replicate.com",
			"REPLICATE_API_TOKEN": "r8_abc123",
		},
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := db.migrateConfigsToKV(ctx); err != nil {
		t.Fatalf("migrateConfigsToKV: %v", err)
	}

	v, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a", "replicate.replicate_api_token")
	if err != nil || v.Value != "r8_abc123" {
		t.Fatalf("migrated ALL_CAPS key = %q err=%v; want replicate.replicate_api_token=r8_abc123", v, err)
	}
	// The mangled per-char form must NOT exist.
	if _, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a",
		"replicate.r_e_p_l_i_c_a_t_e__a_p_i__t_o_k_e_n"); err == nil {
		t.Fatalf("mangled per-char key still present after fix")
	}
}

// TestConfigsKVLIKEUnderscoreIsLiteral pins the prefix-scan escaping. "_" and
// "%" are LIKE metacharacters, so the unescaped pattern built from a literal
// prefix let one provider's rows bleed into another's scan — and, worse,
// DeleteConfigPrefix removed rows that only matched the wildcard. Provider
// names and skill ids come straight from the API, so prefixes are not
// guaranteed metacharacter-free.
func TestConfigsKVLIKEUnderscoreIsLiteral(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	names := []string{"foo_bar.apiKey", "fooXbar.apiKey", "pct%bar.apiKey", "pctZbar.apiKey"}
	for _, name := range names {
		if err := db.SetConfigValue(ctx, KindProvider, "user", "user-a", name, StringValue("v")); err != nil {
			t.Fatalf("SetConfigValue(%s): %v", name, err)
		}
	}

	// Prefix scan: only the literal-prefix row comes back.
	got, err := db.ListConfigValues(ctx, KindProvider, "user", "user-a", "foo_bar.")
	if err != nil {
		t.Fatalf("ListConfigValues foo_bar.: %v", err)
	}
	if len(got) != 1 || got["foo_bar.apiKey"].Value != "v" {
		t.Fatalf("ListConfigValues foo_bar. = %v, want only foo_bar.apiKey", got)
	}
	got, err = db.ListConfigValues(ctx, KindProvider, "user", "user-a", "pct%bar.")
	if err != nil {
		t.Fatalf("ListConfigValues pct%%bar.: %v", err)
	}
	if len(got) != 1 || got["pct%bar.apiKey"].Value != "v" {
		t.Fatalf("ListConfigValues pct%%bar. = %v, want only pct%%bar.apiKey", got)
	}

	// Prefix delete: the wildcard look-alikes survive.
	if err := db.DeleteConfigPrefix(ctx, KindProvider, "user", "user-a", "foo_bar."); err != nil {
		t.Fatalf("DeleteConfigPrefix foo_bar.: %v", err)
	}
	if err := db.DeleteConfigPrefix(ctx, KindProvider, "user", "user-a", "pct%bar."); err != nil {
		t.Fatalf("DeleteConfigPrefix pct%%bar.: %v", err)
	}
	for _, name := range []string{"fooXbar.apiKey", "pctZbar.apiKey"} {
		if v, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a", name); err != nil || v.Value != "v" {
			t.Fatalf("%s = %q err=%v, want it untouched by the look-alike prefix delete", name, v, err)
		}
	}
	for _, name := range []string{"foo_bar.apiKey", "pct%bar.apiKey"} {
		if _, err := db.GetConfigValue(ctx, KindProvider, "user", "user-a", name); err == nil {
			t.Fatalf("%s survived its own prefix delete", name)
		}
	}
}

// TestDeleteAgentEscapesLookalikeScopeIDs pins the LIKE escaping on the
// cascade delete. scope_id is "<user>/<agent>", and agent ids are "agt_…" —
// the "_" is a LIKE wildcard, so the unescaped pattern "%/agt_1" also matched
// the rows of an unrelated agent "agtX1" and deleted them.
func TestDeleteAgentEscapesLookalikeScopeIDs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	for _, ag := range []AgentRecord{
		{ID: "agt_1", UserID: "u_a", Name: "one"},
		{ID: "agtX1", UserID: "u_b", Name: "two"},
	} {
		if err := db.SaveAgent(ctx, &ag); err != nil {
			t.Fatalf("SaveAgent(%s): %v", ag.ID, err)
		}
	}
	if err := db.SetConfigValue(ctx, KindSetting, "user-agent", "u_a/agt_1", "bindings.timezone", StringValue("drop")); err != nil {
		t.Fatalf("SetConfigValue doppelganger target: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "user-agent", "u_b/agtX1", "bindings.timezone", StringValue("keep")); err != nil {
		t.Fatalf("SetConfigValue lookalike: %v", err)
	}

	if err := db.DeleteAgent(ctx, "agt_1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, err := db.GetConfigValue(ctx, KindSetting, "user-agent", "u_a/agt_1", "bindings.timezone"); err == nil {
		t.Fatalf("deleted agent's own user-agent rows survived")
	}
	if v, err := db.GetConfigValue(ctx, KindSetting, "user-agent", "u_b/agtX1", "bindings.timezone"); err != nil || v.Value != "keep" {
		t.Fatalf("lookalike agent's row = %q err=%v, want it untouched", v, err)
	}
}

// TestDeleteUserEscapesLookalikeScopeIDs is the same guard for the user
// cascade: user ids are "u_…", so an unescaped "<id>/%" pattern also matched
// (and deleted) another user's per-(user, agent) override rows.
func TestDeleteUserEscapesLookalikeScopeIDs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.CreateUser(ctx, &UserRecord{
		ID: "u_a", Username: "a", Email: "a@example.com",
		PasswordHash: "x", Role: "user", Status: "active",
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "user-agent", "u_a/agt_9", "bindings.timezone", StringValue("drop")); err != nil {
		t.Fatalf("SetConfigValue doppelganger target: %v", err)
	}
	if err := db.SetConfigValue(ctx, KindSetting, "user-agent", "uXa/agt_9", "bindings.timezone", StringValue("keep")); err != nil {
		t.Fatalf("SetConfigValue lookalike: %v", err)
	}

	if err := db.DeleteUser(ctx, "u_a"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := db.GetConfigValue(ctx, KindSetting, "user-agent", "u_a/agt_9", "bindings.timezone"); err == nil {
		t.Fatalf("deleted user's own user-agent rows survived")
	}
	if v, err := db.GetConfigValue(ctx, KindSetting, "user-agent", "uXa/agt_9", "bindings.timezone"); err != nil || v.Value != "keep" {
		t.Fatalf("lookalike user's row = %q err=%v, want it untouched", v, err)
	}
}
