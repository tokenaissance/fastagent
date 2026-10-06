// Package scope reads (user, agent)-keyed rows out of store.configs and
// merges them into the flat shapes the runtime expects.
//
// Every row in configs carries a (kind, user_id, agent_id, name) tuple.
// Resolution walks ownership outer→inner, with inner rows shadowing outer
// rows by `name`:
//
//	system (user='', agent='') →
//	  user (user=X, agent='')   →
//	    agent (user='', agent=Y) →
//	      per-(user, agent) (user=X, agent=Y)
//
// kind="provider": name is the provider key ("openai"). Inner rows
//
//	replace outer entries entirely (no field-level merge).
//
// kind="channel":  name is the channel type ("telegram"). A disabled inner
//
//	row erases the outer entry — lets a user opt out of a system-wide bot.
//
// kind="setting":  name is the namespace ("agents.defaults", "sandbox", …).
//
//	Top-level keys merge field-wise; inner-scope keys win.
//
// The `enabled` column is part of the resolution model, not a leftover of
// the channels table: a row with Enabled=false is this layer's decision
// that the name does not exist. It contributes no value AND it vetoes every
// outer layer's entry of the same name, so an inner layer can switch a
// name off instead of silently falling through to the operator's value.
// Inner layers may re-enable it again.
//
// That veto is what makes "disable" mean something in each kind:
//
//   - kind="provider"/"channel" merge by whole entry, so a disabled inner
//     row removes the outer entry (a user opting out of a system-wide bot
//     or credential must not get it back).
//   - kind="setting" merges field-wise, so a disabled inner row drops the
//     fields the outer layers contributed for that namespace.
//
// A disabled row is still a row: it also blocks the configs_kv fallback,
// which is only consulted when no blob row for the name exists at all.
package scope

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/kvkeys"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// HTTP-layer scope identifiers. The storage layer keys configs by
// (user_id, agent_id) directly; these constants exist for the HTTP API
// contract (URL ?scope= params, dashboard scope picker). Translate to
// storage form via OwnershipFromScope.
const (
	System    = "system"
	User      = "user"
	Agent     = "agent"
	UserAgent = "user-agent"
)

// OwnershipFromScope converts the HTTP-side (scope, scopeID) pair into
// the storage (user_id, agent_id) pair. Empty/unknown scope returns
// ("", "") which the store reads as "system / global".
func OwnershipFromScope(sc, scopeID string) (userID, agentID string) {
	switch sc {
	case User:
		return scopeID, ""
	case Agent:
		return "", scopeID
	default:
		return "", ""
	}
}

// ScopeFromOwnership is the inverse, used when emitting (scope, scopeID)
// back to the dashboard JSON. (X, Y) — both filled — is rendered as
// scope="user-agent" so the UI can tell apart per-(user, agent)
// overrides from plain user or agent rows. Today the dashboard only
// reads scope="system"/"user"/"agent"; the new compound keeps the door
// open for the multi-tenant view.
func ScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
	switch {
	case userID != "" && agentID != "":
		return "user-agent", userID + "/" + agentID
	case userID != "":
		return User, userID
	case agentID != "":
		return Agent, agentID
	default:
		return System, ""
	}
}

// Providers returns the merged map of LLM provider configs for a given
// (user, agent). Pass agentID="" to get only the user-level view. Pass
// both empty to get system-only.
func Providers(ctx context.Context, st store.ConfigReadStore, userID, agentID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.Providers: store is required")
	}
	// The legacy blob is authoritative — it keeps exact JSON types and the
	// complete key set, while configs_kv is a derived mirror that can be
	// partial or lossy (see Setting). Which table each layer is read from is
	// configsReadAuthority's decision, made per layer by providersLayerAt: under
	// configsKVFirst a name whose marker certifies its mirror is decided by the
	// mirror, everything else by the blob. Deciding per name instead of per
	// chain is what keeps a provider that exists only in the mirror visible next
	// to blob-backed siblings.
	out := map[string]config.ProviderConfig{}
	decided := map[string]bool{}
	applyLayer := func(uid, aid string) error {
		lay, err := providersLayerAt(ctx, st, uid, aid)
		if err != nil {
			return err
		}
		for name := range lay.vetoed {
			decided[name] = true
			delete(out, name)
		}
		for name, pc := range lay.set {
			decided[name] = true
			out[name] = pc
		}
		return nil
	}
	// system layer
	if err := applyLayer("", ""); err != nil {
		return nil, err
	}
	// user layer
	if userID != "" {
		if err := applyLayer(userID, ""); err != nil {
			return nil, err
		}
	}
	// agent layer
	if agentID != "" {
		if err := applyLayer("", agentID); err != nil {
			return nil, err
		}
	}
	// per-(user, agent) layer
	if userID != "" && agentID != "" {
		if err := applyLayer(userID, agentID); err != nil {
			return nil, err
		}
	}
	if kvProvs, err := providersFromKV(ctx, st, userID, agentID); err == nil {
		for name, pc := range kvProvs {
			if !decided[name] {
				out[name] = pc
			}
		}
	}
	return out, nil
}

// providerLayer is one ownership layer's provider decisions: the names it
// offers (set) and the names it switches off (vetoed, which erase the outer
// entry). A name in either map is one the layer decided, so the merged walk
// leaves it out of the last-resort fallback.
type providerLayer struct {
	set    map[string]config.ProviderConfig
	vetoed map[string]bool
}

// providersLayerAt resolves one ownership layer's providers, honoring
// configsReadAuthority. The blob rows are always read; under configsKVFirst a name
// whose marker certifies its mirror leaves overrides the blob's decision for
// that name (payload and veto), and a name the mirror holds without
// certification is left undecided — the blob answers if it has the name, and
// otherwise the merged walk's last-resort fallback does.
func providersLayerAt(ctx context.Context, st store.ConfigReadStore, userID, agentID string) (providerLayer, error) {
	lay := providerLayer{set: map[string]config.ProviderConfig{}, vetoed: map[string]bool{}}
	rows, err := st.ListConfigs(ctx, store.KindProvider, userID, agentID)
	if err != nil {
		return lay, err
	}
	for _, r := range rows {
		if !r.Enabled {
			lay.vetoed[r.Name] = true
			delete(lay.set, r.Name)
			continue
		}
		lay.set[r.Name] = providerToConfig(r)
	}
	if configsReadAuthority != configsKVFirst {
		return lay, nil
	}
	sc, sid := kvScopeFromOwnership(userID, agentID)
	kvVals, err := st.ListConfigValues(ctx, store.KindProvider, sc, sid, "")
	if err != nil || len(kvVals) == 0 {
		return lay, nil
	}
	// One marker query for the whole layer, not one per provider name.
	markers, err := st.ListConfigMirrors(ctx, store.KindProvider, sc, sid)
	if err != nil {
		return lay, nil
	}
	for name, leaves := range groupProviderLeaves(kvVals) {
		m, certified := certifiedMirrorIn(markers, name, leaves)
		if !certified {
			continue
		}
		pc, ok := kvValsToProviders(leaves)[name]
		if !ok {
			pc = config.ProviderConfig{}
		}
		if m.Enabled != nil && !*m.Enabled {
			lay.vetoed[name] = true
			delete(lay.set, name)
			continue
		}
		delete(lay.vetoed, name)
		lay.set[name] = pc
	}
	return lay, nil
}

