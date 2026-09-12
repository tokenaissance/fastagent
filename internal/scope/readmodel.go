/**
 * [INPUT]: gets a store.ConfigReader from the caller; uses store.MirrorPrefixFor
 *   and store.ListConfigValues for the mirror half. Imports config (provider
 *   shapes) and kvkeys indirectly through scope's own projection helpers.
 * [OUTPUT]: the per-scope read models adapters resolve through instead of
 *   naming a table: SettingAt / SettingNamesAt / ProviderStateAt /
 *   ProvidersAt / RowsAt. ExactSetting is re-expressed on the same single
 *   resolution rule.
 * [POS]: scope package's read face. scope.go owns the layer-merge resolvers
 *   (Setting / SettingInto / BatchSettings / Providers); this file owns the
 *   single-scope ones. Together they are the only place in the codebase that
 *   knows which table a settings/provider read comes from, so the migration
 *   phase that flips the mirror to authoritative is a change here and not in
 *   every caller.
 * [PROTOCOL]: On change, update this header, then check
 *   docs/configs-kv-scope-adaptation.md 现状 · 读路径.
 */

package scope

import (
	"context"
	"errors"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// settingAtRaw is the one resolution rule for a single setting namespace at a
// single (userID, agentID) scope, and every single-scope settings reader is a
// projection of it. Blob first — exact JSON types, the complete key set, and
// the enabled veto — with the configs_kv mirror consulted only when no blob row
// exists at all. The blob is authoritative only while the migration is in
// flight; when it flips, this function is the one place the order changes (see
// docs/configs-kv-scope-adaptation.md).
//
// A missing row, a disabled row and an empty row are all "nothing here" and
// return a nil map, which is what lets the callers keep their old behaviour
// (a disabled row is this layer's decision that the namespace has no value,
// and it must not be undone by the mirror).
func settingAtRaw(ctx context.Context, st store.ConfigReader, namespace, userID, agentID string) (map[string]interface{}, error) {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, agentID, namespace)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	} else if rec != nil {
		if !rec.Enabled || len(rec.Data) == 0 {
			return nil, nil
		}
		return rec.Data, nil
	}
	// No blob row: serve the mirror (rows written straight into configs_kv have
	// no blob counterpart). A mirror that fails to load is not fatal — the blob
	// is the authority and the fallback is best-effort, so the read degrades to
	// "empty" rather than failing the caller.
	kvPrefix := kvPrefixForNamespace(namespace)
	sc, sid := kvScopeFromOwnership(userID, agentID)
	if kvVals, err := st.ListConfigValues(ctx, store.KindSetting, sc, sid, kvPrefix); err == nil && len(kvVals) > 0 {
		return kvToSettingMap(kvPrefix, kvVals), nil
	}
	return nil, nil
}

// SettingAt returns the effective data for one setting namespace at exactly one
// (userID, agentID) scope — no layer merge. It is the map form of ExactSetting,
// and the entry point for adapters that want "what is this namespace here?"
// without knowing whether the answer currently lives in the blob or the mirror.
//
// The result is a shallow copy, so a caller doing read-modify-write (the CLI
// patches one key and saves the result) cannot write back through the map the
// store just handed out. Nested values are shared, which is what the callers
// that mutate a single top-level key need.
func SettingAt(ctx context.Context, st store.ConfigReader, namespace, userID, agentID string) (map[string]interface{}, error) {
	if st == nil {
		return nil, errors.New("scope.SettingAt: store is required")
	}
	m, err := settingAtRaw(ctx, st, namespace, userID, agentID)
	if err != nil {
		return nil, err
	}
	return cloneTopLevel(m), nil
}

// SettingNamesAt enumerates the settings namespaces that have a row at exactly
// one (userID, agentID) scope, keyed by namespace.
//
// This is the row-enumeration view, not a resolved one: it answers "which
// namespaces does this scope have?", which is what a config dump and a
// namespace-keyed copy need, and it deliberately does not walk the mirror for
// namespaces that exist only there — recovering a namespace name from a
// configs_kv prefix needs an inverse of MirrorPrefixFor, which does not exist
// (the layout has a rename in it). The same call the codebase already made for
// Channels and Timezone: reads that are not the runtime's merged view do not get
// a fallback bolted on (see docs/configs-kv-scope-adaptation.md 残留风险).
//
// A disabled row is skipped: it carries no payload, and listing it would report
// a namespace the runtime resolves to nothing.
func SettingNamesAt(ctx context.Context, st store.ConfigReader, userID, agentID string) (map[string]map[string]interface{}, error) {
	if st == nil {
		return nil, errors.New("scope.SettingNamesAt: store is required")
	}
	rows, err := st.ListConfigs(ctx, store.KindSetting, userID, agentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]interface{}, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		out[row.Name] = cloneTopLevel(row.Data)
	}
	return out, nil
}

