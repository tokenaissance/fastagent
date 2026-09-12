/**
 * [INPUT]: the settable/providable resolvers get a store.ConfigReadStore from
 *   the caller (ConfigReader plus the mirror marker, MirrorReader); the
 *   row-enumeration ones (SettingNamesAt / RowsAt / AgentScopeRows) still take
 *   store.ConfigReader. Uses store.ConfigsKVPrefixFor, store.ListConfigValues and
 *   store.GetConfigMirror. Imports config (provider shapes) and kvkeys
 *   indirectly through scope's own mirror helpers.
 * [OUTPUT]: the per-scope read models adapters resolve through instead of
 *   naming a table: SettingAt / SettingNamesAt / ProviderStateAt /
 *   ProvidersAt / RowsAt. ExactSetting is re-expressed on the same single
 *   resolution rule. The table they pick is configsReadAuthority, and the
 *   mirror-first branch certifies each row through certifiedMirror (one name)
 *   or certifiedMirrorIn (a scope's markers read once via ListConfigMirrors).
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
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// readAuthority selects which table a configs read resolves through, and it is
// the single lever the configs -> configs_kv migration pulls to flip authority
// (docs/configs-kv-scope-adaptation.md 目标形态与迁移阶段).
//
//   - blobFirst is the migration-period order: the legacy blob is
//     authoritative, and the mirror answers only rows the blob does not have.
//   - configsKVFirst is the target: the mirror answers a row whose completeness
//     marker certifies it, and the blob is the fallback for rows the mirror
//     cannot certify.
//
// The per-row certification is not optional in configsKVFirst: trusting the
// mirror without it is the web_search outage — a partial mirror served as
// if it were the whole namespace.
//
// It is a package variable, not a per-call argument, because the decision is
// process-global and belongs to this one layer: every read site above funnels
// through this file, so the flip is this one name and never reaches into a
// caller. The value stays blobFirst until the reconcile gate is green
// (fastagent configs reconcile-mirror --strict), which is the last acceptance
// before the flip.
type readAuthority int

const (
	blobFirst readAuthority = iota
	configsKVFirst
)

var configsReadAuthority = blobFirst

// certifiedMirror returns the row's completeness marker when it certifies
// exactly these leaves, judged against the marker alone.
//
// ok=false covers every reason a mirror-first reader must not serve the mirror
// for this row: no marker at all (a legacy row, or one only ever written to
// configs_kv), a marker that predates the enabled column, leaves that drifted
// after the marker was written (fingerprint or count mismatch), or a marker
// read error — the caller falls back to the blob in every case. It is the
// read-path half of store.VerifyConfigMirror: the reconciler compares the
// marker against the blob, a reader compares it against the leaves it just
// loaded and never needs the blob to do so.
func certifiedMirror(ctx context.Context, st store.ConfigReadStore, kind, sc, sid, name string, leaves map[string]store.ConfigValue) (store.ConfigMirror, bool) {
	m, ok, err := st.GetConfigMirror(ctx, kind, sc, sid, name)
	if err != nil || !ok {
		return store.ConfigMirror{}, false
	}
	if !store.MirrorSelfConsistent(m, leaves) {
		return store.ConfigMirror{}, false
	}
	return m, true
}

// certifiedMirrorIn is certifiedMirror against a marker map that was already
// read for the whole scope (ListConfigMirrors), so a reader certifying many
// names at one scope does not issue one marker query per name. The rule is the
// same one store.MirrorSelfConsistent applies; only the lookup differs.
func certifiedMirrorIn(markers map[string]store.ConfigMirror, name string, leaves map[string]store.ConfigValue) (store.ConfigMirror, bool) {
	m, ok := markers[name]
	if !ok || !store.MirrorSelfConsistent(m, leaves) {
		return store.ConfigMirror{}, false
	}
	return m, true
}

// settingAtRaw is the one resolution rule for a single setting namespace at a
// single (userID, agentID) scope, and every single-scope settings reader goes
// through it. Which table answers is configsReadAuthority's decision;
// both orders funnel through this function, which is the one place the flip
// changes (see docs/configs-kv-scope-adaptation.md).
//
// A missing row, a disabled row and an empty row are all "nothing here" and
// return a nil map, which is what lets the callers keep their old behaviour
// (a disabled row is this layer's decision that the namespace has no value,
// and it must not be undone by the mirror).
func settingAtRaw(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, error) {
	if configsReadAuthority == configsKVFirst {
		if m, served, err := settingFromCertifiedMirror(ctx, st, namespace, userID, agentID); err != nil {
			return nil, err
		} else if served {
			return m, nil
		}
	}
	return settingFromBlob(ctx, st, namespace, userID, agentID)
}

// settingFromCertifiedMirror answers one setting namespace from the configs_kv
// mirror when the row's marker certifies the leaves just read. served=false
// means the mirror cannot answer for this row — no marker, drifted/legacy
// leaves, or a read error — and the caller falls back to the blob. served=true
// means the mirror is the answer, including the case where it answers "nothing"
// because the row is switched off.
func settingFromCertifiedMirror(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, bool, error) {
	data, enabled, ok := certifiedSettingLayer(ctx, st, namespace, userID, agentID)
	if !ok {
		return nil, false, nil
	}
	if !enabled {
		// The row's veto: nothing here, and the blob must not resurrect it.
		return nil, true, nil
	}
	return data, true, nil
}

// certifiedSettingLayer is the layer-level form of settingFromCertifiedMirror:
// it returns one layer's payload and enabled decision from the mirror iff the
// row's marker certifies the leaves that were read. ok=false is "this layer's
// mirror does not certify the row", which the merged resolvers read as "fall
// back to the blob row for this layer". The payload of a disabled row is still
// returned (the veto is in the bool), so a merged walk can tell "off" from
// "absent".
func certifiedSettingLayer(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, bool, bool) {
	sc, sid := kvScopeFromOwnership(userID, agentID)
	m, ok, err := st.GetConfigMirror(ctx, store.KindSetting, sc, sid, namespace)
	if err != nil || !ok {
		return nil, false, false
	}
	kvPrefix := kvPrefixForNamespace(namespace)
	leaves, err := st.ListConfigValues(ctx, store.KindSetting, sc, sid, kvPrefix)
	if err != nil {
		return nil, false, false
	}
	if !store.MirrorSelfConsistent(m, leaves) {
		return nil, false, false
	}
	return kvToSettingMap(kvPrefix, leaves), m.Enabled != nil && *m.Enabled, true
}

// settingFromBlob is the migration-period order: the legacy blob is
// authoritative — exact JSON types, the complete key set, and the enabled veto
// — with the configs_kv mirror consulted only when no blob row exists at all.
func settingFromBlob(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, error) {
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

// settingLayerAt resolves one setting namespace as it is owned by exactly one
// layer — the per-layer step the merged resolvers (Setting / BatchSettings)
// walk four times. It honors configsReadAuthority: under configsKVFirst a
// marker-certified mirror answers the layer and the blob row is not
// consulted, otherwise the blob row does (and, when there is none, the layer
// owns nothing here — the merged walk's last-resort fallback is where a
// blob-less, marker-less row is served). present=false is "this layer owns
// nothing", which is distinct from a present row that is switched off.
func settingLayerAt(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (data map[string]interface{}, enabled, present bool, err error) {
	if configsReadAuthority == configsKVFirst {
		if d, e, ok := certifiedSettingLayer(ctx, st, namespace, userID, agentID); ok {
			return d, e, true, nil
		}
	}
	rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, agentID, namespace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, false, nil
		}
		return nil, false, false, err
	}
	if rec == nil {
		return nil, false, false, nil
	}
	return rec.Data, rec.Enabled, true, nil
}

// settingLayerIDs lists the ownership layers a settings read walks, outer→inner
// — the four layers Setting, BatchSettings and Timezone all agree on. Shared so
// the merged resolvers cannot drift on which layers exist or their order.
func settingLayerIDs(userID, agentID string) [][2]string {
	layers := [][2]string{{"", ""}}
	if userID != "" {
		layers = append(layers, [2]string{userID, ""})
	}
	if agentID != "" {
		layers = append(layers, [2]string{"", agentID})
	}
	if userID != "" && agentID != "" {
		layers = append(layers, [2]string{userID, agentID})
	}
	return layers
}

// settingLayerView is one layer's answer for a namespace: its payload, its
// enabled decision, and whether the layer owns the namespace at all (present).
// present=false is not the same as enabled=false: the latter is a row that
// vetoes, the former is a layer with nothing to say.
type settingLayerView struct {
	data    map[string]interface{}
	enabled bool
	present bool
}

// mergeSettingLayers applies the Setting merge rule over already-resolved
// layers, in order: a disabled row vetoes (clears what the outer layers
// contributed) and an enabled row field-merges its top-level keys. saw reports
// whether any layer owned the namespace. It is the one definition of the merge,
// shared by Setting (which resolves each layer on demand) and the batched
// resolver (which pre-fetches the layers), so the two cannot disagree.
func mergeSettingLayers(views []settingLayerView) (map[string]interface{}, bool) {
	out := map[string]interface{}{}
	saw := false
	for _, v := range views {
		if !v.present {
			continue
		}
		saw = true
		if !v.enabled {
			out = map[string]interface{}{}
			continue
		}
		for k, val := range v.data {
			out[k] = val
		}
	}
	return out, saw
}

// leavesUnderPrefix selects the leaves of one configs_kv name prefix out of a
// whole scope's leaves (ListConfigValues with an empty prefix). The prefix
// mapping is injective within a kind (see store.ConfigsKVPrefixFor), so a prefix
// match is exactly that row's leaves and never a sibling's.
func leavesUnderPrefix(leaves map[string]store.ConfigValue, prefix string) map[string]store.ConfigValue {
	var out map[string]store.ConfigValue
	for k, v := range leaves {
		if strings.HasPrefix(k, prefix) {
			if out == nil {
				out = map[string]store.ConfigValue{}
			}
			out[k] = v
		}
	}
	return out
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
func SettingAt(ctx context.Context, st store.ConfigReadStore, namespace, userID, agentID string) (map[string]interface{}, error) {
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
// configs_kv prefix needs an inverse of ConfigsKVPrefixFor, which does not exist
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
func ProvidersAt(ctx context.Context, st store.ConfigReadStore, userID, agentID string) (map[string]config.ProviderConfig, error) {
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
	kvVals, err := st.ListConfigValues(ctx, store.KindProvider, sc, sid, "")
	if err != nil || len(kvVals) == 0 {
		return out, nil
	}
	// One marker query for the whole scope, not one per name.
	var markers map[string]store.ConfigMirror
	if configsReadAuthority == configsKVFirst {
		markers, err = st.ListConfigMirrors(ctx, store.KindProvider, sc, sid)
		if err != nil {
			markers = nil
		}
	}
	for name, leaves := range groupProviderLeaves(kvVals) {
		pc, ok := kvValsToProviders(leaves)[name]
		if !ok {
			continue
		}
		if configsReadAuthority == configsKVFirst {
			if m, certified := certifiedMirrorIn(markers, name, leaves); certified {
				// Certified: the mirror decides this name outright — payload
				// and veto. A mirror-only provider has no row to be switched
				// off, but a marker can still record the decision.
				if m.Enabled != nil && !*m.Enabled {
					delete(out, name)
				} else {
					out[name] = pc
				}
				continue
			}
		}
		// Not certified (or blobFirst): a name with no blob row is served from
		// the mirror, as before; a name the blob already decided keeps the blob.
		if !inBlob[name] {
			out[name] = pc
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
func ProviderStateAt(ctx context.Context, st store.ConfigReadStore, name, userID, agentID string) (pc config.ProviderConfig, present, enabled bool, err error) {
	if st == nil {
		return config.ProviderConfig{}, false, false, errors.New("scope.ProviderStateAt: store is required")
	}
	kvPrefix := store.ConfigsKVPrefixFor(store.KindProvider, name)
	sc, sid := kvScopeFromOwnership(userID, agentID)
	// configs_kv-first: a marker-certified mirror answers both halves of the
	// caller's question without the blob — the payload (present) and the veto
	// (enabled). An uncertified row falls through to the blob below.
	if configsReadAuthority == configsKVFirst {
		if leaves, lerr := st.ListConfigValues(ctx, store.KindProvider, sc, sid, kvPrefix); lerr == nil && len(leaves) > 0 {
			if m, certified := certifiedMirror(ctx, st, store.KindProvider, sc, sid, name, leaves); certified {
				p, ok := kvValsToProviders(leaves)[name]
				if !ok {
					p = config.ProviderConfig{}
				}
				return p, true, m.Enabled != nil && *m.Enabled, nil
			}
		}
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