// providersFromKV reads all provider KV values with scope merge and
// reconstructs them into ProviderConfig structs. The caller merges the
// result per provider name, keeping names the blob already decided.
func providersFromKV(ctx context.Context, st store.ConfigReader, userID, agentID string) (map[string]config.ProviderConfig, error) {
	kvVals, err := GetValues(ctx, st, store.KindProvider, "", userID, agentID)
	if err != nil || len(kvVals) == 0 {
		return nil, err
	}
	return kvValsToProviders(kvVals), nil
}

// kvValsToProviders is a helper that converts flat KV pairs (from a single
// scope) into a provider map. Used by AgentScopeProviders and
// UserScopeProviders where scope merge is not needed.
func kvValsToProviders(kvVals map[string]store.ConfigValue) map[string]config.ProviderConfig {
	grouped := groupProviderLeaves(kvVals)
	out := make(map[string]config.ProviderConfig, len(grouped))
	for provName, leaves := range grouped {
		fields := make(map[string]store.ConfigValue, len(leaves))
		for fullKey, value := range leaves {
			fields[fullKey[len(provName)+1:]] = value
		}
		m := kvFieldMap(fields)
		blob, _ := json.Marshal(m)
		var pc config.ProviderConfig
		// Never swallow the mirror error: a tagged row that still fails
		// here (or an untagged row the legacy guesser re-typed — "123456" →
		// number) used to lose the field without a trace, so an all-digit
		// apiKey read back empty.
		if err := json.Unmarshal(blob, &pc); err != nil {
			slog.Warn("configs_kv provider mirror failed; provider skipped",
				"provider", provName, "error", err)
			continue
		}
		out[provName] = pc
	}
	return out
}

// groupProviderLeaves splits a scope's flat provider rows by provider name —
// the first dotted segment, which is the ConfigsKVPrefixFor layout ("<name>.").
// The keys stay full ("openai.api_key"), not stripped, so each name's leaf set
// can be handed to the completeness marker: ConfigsKVFingerprint covers the full
// names, so stripping them here would make the fingerprint check fail.
func groupProviderLeaves(kvVals map[string]store.ConfigValue) map[string]map[string]store.ConfigValue {
	out := map[string]map[string]store.ConfigValue{}
	for fullKey, value := range kvVals {
		idx := strings.IndexByte(fullKey, '.')
		if idx < 0 {
			continue
		}
		name := fullKey[:idx]
		if out[name] == nil {
			out[name] = map[string]store.ConfigValue{}
		}
		out[name][fullKey] = value
	}
	return out
}

// kvFieldMap rebuilds the camelCase JSON object for one config entry from its
// flat KV field rows.
//
// A tagged row is decoded by its tag. An untagged (legacy) row keeps the
// conservative rule this reader has always used: object/array text is decoded
// (a "models" row came back as a slice) and every scalar passes through as the
// raw text, because a stored "123" is indistinguishable from the string "123"
// and guessing wrong dropped the field when mirroring — an all-digit api_key
// read back empty. Tagged rows do not need the workaround: the writer said
// what the value was.
func kvFieldMap(fields map[string]store.ConfigValue) map[string]interface{} {
	m := make(map[string]interface{}, len(fields))
	for storedKey, value := range fields {
		camelKey := snakeToCamel(storedKey)
		if value.Kind != "" {
			m[camelKey] = value.Decode()
			continue
		}
		m[camelKey] = value.DecodeLegacyStructure()
	}
	return m
}

// AgentScopeProviders returns providers stored at (user='', agent=Y)
// only — the agent's "official" rows, without system or user layers
// merged in. Use this to overlay an agent's own rows on top of an
// already system+user-merged view: re-running the full Providers walk
// would re-apply outer layers and silently clobber any user-scope
// override the caller already merged in.
func AgentScopeProviders(ctx context.Context, st store.ConfigReadStore, agentID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.AgentScopeProviders: store is required")
	}
	if agentID == "" {
		return map[string]config.ProviderConfig{}, nil
	}
	return ProvidersAt(ctx, st, "", agentID)
}

// UserScopeProviders returns providers stored at (user=X, agent='')
// only — the user's personal rows, without the system layer. Used by
// the foreign-agent path so a viewer can fall back to the owner's
// provider credentials without dragging the owner's full merged view
// (which would re-apply system rows on top of the viewer's already-
// merged set).
func UserScopeProviders(ctx context.Context, st store.ConfigReadStore, userID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.UserScopeProviders: store is required")
	}
	if userID == "" {
		return map[string]config.ProviderConfig{}, nil
	}
	return ProvidersAt(ctx, st, userID, "")
}

// kvPrefixForNamespace maps a settings namespace onto the configs_kv name
// prefix its rows are flattened under. Only agents.defaults differs: its
// rows have always been written under the shorter "agent." prefix (both by
// dualWriteSettingKV and by the configs→kv migration), so every reader and
// writer must agree on it.
func kvPrefixForNamespace(namespace string) string {
	return store.ConfigsKVPrefixFor(store.KindSetting, namespace)
}

// ExactSetting reads one setting namespace at exactly one (userID, agentID)
// scope — no layer merge. It is the settings-side counterpart of
// AgentScopeProviders / UserScopeProviders, for callers that overlay an
// inner layer on top of an already-merged view (the gateway overlays the
// agent layer and the per-(caller,agent) layer of tools.* this way).
// Precedence matches Setting: the legacy configs blob first, configs_kv only
// when no blob row exists.
//
// Missing row is not an error: dst is left untouched.
func ExactSetting(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string, dst interface{}) error {
	if st == nil {
		return errors.New("scope.ExactSetting: store is required")
	}
	// One resolution rule for every single-scope settings read: settingAtRaw
	// owns blob-first / veto / mirror-fallback, and the typed form mirrors it
	// rather than being a second implementation of the same rule.
	m, err := settingAtRaw(ctx, st, namespace, userID, agentID)
	if err != nil {
		return err
	}
	if len(m) == 0 {
		return nil
	}
	return jsonInto(m, dst)
}

// UserScopeSetting loads one setting namespace at (user=X, agent='') only —
// the user's personal row, without the system layer merged in. Thin wrapper
// over ExactSetting kept for the callers that express "the user layer".
func UserScopeSetting(ctx context.Context, st store.ConfigReadStore, namespace, userID string, dst interface{}) error {
	if st == nil {
		return errors.New("scope.UserScopeSetting: store is required")
	}
	if userID == "" {
		return nil
	}
	return ExactSetting(ctx, st, namespace, userID, "", dst)
}