// ProvidersAt returns the providers that exist at exactly one (userID, agentID)
// scope — the agent's own rows, the user's own rows, or the system's, without
// any other layer merged in. It is the single-scope primitive behind
// AgentScopeProviders / UserScopeProviders, defined once so those two cannot
// drift apart or from this one.
//
// Blob first, mirror only for names with no blob row — per name, not per chain,
// so a provider that exists only in the mirror stays visible next to
// blob-backed siblings (see Providers). A disabled row is the scope's decision
// that the name is not here: it is absent from the result, and callers that
// overlay the result on an already-merged view get the erase-the-outer-entry
// behaviour for free, because "not present" is exactly what they overwrite with.
func ProvidersAt(ctx context.Context, st store.ConfigReader, userID, agentID string) (map[string]config.ProviderConfig, error) {
	if st == nil {
		return nil, errors.New("scope.ProvidersAt: store is required")
	}
	rows, err := st.ListConfigs(ctx, store.KindProvider, userID, agentID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]config.ProviderConfig, len(rows))
	inBlob := make(map[string]bool, len(rows))
	for _, r := range rows {
		inBlob[r.Name] = true
		if !r.Enabled {
			delete(out, r.Name)
			continue
		}
		out[r.Name] = providerToConfig(r)
	}
	sc, sid := kvScopeFromOwnership(userID, agentID)
	if kvVals, err := st.ListConfigValues(ctx, store.KindProvider, sc, sid, ""); err == nil && len(kvVals) > 0 {
		for name, pc := range kvValsToProviders(kvVals) {
			if !inBlob[name] {
				out[name] = pc
			}
		}
	}
	return out, nil
}

// ProviderStateAt returns one provider at exactly one (userID, agentID) scope:
// its payload, and this scope's decision about it. present reports that the
// scope has the provider at all (a row, or a leaf set that exists only in the
// mirror); enabled reports that the scope offers it, i.e. present and not
// disabled.
//
// The two are separate because callers need different halves. A reader that
// answers "which provider does this agent use" wants enabled — that is the same
// rule Providers and AgentScopeProviders apply. A read-modify-write that is
// about to save the row back (the CLI setting one field) wants the payload,
// which still exists while the row is switched off; collapsing the two would
// make editing a disabled provider silently reset it to the preset defaults.
func ProviderStateAt(ctx context.Context, st store.ConfigReader, name, userID, agentID string) (pc config.ProviderConfig, present, enabled bool, err error) {
	if st == nil {
		return config.ProviderConfig{}, false, false, errors.New("scope.ProviderStateAt: store is required")
	}
	rec, err := st.GetConfigByName(ctx, store.KindProvider, userID, agentID, name)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return config.ProviderConfig{}, false, false, err
		}
	} else if rec != nil {
		return providerToConfig(*rec), true, rec.Enabled, nil
	}
	// No blob row: the provider exists only in the mirror, where a row's mere
	// presence is the decision (there is no enabled column to contradict it).
	kvPrefix := store.MirrorPrefixFor(store.KindProvider, name)
	sc, sid := kvScopeFromOwnership(userID, agentID)
	if kvVals, err := st.ListConfigValues(ctx, store.KindProvider, sc, sid, kvPrefix); err == nil && len(kvVals) > 0 {
		if pc, ok := kvValsToProviders(kvVals)[name]; ok {
			return pc, true, true, nil
		}
	}
	return config.ProviderConfig{}, false, false, nil
}

// RowsAt returns the raw configs rows of one (userID, agentID) scope for one
// kind — the row-level view a CRUD editor works from, deliberately unmerged and
// without a mirror fallback. The editor lists "which rows does this scope have"
// and addresses them by id / updatedAt / credentialKey, which are properties of
// a blob row; a name that exists only in the mirror has none of them, so
// serving it here would hand the caller an entry it cannot address. The merged
// view is Providers / Setting / BatchSettings.
//
// It exists so that "which table" stays a decision of this package: an adapter
// asks for the rows of a scope, and the fact that they currently live in the
// blob is not its business.
func RowsAt(ctx context.Context, st store.ConfigReader, kind, userID, agentID string) ([]store.ConfigRecord, error) {
	if st == nil {
		return nil, errors.New("scope.RowsAt: store is required")
	}
	return st.ListConfigs(ctx, kind, userID, agentID)
}

// AgentScopeRows is the batched sibling of RowsAt: one settings namespace at
// agent scope for many agents, in one query — the shape the per-agent overlay
// paths need (tools.categories / tools.providers / skills.entries /
// agents.defaults, each read for every agent a user owns).
//
// It returns rows rather than a merged map because the callers select ownership
// layers themselves: the tool overlay needs the agent's own row (user_id="")
// *and* the caller's per-(caller, agent) row out of the same batch, and applies
// them in an order the query cannot express. Only enabled rows come back, which
// is the same rule the resolvers apply to a disabled row (it contributes
// nothing) — so a caller applying these in layer order lands on the same answer
// as Setting.
//
// The batching is why this is not N calls to SettingAt: loadUserSpace runs it
// for every agent of a user. That is also why the mirror fallback is not here
// yet — the blob batch is the whole point of the call shape, and these four
// namespaces are always written through the dual-write, so a page of agents
// with no blob row at all does not occur in practice. When the flip lands this
// is the function that grows the fallback, not its callers.
func AgentScopeRows(ctx context.Context, st store.ConfigReader, namespace string, agentIDs []string) ([]store.ConfigRecord, error) {
	if st == nil {
		return nil, errors.New("scope.AgentScopeRows: store is required")
	}
	return st.BatchGetConfigsByAgentIDs(ctx, store.KindSetting, namespace, agentIDs)
}

// cloneTopLevel copies a map's own keys so a caller mutating the result cannot
// write through to the row the store scanned. Nested values are shared — the
// read-modify-write callers patch a top-level key, and a deep copy would only
// cost cycles.
func cloneTopLevel(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
