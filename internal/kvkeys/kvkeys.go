// Package kvkeys holds the naming rules shared by every reader and writer of
// the configs_kv shadow table.
//
// configs_kv flattens a config JSON blob into dotted single-value rows, so a
// row name mixes two kinds of segment:
//
//   - struct-field segments, stored snake_case from a camelCase Go field
//     (memory.autoPersist → memory.auto_persist). These convert back on read.
//   - data segments — user-supplied map keys such as tool category ids
//     (web_search), provider names, skill ids, team ids and skill env var
//     names. These must stay verbatim.
//
// The two converters are NOT mutually inverse for data keys: CamelToSnake only
// folds camelCase, so CamelToSnake("webSearch") and CamelToSnake("web_search")
// both land on "web_search". Re-casing a data segment on read silently renames
// the map key, and the runtime then misses its own lookup — that is exactly how
// the cloud dev incident presented (gateway.registerAgentToolChains looks up
// Tools["web_search"], finds "webSearch", registers no search tool and logs
// nothing at all). DataPaths is therefore the single source of truth for "this
// segment is data", used by both directions.
package kvkeys

import "strings"

// dataPaths enumerates the dotted KV names whose *last* segment is a
// user-supplied map key rather than a Go struct field name. "*" matches
// exactly one segment; every other element must match literally.
//
// These name the first key of every map that a config struct exposes —
// config.Config.Tools (keyed by category id), config.ToolProviders,
// config.Skills.Entries, config.Plugins.Entries, config.Teams, and the
// per-agent plugin opt-in map (configs row name "plugins.enabled"). Fields
// *below* such a key are struct fields again and convert normally; the
// free-form maps (see openPaths) are handled by the prefix rule instead.
var dataPaths = [][]string{
	{"tools", "categories", "*"},
	{"tools", "providers", "*"},
	{"skills", "entries", "*"},
	{"plugins", "entries", "*"},
	// The per-agent plugin opt-in row is keyed by plugin id
	// ({"<pluginID>": bool}) and shares neither shape nor kind with the
	// "plugins" settings namespace — see store.KindPluginEnabled. Without
	// this entry a camelCase or ALL_CAPS plugin id was folded on the way
	// into configs_kv (browserUse → browser_use), the same failure mode as
	// the original web_search incident.
	{"plugins", "enabled", "*"},
	{"teams", "*"},
}

// openPaths enumerates paths whose *value* is a free-form map — user data of
// unbounded depth (ToolProviderCfg.Options, SkillEntryCfg.Env,
// PluginEntryCfg.Config). Every segment strictly below such a path is a data
// key, at any depth: an exact-length table can only ever cover the first
// level, which silently renamed nested keys ("headers": {"X_API_Key": …} came
// back as xAPIKey, "mcpServers": {"my_server": …} as myServer) — the same
// failure mode as the original web_search incident, one level down.
var openPaths = [][]string{
	{"tools", "providers", "*", "options"},
	{"skills", "entries", "*", "env"},
	{"plugins", "entries", "*", "config"},
}

// Path splits a dotted KV prefix ("tools.categories." or "agent.") into its
// segments, dropping the trailing dot and any empty segment.
func Path(prefix string) []string {
	trimmed := strings.TrimSuffix(prefix, ".")
	if trimmed == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(trimmed, ".")+1)
	for _, seg := range strings.Split(trimmed, ".") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// IsDataKey reports whether the last element of path (a full dotted KV name,
// namespace included) is a map key rather than a struct field name.
func IsDataKey(path []string) bool {
	for _, pat := range dataPaths {
		if matchesExact(pat, path) {
			return true
		}
	}
	// Free-form containers: anything strictly below them is data, whatever its
	// depth. The container segment itself ("options", "env", "config") is a
	// struct field, hence the strict >.
	for _, pat := range openPaths {
		if len(path) > len(pat) && matchesPrefix(pat, path) {
			return true
		}
	}
	return false
}

// matchesExact reports whether path matches pat literally, "*" accepting any
// single segment.
func matchesExact(pat, path []string) bool {
	if len(pat) != len(path) {
		return false
	}
	return matchesPrefix(pat, path)
}

// matchesPrefix reports whether the first len(pat) segments of path match pat.
func matchesPrefix(pat, path []string) bool {
	if len(path) < len(pat) {
		return false
	}
	for i, want := range pat {
		if want != "*" && want != path[i] {
			return false
		}
	}
	return true
}

// StoredSegment returns the spelling to write to configs_kv for one JSON
// object key given the already-emitted row path before it (prefix must not
// alias the caller's slice; a fresh slice is built here).
func StoredSegment(prefix []string, key string) string {
	if IsDataKey(withKey(prefix, key)) {
		return key
	}
	return CamelToSnake(key)
}

// RestoredSegment returns the JSON object key for one stored segment, given
// the row path before it. Data keys come back verbatim; struct fields come
// back camelCase.
func RestoredSegment(prefix []string, seg string) string {
	// A segment without "_" is a fixed point of both directions, so the
	// shape lookup is skipped on this hot read path.
	if !strings.ContainsRune(seg, '_') {
		return seg
	}
	if IsDataKey(withKey(prefix, seg)) {
		return seg
	}
	return SnakeToCamel(seg)
}

// withKey appends key to a copy of prefix.
func withKey(prefix []string, key string) []string {
	out := make([]string, 0, len(prefix)+1)
	out = append(out, prefix...)
	return append(out, key)
}

// CamelToSnake converts a camelCase string to snake_case. ALL_CAPS and
// already_snake strings are lowercased only, so a token like
// REPLICATE_API_TOKEN doesn't become r_e_p_l_i_c_a_t_e__a_p_i__t_o_k_e_n.
// Data-key segments bypass this entirely — see dataPaths.
func CamelToSnake(s string) string {
	hasUpper, hasLower := false, false
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
	}
	if !hasUpper || !hasLower {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteByte(byte(r + 32))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SnakeToCamel converts a snake_case string to camelCase. The inverse of
// CamelToSnake for struct-field names only.
func SnakeToCamel(s string) string {
	var b []byte
	upper := false
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			upper = true
			continue
		}
		if upper && s[i] >= 'a' && s[i] <= 'z' {
			b = append(b, s[i]-32)
			upper = false
			continue
		}
		b = append(b, s[i])
		upper = false
	}
	return string(b)
}