// Channels returns the merged channel map. Disabled rows in an inner
// scope erase the outer entry.
func Channels(ctx context.Context, st store.ConfigReader, userID, agentID string) (map[string]config.ChannelConfig, error) {
	if st == nil {
		return nil, errors.New("scope.Channels: store is required")
	}
	out := map[string]config.ChannelConfig{}
	apply := func(rows []store.ConfigRecord) {
		for _, r := range rows {
			if !r.Enabled {
				delete(out, r.Name)
				continue
			}
			out[r.Name] = channelToConfig(r)
		}
	}
	if rows, err := st.ListConfigs(ctx, store.KindChannel, "", ""); err != nil {
		return nil, err
	} else {
		apply(rows)
	}
	if userID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, userID, ""); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	if agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, "", agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	if userID != "" && agentID != "" {
		if rows, err := st.ListConfigs(ctx, store.KindChannel, userID, agentID); err != nil {
			return nil, err
		} else {
			apply(rows)
		}
	}
	return out, nil
}

// Setting returns the merged JSON for one namespace across the
// system → user → agent → per-(user, agent) chain. Field-level merge on
// the top-level map; inner-ownership fields override outer ones. Unset
// namespaces yield an empty map without erroring — callers Unmarshal
// into typed structs and rely on zero-valued fields.
//
// The legacy configs blob is the authority and configs_kv mirrors it:
// the blob keeps exact JSON types and the complete key set, while the mirror
// is written row-by-row (non-transactionally) and re-types values on read. A
// partial or lossy mirror therefore must not shadow the blob — that produced
// namespaces that silently lost every key the mirror happened not to carry,
// and (via parseKVValue) string fields holding number-like values. The mirror
// is consulted only when the blob has no row at all.
//
// Which table each layer is read from is configsReadAuthority's decision, made
// per layer by settingLayerAt: under configsKVFirst a layer whose marker certifies
// its mirror answers (and only then — an uncertified row
// falls back to the blob), so the flip is the same one lever the single-scope
// resolvers pull. The last-resort walk of GetValues still serves rows that have
// no blob row and no marker at all (rows written straight into configs_kv),
// unchanged.
func Setting(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, error) {
	if st == nil {
		return nil, errors.New("scope.Setting: store is required")
	}

	// Resolve each layer through the one per-layer rule, then merge. The merge
	// itself (the veto / field-merge order) lives in mergeSettingLayers, shared
	// with the batched resolver so the two cannot drift.
	layers := settingLayerIDs(userID, agentID)
	views := make([]settingLayerView, len(layers))
	for i, l := range layers {
		data, enabled, present, err := settingLayerAt(ctx, st, namespace, l[0], l[1])
		if err != nil {
			return nil, err
		}
		views[i] = settingLayerView{data: data, enabled: enabled, present: present}
	}
	out, sawRow := mergeSettingLayers(views)
	if sawRow {
		return out, nil
	}
	// No layer owned the namespace in either table: serve the merged raw mirror
	// (rows written straight into configs_kv have no blob counterpart).
	kvPrefix := kvPrefixForNamespace(namespace)
	if kvVals, err := GetValues(ctx, st, store.KindSetting, kvPrefix, userID, agentID); err == nil && len(kvVals) > 0 {
		return kvToSettingMap(kvPrefix, kvVals), nil
	}
	return out, nil
}

// kvToSettingMap converts flat KV pairs back into the camelCase map that
// callers (SettingInto, assembleConfig) expect. The prefix is stripped,
// struct-field segments are converted back to camelCase, and map-key
// segments (see kvkeys.dataPaths) are kept verbatim. Nested dots produce
// nested maps.
//
// Each leaf is decoded by its own value_kind tag, so a stored "123" comes
// back as the string "123" and a stored 123 as a number — an untagged row
// still falls back to the legacy guess.
func kvToSettingMap(prefix string, kv map[string]store.ConfigValue) map[string]interface{} {
	out := map[string]interface{}{}
	// The namespace seeds the path matched against kvkeys.dataPaths.
	// agents.defaults is stored under the "agent." prefix, which matches
	// no pattern — it has no map-keyed children.
	nsSegments := kvkeys.Path(prefix)
	for fullKey, value := range kv {
		// Strip the prefix to get the relative key.
		relKey := fullKey
		if len(prefix) > 0 && len(fullKey) > len(prefix) {
			relKey = fullKey[len(prefix):]
		}
		// Rebuild nested maps from dotted keys. Struct-field segments are
		// converted snake_case→camelCase, map-key segments stay verbatim;
		// the leaf holds the parsed value (bool / number / JSON object /
		// string). Empty segments (stray or trailing dots) are tolerated
		// and dropped.
		segments := make([]string, 0, strings.Count(relKey, ".")+1)
		for _, seg := range strings.Split(relKey, ".") {
			if seg != "" {
				segments = append(segments, seg)
			}
		}
		if len(segments) == 0 {
			continue
		}
		node := out
		for i, seg := range segments {
			key := seg
			// RestoredSegment only rewrites segments containing "_", so
			// non-candidates skip the path build entirely.
			if strings.IndexByte(seg, '_') >= 0 {
				path := make([]string, 0, len(nsSegments)+i)
				path = append(path, nsSegments...)
				path = append(path, segments[:i]...)
				key = kvkeys.RestoredSegment(path, seg)
			}
			if i == len(segments)-1 {
				node[key] = value.Decode()
				continue
			}
			child, ok := node[key].(map[string]interface{})
			if !ok {
				child = map[string]interface{}{}
				node[key] = child
			}
			node = child
		}
	}
	return out
}

// snakeToCamel converts a snake_case string to camelCase. Thin alias for
// kvkeys.SnakeToCamel, kept so callers (and the round-trip test) express the
// codec in the storage direction they care about.
func snakeToCamel(s string) string { return kvkeys.SnakeToCamel(s) }

// There is deliberately no value parser here any more. Values carry their
// JSON type in configs_kv.value_kind (store.ConfigValue), so the read path
// restores the type the writer recorded instead of re-deriving it from the
// text. The old heuristic that used to live at this spot — parseKVValue —
// survives only as store.decodeLegacyValue, for rows written before the tag
// existed.

// SettingInto resolves Setting and unmarshals the merged JSON into dst.
// Convenience for callers that want a typed config block.
func SettingInto(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string, dst interface{}) error {
	merged, err := Setting(ctx, st, namespace, userID, agentID)
	if err != nil {
		return err
	}
	if len(merged) == 0 {
		return nil
	}
	if err := jsonInto(merged, dst); err != nil {
		// configs_kv cannot represent every value faithfully: parseKVValue
		// re-types the stored string ("123" → number, "true" → bool,
		// "{…}"/"[…]" → object/array), which then fails to unmarshal into a
		// string/bool field and takes the whole namespace down — in the
		// gateway that means the caller's UserSpace never loads. The legacy
		// configs blob keeps exact JSON types, so retry there before giving
		// up. Rebuild into a fresh value: a failed Unmarshal may have left
		// dst partially populated.
		kvErr := err
		if st != nil && dst != nil {
			if rec, rerr := st.GetConfigByName(ctx, store.KindSetting, userID, agentID, namespace); rerr == nil && rec != nil && len(rec.Data) > 0 {
				fresh := reflect.New(reflect.TypeOf(dst).Elem()).Interface()
				if berr := jsonInto(rec.Data, fresh); berr == nil {
					reflect.ValueOf(dst).Elem().Set(reflect.ValueOf(fresh).Elem())
					slog.Warn("configs_kv mirror failed; served from the legacy blob",
						"namespace", namespace, "user", userID, "agent", agentID, "error", kvErr)
					return nil
				}
			}
		}
		return kvErr
	}
	return nil
}

// jsonInto maps a rebuilt settings map onto a typed config struct — the
// same marshal/unmarshal hop SettingInto has always used, shared with
// UserScopeSetting so both paths agree on the codec.
func jsonInto(v interface{}, dst interface{}) error {
	blob, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(blob, dst)
}

// BatchSettings resolves multiple namespaces in one ListConfigs call per
// ownership layer instead of N point lookups. It walks the same layers, in
// the same order, and applies the same enabled/veto rule as Setting — the
// batch form exists only to replace the per-namespace queries, and the two
// must not be able to disagree. TestBatchSettings_MatchesSetting pins that.
func BatchSettings(
	ctx context.Context,
	st store.ConfigReadStore,
	namespaces []string,
	userID, agentID string,
) (map[string]map[string]interface{}, error) {
	if st == nil {
		return nil, errors.New("scope.BatchSettings: store is required")
	}

	if configsReadAuthority == configsKVFirst {
		return batchSettingsConfigsKVFirst(ctx, st, namespaces, userID, agentID)
	}

	nsSet := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = struct{}{}
	}

	// One query per ownership layer, outer→inner — the same four layers
	// Setting walks, so the two resolvers cannot drift apart again.
	layers := settingLayerIDs(userID, agentID)

	merged := make(map[string]map[string]interface{}, len(namespaces))
	seen := make(map[string]bool, len(namespaces))
	for _, layer := range layers {
		rows, err := st.ListConfigs(ctx, store.KindSetting, layer[0], layer[1])
		if err != nil {
			return nil, fmt.Errorf("scope.BatchSettings: load %q/%q configs: %w", layer[0], layer[1], err)
		}
		for _, row := range rows {
			if _, ok := nsSet[row.Name]; !ok {
				continue
			}
			seen[row.Name] = true
			if !row.Enabled {
				// Veto: drop what the outer layers contributed, and drop the
				// row's own data — a disabled row carries no payload.
				merged[row.Name] = map[string]interface{}{}
				continue
			}
			if merged[row.Name] == nil {
				merged[row.Name] = map[string]interface{}{}
			}
			for k, v := range row.Data {
				merged[row.Name][k] = v
			}
		}
	}

	out := make(map[string]map[string]interface{}, len(namespaces))
	for _, ns := range namespaces {
		if seen[ns] {
			// The chain owns this namespace. An empty map here is the veto /
			// empty-row outcome, and the mirror must not resurrect it.
			if len(merged[ns]) > 0 {
				out[ns] = merged[ns]
			}
			continue
		}
		// No blob row anywhere: serve the mirror (rows written straight into
		// configs_kv have no blob counterpart). Setting is the definition of
		// this contract, so reuse it rather than re-deriving the rule.
		fromMirror, err := Setting(ctx, st, ns, userID, agentID)
		if err != nil {
			return nil, fmt.Errorf("scope.BatchSettings: resolve %q: %w", ns, err)
		}
		if len(fromMirror) > 0 {
			out[ns] = fromMirror
		}
	}
	return out, nil
}

// batchSettingsConfigsKVFirst is BatchSettings under configsKVFirst. Markers are per
// row, so the blob-first batch's one-query-per-layer trick cannot certify them;
// this walks the same four layers but fetches each layer's three sources once —
// the blob rows, every mirror leaf at the scope, and every marker at the scope
// (ListConfigMirrors) — and then certifies each requested namespace locally.
// The per-namespace merge is mergeSettingLayers, the same rule Setting applies,
// so the two resolvers cannot disagree. Query count is O(layers), not
// O(namespaces × layers).
func batchSettingsConfigsKVFirst(
	ctx context.Context,
	st store.ConfigReadStore,
	namespaces []string,
	userID, agentID string,
) (map[string]map[string]interface{}, error) {
	prefixOf := make(map[string]string, len(namespaces))
	for _, ns := range namespaces {
		prefixOf[ns] = kvPrefixForNamespace(ns)
	}

	layers := settingLayerIDs(userID, agentID)
	blobAt := make([]map[string]*store.ConfigRecord, len(layers))
	leavesAt := make([]map[string]map[string]store.ConfigValue, len(layers))
	markersAt := make([]map[string]store.ConfigMirror, len(layers))
	for i, l := range layers {
		uid, aid := l[0], l[1]
		rows, err := st.ListConfigs(ctx, store.KindSetting, uid, aid)
		if err != nil {
			return nil, fmt.Errorf("scope.BatchSettings: load %q/%q configs: %w", uid, aid, err)
		}
		sc, sid := kvScopeFromOwnership(uid, aid)
		allLeaves, err := st.ListConfigValues(ctx, store.KindSetting, sc, sid, "")
		if err != nil {
			return nil, fmt.Errorf("scope.BatchSettings: load %q/%q mirror: %w", uid, aid, err)
		}
		markers, err := st.ListConfigMirrors(ctx, store.KindSetting, sc, sid)
		if err != nil {
			return nil, fmt.Errorf("scope.BatchSettings: load %q/%q markers: %w", uid, aid, err)
		}
		blob := make(map[string]*store.ConfigRecord, len(namespaces))
		for j := range rows {
			if _, want := prefixOf[rows[j].Name]; want {
				blob[rows[j].Name] = &rows[j]
			}
		}
		byNS := make(map[string]map[string]store.ConfigValue, len(namespaces))
		for ns, prefix := range prefixOf {
			if lv := leavesUnderPrefix(allLeaves, prefix); len(lv) > 0 {
				byNS[ns] = lv
			}
		}
		blobAt[i], leavesAt[i], markersAt[i] = blob, byNS, markers
	}

	out := make(map[string]map[string]interface{}, len(namespaces))
	for _, ns := range namespaces {
		prefix := prefixOf[ns]
		views := make([]settingLayerView, len(layers))
		for i := range layers {
			// A marker-certified mirror answers the layer; otherwise the
			// blob row does — the same order settingLayerAt applies.
			if lv := leavesAt[i][ns]; len(lv) > 0 {
				if m, ok := certifiedMirrorIn(markersAt[i], ns, lv); ok {
					views[i] = settingLayerView{
						data:    kvToSettingMap(prefix, lv),
						enabled: m.Enabled != nil && *m.Enabled,
						present: true,
					}
					continue
				}
			}
			if rec := blobAt[i][ns]; rec != nil {
				views[i] = settingLayerView{data: rec.Data, enabled: rec.Enabled, present: true}
			}
		}
		merged, saw := mergeSettingLayers(views)
		if !saw {
			// No layer owned the namespace in either table: the last resort is
			// the merged raw mirror, exactly as Setting does.
			raw := map[string]store.ConfigValue{}
			for i := range layers {
				for k, v := range leavesAt[i][ns] {
					raw[k] = v
				}
			}
			if len(raw) > 0 {
				merged = kvToSettingMap(prefix, raw)
			}
		}
		if len(merged) > 0 {
			out[ns] = merged
		}
	}
	return out, nil
}

// SaveSettingByScope is the legacy (scope, scopeID) form kept for the
// HTTP layer, which still emits scope strings in URL params and JSON.
// New callers should use SaveSetting with explicit (userID, agentID).
func SaveSettingByScope(ctx context.Context, st store.ConfigStore, sc, scopeID, namespace string, data map[string]interface{}) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveSetting(ctx, st, uid, aid, namespace, data)
}

// SaveProviderByScope / SaveChannelByScope mirror the same legacy bridge.
func SaveProviderByScope(ctx context.Context, st store.ConfigStore, sc, scopeID, name string, p config.ProviderConfig) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveProvider(ctx, st, uid, aid, name, p)
}

func SaveChannelByScope(ctx context.Context, st store.ConfigRowWriter, sc, scopeID, channelType, credentialKey string, enabled bool, c config.ChannelConfig) error {
	uid, aid := OwnershipFromScope(sc, scopeID)
	return SaveChannel(ctx, st, uid, aid, channelType, credentialKey, enabled, c)
}

// SandboxNamespace is the settings namespace holding SandboxCfg.
const SandboxNamespace = "sandbox"

// rejectUnreadableAgentScope refuses agent-scope writes to namespaces the
// runtime cannot read, so the mistake surfaces at the write instead of
// silently doing nothing ("I configured it, but the agent says it's not
// there" — the web_search incident class).
//
// Today only one namespace qualifies: sandbox. The executor pool is built
// once from the SYSTEM-scope sandbox row (gateway.buildSystemSandboxPool)
// and handed to every agent, and ResolvedAgent.Sandbox is only ever filled
// from the system/user layers — nothing reads a sandbox row at agent scope.
// Writing one produced an agent that believes sandbox is required while no
// executor exists, which the runtime surfaces as "sandbox required but no
// executor available".
//
// Agent-scope tools.providers / tools.categories used to be in the same
// boat; those are now honored (gateway.toolConfigForAgent overlays the agent
// and per-(user,agent) layers), so they are deliberately absent here.
func rejectUnreadableAgentScope(namespace, userID, agentID string) error {
	if agentID == "" {
		return nil
	}
	if namespace == SandboxNamespace {
		return fmt.Errorf(
			"scope.SaveSetting: sandbox is a system/user-scope setting (the sandbox executor pool is built once from the system row); an agent-scope sandbox row is never read by the runtime — write it at system or user scope instead (namespace=%q agent=%q user=%q)",
			namespace, agentID, userID)
	}
	return nil
}

// SaveSetting upserts a single namespace at the given (user, agent)
// ownership. Pass nil/empty data to delete the row instead of writing
// {}. Pass empty userID/agentID for system-level.
//
// "Delete" is not "switch off": a deleted row lets the outer layers answer,
// while a disabled row is a veto that clears them. SaveSetting is the first
// shape only; SaveSettingState is how a caller writes the second.
func SaveSetting(ctx context.Context, st store.ConfigStore, userID, agentID, namespace string, data map[string]interface{}) error {
	return SaveSettingState(ctx, st, userID, agentID, namespace, data, true)
}

// SaveSettingState is SaveSetting with the row's enabled decision made
// explicit — the settings half of the contract SaveProviderState and
// SaveAgentPluginEnabled already implement.
//
// The readers have always had a veto: settingLayerAt returns enabled=false for
// a disabled row and mergeSettingLayers clears the outer layers for it. What
// was missing was a writer that records it, so `enabled=false` existed only in
// hand-written rows — and once reads were answered by the mirror, a hand
// written veto had no marker to carry its decision and the mirror's stale
// `enabled=true` won instead (TestPanelConfigMatchesRuntimeResolver pinned the
// loss). This is that writer: the veto is dual-written like every other row
// state, so the marker records it and both read orders agree.
//
// enabled=false with data still writes the payload's leaves: switching a
// namespace off is a decision about the row, not an erasure of it (same rule
// as a disabled provider — see dualWriteProviderKV), so editing a disabled row
// back on does not lose its value.
func SaveSettingState(ctx context.Context, st store.ConfigStore, userID, agentID, namespace string, data map[string]interface{}, enabled bool) error {
	if st == nil {
		return errors.New("scope.SaveSettingState: store is required")
	}
	// Single choke point for settings writes, so the configs_kv layout rule
	// cannot be bypassed by a new caller — same shape as ValidateProviderName
	// at SaveProviderState's entry.
	if err := store.ValidateConfigName(store.KindSetting, namespace); err != nil {
		return err
	}
	if err := rejectUnreadableAgentScope(namespace, userID, agentID); err != nil {
		return err
	}
	// One transaction for both tables: configs_kv mirrors the blob, and a
	// half-applied pair is exactly the state the readers then
	// have to defend against (blob authoritative, mirror fallback). Failing
	// loudly here is what keeps "both or neither" true.
	return store.WithConfigTx(ctx, st, func(tx store.ConfigTxStore) error {
		if err := dualWriteSettingKV(ctx, tx, userID, agentID, namespace, data, enabled); err != nil {
			return err
		}
		if len(data) == 0 && enabled {
			// enabled + nothing to write is "this namespace is empty now":
			// find and drop the row if it exists, and drop the marker with it
			// so nothing vouches for a row that is gone. Idempotent:
			// missing-row is a no-op.
			sc, sid := kvScopeFromOwnership(userID, agentID)
			if err := tx.DeleteConfigMirror(ctx, store.KindSetting, sc, sid, namespace); err != nil {
				return err
			}
			if rec, err := tx.GetConfigByName(ctx, store.KindSetting, userID, agentID, namespace); err == nil && rec != nil {
				if err := tx.DeleteConfig(ctx, rec.ID); err != nil {
					return err
				}
			}
			// The row is gone (or never existed). Stamp anyway: a read that
			// cached the old row must rebuild, and "this namespace is empty" is
			// itself a value the reader has to see.
			return stampConfigWrite(ctx, tx)
		}
		rec := &store.ConfigRecord{
			Kind:    store.KindSetting,
			UserID:  userID,
			AgentID: agentID,
			Name:    namespace,
			// The caller's decision travels to the row and, through
			// dualWriteSettingKV's marker, to the mirror. SaveSetting always
			// passes true, so writing a namespace through that path still
			// clears any veto a previous row carried — one of the two ways a
			// disabled row goes away (the other is DeleteConfig).
			Enabled: enabled,
			Data:    data,
		}
		if err := tx.SaveConfig(ctx, rec); err != nil {
			return err
		}
		return stampConfigWrite(ctx, tx)
	})
}

// stampConfigWrite moves the one counter every config read compares against,
// inside the caller's transaction. Content and version commit together, so a
// reader can never see the new version without the new content (C5), and a
// writer cannot save content without moving the version (C1). The counter is
// monotone, so stamping after an idempotent write costs at most one rebuild.
func stampConfigWrite(ctx context.Context, tx store.ConfigTxStore) error {
	if _, err := tx.BumpConfigEpoch(ctx); err != nil {
		return fmt.Errorf("scope: stamp config write: %w", err)
	}
	return nil
}

// PluginEnabledNamespace is the row name that holds a per-agent plugin
// opt-in map: data = {"<pluginID>": true|false}. Missing key / missing row
// means "no override — use the system-wide plugin state".
//
// Rows live under store.KindPluginEnabled, not KindSetting: see the kind's
// doc comment for why sharing the "setting" partition was a bug.
const PluginEnabledNamespace = "plugins.enabled"

// AgentPluginEnabled returns the per-agent plugin opt-in map for agentID
// ((user_id, agent_id) = ("", Y)), or nil when no row exists. Missing keys
// fall through to the system-wide plugin state; callers treat nil as
// "no overrides".
func AgentPluginEnabled(ctx context.Context, st store.ConfigReadStore, agentID string) (map[string]bool, error) {
	if st == nil {
		return nil, errors.New("scope.AgentPluginEnabled: store is required")
	}
	if agentID == "" {
		return nil, nil
	}
	// configs_kv-first: a marker-certified mirror answers the row outright,
	// including the "no overrides" veto. An uncertified row falls through to
	// the blob below — same guard as every other mirrored kind.
	if configsReadAuthority == configsKVFirst {
		kvPrefix := store.ConfigsKVPrefixFor(store.KindPluginEnabled, PluginEnabledNamespace)
		if leaves, err := st.ListConfigValues(ctx, store.KindPluginEnabled, Agent, agentID, kvPrefix); err == nil && len(leaves) > 0 {
			if m, ok := certifiedMirror(ctx, st, store.KindPluginEnabled, Agent, agentID, PluginEnabledNamespace, leaves); ok {
				if m.Enabled != nil && !*m.Enabled {
					return nil, nil
				}
				if data := kvToSettingMap(kvPrefix, leaves); len(data) > 0 {
					return boolMapFromData(data), nil
				}
				return nil, nil
			}
		}
	}
	// Blob first (authoritative), mirror only for blob-less rows — same
	// contract as every other read in this package.
	rec, err := st.GetConfigByName(ctx, store.KindPluginEnabled, "", agentID, PluginEnabledNamespace)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	} else if rec != nil {
		// A disabled row means "no overrides at all" and blocks the mirror.
		if !rec.Enabled {
			return nil, nil
		}
		return boolMapFromData(rec.Data), nil
	}
	kvPrefix := store.ConfigsKVPrefixFor(store.KindPluginEnabled, PluginEnabledNamespace)
	if kvVals, err := st.ListConfigValues(ctx, store.KindPluginEnabled, Agent, agentID, kvPrefix); err == nil && len(kvVals) > 0 {
		// kvToSettingMap restores map-key segments verbatim (kvkeys.dataPaths
		// carries {"plugins","enabled","*"}), so plugin ids come back with
		// their original spelling. The prefix already names the row, so the
		// result is flat: plugin id → bool.
		data := kvToSettingMap(kvPrefix, kvVals)
		if len(data) > 0 {
			return boolMapFromData(data), nil
		}
	}
	return nil, nil
}

// SaveAgentPluginEnabled writes (or, for an empty map, deletes) the
// per-agent plugin opt-in row. The configs_kv mirror is kept in step under
// the same kind.
func SaveAgentPluginEnabled(ctx context.Context, st store.ConfigStore, agentID string, enabled map[string]bool) error {
	if st == nil {
		return errors.New("scope.SaveAgentPluginEnabled: store is required")
	}
	if agentID == "" {
		return errors.New("scope.SaveAgentPluginEnabled: agentID is required")
	}
	data := make(map[string]interface{}, len(enabled))
	for k, v := range enabled {
		data[k] = v
	}
	// Both tables in one transaction — see SaveSetting.
	return store.WithConfigTx(ctx, st, func(tx store.ConfigTxStore) error {
		if err := dualWritePluginEnabledKV(ctx, tx, agentID, data); err != nil {
			return err
		}
		if len(data) == 0 {
			// Idempotent: missing row is a no-op, so "reset" can be replayed.
			if rec, err := tx.GetConfigByName(ctx, store.KindPluginEnabled, "", agentID, PluginEnabledNamespace); err == nil && rec != nil {
				if err := tx.DeleteConfig(ctx, rec.ID); err != nil {
					return err
				}
			}
			return stampConfigWrite(ctx, tx)
		}
		if err := tx.SaveConfig(ctx, &store.ConfigRecord{
			Kind:    store.KindPluginEnabled,
			UserID:  "",
			AgentID: agentID,
			Name:    PluginEnabledNamespace,
			Enabled: true,
			Data:    data,
		}); err != nil {
			return err
		}
		return stampConfigWrite(ctx, tx)
	})
}

// boolMapFromData maps {"<id>": true|false, ...} onto map[string]bool,
// dropping values that aren't booleans (a hand-edited row can't crash a
// caller).
func boolMapFromData(data map[string]interface{}) map[string]bool {
	out := make(map[string]bool, len(data))
	for k, v := range data {
		if b, ok := v.(bool); ok {
			out[k] = b
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dualWritePluginEnabledKV mirrors the opt-in map into configs_kv. The keys
// below the row name are plugin ids (data keys), so the shared flattening
// rule keeps them verbatim.
func dualWritePluginEnabledKV(ctx context.Context, st store.KVStore, agentID string, data map[string]interface{}) error {
	kvPrefix := store.ConfigsKVPrefixFor(store.KindPluginEnabled, PluginEnabledNamespace)
	if err := st.DeleteConfigPrefix(ctx, store.KindPluginEnabled, Agent, agentID, kvPrefix); err != nil {
		return fmt.Errorf("scope: clear configs_kv prefix %q: %w", kvPrefix, err)
	}
	if len(data) == 0 {
		return st.DeleteConfigMirror(ctx, store.KindPluginEnabled, Agent, agentID, PluginEnabledNamespace)
	}
	flat := map[string]store.ConfigValue{}
	flattenJSONToKV(kvPrefix, data, flat)
	for name, value := range flat {
		if err := st.SetConfigValue(ctx, store.KindPluginEnabled, Agent, agentID, name, value); err != nil {
			return fmt.Errorf("scope: mirror plugin opt-in %q: %w", name, err)
		}
	}
	return saveMirror(ctx, st, store.KindPluginEnabled, Agent, agentID, PluginEnabledNamespace, kvPrefix, true, flat)
}

// providerNamePattern is the charset a provider name may use.
//
// The name is not just a label: it is the configs_kv key prefix
// ("<name>.<field>") and the left half of a "provider/model" reference. A
// '.' inside it is ambiguous — the mirror reader can only split at the first
// dot, so "my.provider.api_key" is read as provider "my" with field
// "provider.api_key", which maps to an empty ProviderConfig; a '/' would
// collide with the model reference separator. Both used to be accepted
// (handleCreateProvider checked only for non-empty) and produced a provider
// that reads back empty whenever the blob row is absent.
var providerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateProviderName rejects names the storage layout cannot represent
// faithfully. Exported so the HTTP layer can answer 400 instead of letting
// the generic 500 path carry it, and so the CLI reports the same reason.
func ValidateProviderName(name string) error {
	if name == "" {
		return errors.New("provider name is required")
	}
	if !providerNamePattern.MatchString(name) {
		return fmt.Errorf("invalid provider name %q: use letters, digits, '_' or '-' (%d chars max, starting with a letter or digit); '.' and '/' would be ambiguous in the configs_kv key layout and in provider/model references", name, 64)
	}
	return nil
}

// SaveProvider upserts a kind="provider" row at the given (user, agent)
// ownership, marking it enabled — "create/update this provider and use it".
// Callers that rewrite an existing row must use SaveProviderState and pass
// that row's flag, or editing a disabled provider silently re-enables it.
func SaveProvider(ctx context.Context, st store.ConfigStore, userID, agentID, name string, p config.ProviderConfig) error {
	return SaveProviderState(ctx, st, userID, agentID, name, p, true)
}

// SaveProviderState is SaveProvider with an explicit enabled flag. The flag
// is the write half of the enabled contract (see the package doc): false
// means this scope switches the provider off, which also erases the outer
// entries of the same name on read.
func SaveProviderState(ctx context.Context, st store.ConfigStore, userID, agentID, name string, p config.ProviderConfig, enabled bool) error {
	// Single choke point: HTTP create/update, the admin API, onboarding and
	// the CLI all land here, so the rule cannot be bypassed by a new caller.
	if err := ValidateProviderName(name); err != nil {
		return err
	}
	// Both tables in one transaction, and the stamp commits with them — see
	// SaveSetting.
	return store.WithConfigTx(ctx, st, func(tx store.ConfigTxStore) error {
		if err := dualWriteProviderKV(ctx, tx, userID, agentID, name, p, enabled); err != nil {
			return err
		}
		rec := &store.ConfigRecord{
			Kind:    store.KindProvider,
			UserID:  userID,
			AgentID: agentID,
			Name:    name,
			Enabled: enabled,
			Data:    providerToData(p),
		}
		if err := tx.SaveConfig(ctx, rec); err != nil {
			return err
		}
		return stampConfigWrite(ctx, tx)
	})
}

// DeleteProvider removes a provider row and its configs_kv mirror in one
// transaction, and stamps the counter with them. The delete used to be two
// statements from the handler (mirror, then row) with no transaction at all, so
// a reader could see the row gone and the mirror still present — or, worse for
// the read cache, neither change announced.
func DeleteProvider(ctx context.Context, st store.ConfigStore, rec *store.ConfigRecord) error {
	if st == nil {
		return errors.New("scope.DeleteProvider: store is required")
	}
	if rec == nil || rec.ID == "" {
		return errors.New("scope.DeleteProvider: record is required")
	}
	return store.WithConfigTx(ctx, st, func(tx store.ConfigTxStore) error {
		// Checked inline rather than through DualDeleteProviderKV, which
		// swallows its errors: inside a transaction a swallowed failure commits
		// the row delete and leaves the mirror behind.
		sc, sid := kvScopeFromOwnership(rec.UserID, rec.AgentID)
		if err := tx.DeleteConfigPrefix(ctx, store.KindProvider, sc, sid,
			store.ConfigsKVPrefixFor(store.KindProvider, rec.Name)); err != nil {
			return fmt.Errorf("scope.DeleteProvider: drop mirror values: %w", err)
		}
		if err := tx.DeleteConfigMirror(ctx, store.KindProvider, sc, sid, rec.Name); err != nil {
			return fmt.Errorf("scope.DeleteProvider: drop mirror marker: %w", err)
		}
		if err := tx.DeleteConfig(ctx, rec.ID); err != nil {
			return err
		}
		return stampConfigWrite(ctx, tx)
	})
}

// SaveChannel upserts a kind="channel" row at the given (user, agent)
// ownership. credentialKey is the stable lookup handle for inbound
// dispatch (bot token tail, app id).
func SaveChannel(ctx context.Context, st store.ConfigRowWriter, userID, agentID, channelType, credentialKey string, enabled bool, c config.ChannelConfig) error {
	rec := &store.ConfigRecord{
		Kind:          store.KindChannel,
		UserID:        userID,
		AgentID:       agentID,
		Name:          channelType,
		Enabled:       enabled,
		CredentialKey: credentialKey,
		Data:          channelToData(c),
	}
	return st.SaveConfig(ctx, rec)
}

func providerToConfig(r store.ConfigRecord) config.ProviderConfig {
	pc := config.ProviderConfig{}
	if blob, err := json.Marshal(r.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &pc)
	}
	return pc
}

func providerToData(p config.ProviderConfig) map[string]interface{} {
	return store.ValueToMap(p)
}

func channelToConfig(r store.ConfigRecord) config.ChannelConfig {
	cc := config.ChannelConfig{Enabled: r.Enabled}
	if blob, err := json.Marshal(r.Data); err == nil && len(blob) > 0 {
		_ = json.Unmarshal(blob, &cc)
	}
	cc.Enabled = r.Enabled
	return cc
}

func channelToData(c config.ChannelConfig) map[string]interface{} {
	m := store.ValueToMap(c)
	delete(m, "enabled") // enabled lives on the row column, not in data
	return m
}

// ---------------------------------------------------------------------------
// configs_kv read/write helpers
// ---------------------------------------------------------------------------

// kvScopeFromOwnership converts (userID, agentID) into the configs_kv
// (scope, scope_id) pair. Mirrors ScopeFromOwnership so the per-(user,
// agent) layer survives the configs_kv dual-write — without it, a key
// bound at per-(user, agent) scope would be flattened to the user layer
// and leak across the user's agents.
func kvScopeFromOwnership(userID, agentID string) (scope, scopeID string) {
	return store.KVScopeFromOwnership(userID, agentID)
}

// GetValues reads all values matching a prefix with scope merge.
// System values are read first, then user overrides, then agent overrides.
// For each name key, the innermost scope wins.
//
// The values stay tagged (store.ConfigValue) rather than being pre-decoded:
// the caller knows which keys are data keys and which are struct fields, and
// Decode is what turns a tag into a Go value.
func GetValues(ctx context.Context, st store.ConfigReader, kind, prefix, userID, agentID string) (map[string]store.ConfigValue, error) {
	if st == nil {
		return nil, errors.New("scope.GetValues: store is required")
	}
	out := map[string]store.ConfigValue{}
	merge := func(sc, sid string) error {
		m, err := st.ListConfigValues(ctx, kind, sc, sid, prefix)
		if err != nil {
			return err
		}
		for k, v := range m {
			out[k] = v
		}
		return nil
	}
	if err := merge(System, ""); err != nil {
		return nil, err
	}
	if userID != "" {
		if err := merge(User, userID); err != nil {
			return nil, err
		}
	}
	if agentID != "" {
		if err := merge(Agent, agentID); err != nil {
			return nil, err
		}
	}
	if userID != "" && agentID != "" {
		if err := merge(UserAgent, store.KVUserAgentScopeID(userID, agentID)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// flattenJSONToKV flattens a map into dotted-key → store.ConfigValue pairs,
// used for dual-writing to configs_kv. Same logic as store.flattenJSON.
//
// Each leaf carries the tag naming its JSON type (store.EncodeConfigValue).
// The write side is the only place that still knows the type, so it is the
// only place that can record it — the earlier version stringified every
// scalar and left the read side to guess it back.
//
// Map keys (data segments) are written verbatim, struct fields snake_case —
// see kvkeys. Without the data-key exception an ALL_CAPS skill env var
// (REPLICATE_API_TOKEN) was lowercased to replicate_api_token and could never
// be restored from configs_kv.
//
// An empty object stops here rather than descending: descending yields zero
// leaves, so `{"config":{}}` would flatten to nothing and a mirror-first read
// would answer "no such key". JSONObjectOf owns that rule for both flatteners.
func flattenJSONToKV(prefix string, data map[string]interface{}, out map[string]store.ConfigValue) {
	prefixPath := kvkeys.Path(prefix)
	for k, v := range data {
		seg := kvkeys.StoredSegment(prefixPath, k)
		fullKey := prefix + seg
		// Descend by structure, not by concrete type: a nested
		// map[string]string, a map[string]SomeCfg or a plain struct is a JSON
		// object too. Asserting `v.(map[string]interface{})` let those become
		// one object-valued leaf — the collapse that shows up as a reconcile
		// gap (`tools.providers.searxng` where the mirror says
		// `tools.providers.searxng.endpoint`). The configs_kv side
		// (store.flattenJSON) uses the same check, so the two agree on the
		// leaf boundary.
		if nested, ok := store.JSONObjectOf(v); ok {
			flattenJSONToKV(fullKey+".", nested, out)
			continue
		}
		// A nil leaf gets a tagged null row instead of being skipped: with
		// a tag, {"a":null} and {} are no longer the same mirror.
		// An empty object reaches this line for the same reason (see
		// JSONObjectOf): it is a leaf, and {"config":{}} must not flatten to
		// the same rows as a map with no "config" key.
		out[fullKey] = store.EncodeConfigValue(v)
	}
}

// dualWriteSettingKV writes the flattened KV pairs to configs_kv alongside
// the legacy configs table write. Called by SaveSettingState to keep both
// tables in sync during migration.
//
// The marker is written for every row state, including the empty one: the
// marker is the row registry that carries the enabled decision, so a disabled
// row with no payload is a row the mirror must still be able to answer for.
// Only a row that is going away has its marker removed, and the caller does
// that (SaveSettingState's delete branch) because "the row is gone" is the
// caller's decision, not the flattener's.
func dualWriteSettingKV(ctx context.Context, st store.KVStore, userID, agentID, namespace string, data map[string]interface{}, enabled bool) error {
	sc, sid := kvScopeFromOwnership(userID, agentID)
	kvPrefix := store.ConfigsKVPrefixFor(store.KindSetting, namespace)
	// The prefix delete runs for every write: re-writing a namespace has to
	// clear the leaves it used to have.
	if err := st.DeleteConfigPrefix(ctx, store.KindSetting, sc, sid, kvPrefix); err != nil {
		return fmt.Errorf("scope: clear configs_kv prefix %q: %w", kvPrefix, err)
	}
	flat := map[string]store.ConfigValue{}
	flattenJSONToKV(kvPrefix, data, flat)
	for name, value := range flat {
		if err := st.SetConfigValue(ctx, store.KindSetting, sc, sid, name, value); err != nil {
			return fmt.Errorf("scope: mirror setting %q: %w", name, err)
		}
	}
	// The marker records the caller's enabled decision so a mirror-first
	// reader can answer the veto question without the blob — the same contract
	// dualWriteProviderKV implements.
	return saveMirror(ctx, st, store.KindSetting, sc, sid, namespace, kvPrefix, enabled, flat)
}

// saveMirror records the completeness marker for a mirror that was just
// written. flat is the exact leaf set that went into configs_kv, so the marker
// fingerprints what is on disk rather than what was intended; enabled is the
// decision the paired blob row carries, so the marker records the row's whole
// read state (leaves + on/off) and not just its payload.
func saveMirror(ctx context.Context, st store.ConfigMirrorStore, kind, sc, sid, name, prefix string, enabled bool, flat map[string]store.ConfigValue) error {
	if err := st.SaveConfigMirror(ctx, kind, sc, sid, name, store.NewConfigMirror(prefix, enabled, flat)); err != nil {
		return fmt.Errorf("scope: mirror marker for %q: %w", name, err)
	}
	return nil
}

// dualWriteProviderKV writes the flattened provider config to configs_kv.
//
// The leaves are written whether or not the row is enabled: disabling a
// provider is a row decision (it erases the outer entries, it does not erase
// the payload), and the paired blob write records that decision. The marker
// therefore has to carry it too — see ConfigMirror.Enabled.
func dualWriteProviderKV(ctx context.Context, st store.KVStore, userID, agentID, providerName string, p config.ProviderConfig, enabled bool) error {
	sc, sid := kvScopeFromOwnership(userID, agentID)
	kvPrefix := store.ConfigsKVPrefixFor(store.KindProvider, providerName)
	data := providerToData(p)
	flat := map[string]store.ConfigValue{}
	flattenJSONToKV(kvPrefix, data, flat)
	if err := st.DeleteConfigPrefix(ctx, store.KindProvider, sc, sid, kvPrefix); err != nil {
		return fmt.Errorf("scope: clear configs_kv prefix %q: %w", kvPrefix, err)
	}
	for name, value := range flat {
		if err := st.SetConfigValue(ctx, store.KindProvider, sc, sid, name, value); err != nil {
			return fmt.Errorf("scope: mirror provider %q: %w", name, err)
		}
	}
	return saveMirror(ctx, st, store.KindProvider, sc, sid, providerName, kvPrefix, enabled, flat)
}

// DualDeleteProviderKV removes all KV entries for a provider.
func DualDeleteProviderKV(ctx context.Context, st store.KVStore, userID, agentID, providerName string) {
	sc, sid := kvScopeFromOwnership(userID, agentID)
	_ = st.DeleteConfigPrefix(ctx, store.KindProvider, sc, sid, store.ConfigsKVPrefixFor(store.KindProvider, providerName))
	_ = st.DeleteConfigMirror(ctx, store.KindProvider, sc, sid, providerName)
}
