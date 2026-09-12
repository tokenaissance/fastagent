// Capability ports for the configs / configs_kv domain.
//
// Store is one 120-method interface. A consumer that needs "read a config
// row" declares no such need — it takes Store, and with it a dependency on
// users, agents, sessions, cron, MCP and the rest. The ports below split out
// the slice of Store that the configs domain actually uses, so a caller can
// state the capability it needs and a test double can implement six methods
// instead of embedding a real database.
//
// These are the store-side (implementation-side) declarations: Store is
// asserted to satisfy each of them at the bottom of this file, so a signature
// change in Store breaks the build here rather than at an unrelated call
// site. Consumers should still prefer a port to Store when they use only the
// port's methods.
//
// Scope of each port, by the tables it can touch:
//
//	ConfigReader    -> configs + configs_kv reads
//	ConfigReadStore -> configs + configs_kv reads + the mirror's marker
//	MirrorReader    -> the marker reads alone (point + per-scope list)
//	ConfigWriter    -> configs + configs_kv writes
//	ConfigStore     -> both (the whole configs domain)
//	KVStore         -> configs_kv only (the legacy blob stays untouched)
package store

import "context"

// ConfigReader is the read half of the configs domain: the legacy JSON-blob
// table (GetConfigByName, ListConfigs) plus its configs_kv mirror
// (ListConfigValues). Both tables belong in this port because every reader in
// the system resolves the same way — the mirror answers a row its marker
// certifies, the blob answers the rest (see ConfigReadStore) — so a reader that
// could only see one of them would be wrong, not narrow.
//
// BatchGetConfigsByAgentIDs is the batched form of ListConfigs restricted to
// the agent layer; it is here rather than in its own port because it answers
// the same question (resolve a namespace at some scope) with a different
// access shape, and every caller of it is a caller of the other three.
type ConfigReader interface {
	GetConfigByName(ctx context.Context, kind, userID, agentID, name string) (*ConfigRecord, error)
	ListConfigs(ctx context.Context, kind, userID, agentID string) ([]ConfigRecord, error)
	ListConfigValues(ctx context.Context, kind, scope, scopeID, namePrefix string) (map[string]ConfigValue, error)
	BatchGetConfigsByAgentIDs(ctx context.Context, kind, name string, agentIDs []string) ([]ConfigRecord, error)
}

// MirrorReader is the read half of ConfigMirrorStore: reading a row's
// completeness marker, singly or (ListConfigMirrors) for a whole scope at once.
// It is split out because a resolver that must certify a mirrored row only ever
// reads the marker — the dual-write and the reconciler are the only things that
// write one — so a read view can take this without also depending on marker
// writes.
type MirrorReader interface {
	GetConfigMirror(ctx context.Context, kind, scope, scopeID, name string) (ConfigMirror, bool, error)
	// ListConfigMirrors is the batched form: every marker at one scope, keyed by
	// row name, so a reader that certifies many rows issues one query instead of
	// one per row (see providersLayerAt, BatchSettings).
	ListConfigMirrors(ctx context.Context, kind, scope, scopeID string) (map[string]ConfigMirror, error)
}

// ConfigReadStore is the view a resolver needs once it must decide whether a
// configs_kv row set may be trusted: the two configs tables (ConfigReader) plus
// the completeness marker (MirrorReader) it verifies those rows against.
//
// Reading the marker is a read concern in its own right — a mirror-first reader
// loads the leaves and the marker and serves the leaves only if the marker
// certifies them (see ConfigMirror, MirrorSelfConsistent) — so it has to be
// part of the port such a reader takes. It is a separate composite rather than
// a widening of ConfigReader so that a caller which only reads rows, and never
// certifies them, still depends on four methods: every mirror-first read path
// (the merged resolvers, AgentScopeRows, Timezone) takes ConfigReadStore, and
// the views that answer from the blob's own rows by definition (SettingNamesAt,
// RowsAt) keep ConfigReader.
type ConfigReadStore interface {
	ConfigReader
	MirrorReader
}

// ConfigWriter is the write half of the configs domain. Writers need both
// tables because the two are kept in sync by a dual write inside one
// transaction (see scope.SaveSetting), and they need SaveConfig to create or
// replace the blob row that gives the entry its identity and enabled flag.
type ConfigWriter interface {
	SaveConfig(ctx context.Context, c *ConfigRecord) error
	DeleteConfig(ctx context.Context, id string) error
	SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value ConfigValue) error
	DeleteConfigValue(ctx context.Context, kind, scope, scopeID, name string) error
	DeleteConfigPrefix(ctx context.Context, kind, scope, scopeID, namePrefix string) error
}

// ConfigRowWriter is the blob-only write capability: create or replace one
// configs row (identity, enabled flag, JSON payload) without touching the
// mirror. Channel rows use it, and that is deliberate — channels are a
// derived index that migrateChannelsFromConfigs can rebuild from the blob,
// so they have no configs_kv half to keep in step (unlike settings and
// providers, which are dual-written in a transaction).
type ConfigRowWriter interface {
	SaveConfig(ctx context.Context, c *ConfigRecord) error
}

// ConfigMirrorStore is the completeness-marker capability for the configs_kv
// mirror (see ConfigMirror). It is its own port because a marker is metadata
// about the mirroring rather than a leaf of it: a consumer that only reads or
// writes mirror rows has no business deciding whether the mirror is certified,
// and the dual-write is the only thing that should be writing markers.
type ConfigMirrorStore interface {
	SaveConfigMirror(ctx context.Context, kind, scope, scopeID, name string, m ConfigMirror) error
	GetConfigMirror(ctx context.Context, kind, scope, scopeID, name string) (ConfigMirror, bool, error)
	DeleteConfigMirror(ctx context.Context, kind, scope, scopeID, name string) error
}

// ConfigMirrorReconciler is the one-shot certification pass over the whole
// configs table (see DBStore.ReconcileConfigMirrors). It is separate from
// ConfigMirrorStore because it is an operator action, not something a request
// path should ever reach for.
type ConfigMirrorReconciler interface {
	// repair rewrites diverged rows from the blob instead of only
	// reporting them. See DBStore.ReconcileConfigMirrors.
	ReconcileConfigMirrors(ctx context.Context, repair bool) (ConfigMirrorReconcile, error)
}

// ConfigStore is what a caller needs to read and write the configs domain.
// It is deliberately not Store: a handler that resolves settings has no
// business creating users or querying sessions.
type ConfigStore interface {
	ConfigReader
	ConfigWriter
	ConfigMirrorStore
}

// KVStore is the configs_kv-only slice — one value per row, addressed by a
// dotted name. The mirror writers (the dual-write halves inside a
// transaction) need nothing else, and a caller that only touches the mirror
// should say so, since the two tables have different durability stories:
// configs_kv has no rebuild path, the blob does.
type KVStore interface {
	ListConfigValues(ctx context.Context, kind, scope, scopeID, namePrefix string) (map[string]ConfigValue, error)
	SetConfigValue(ctx context.Context, kind, scope, scopeID, name string, value ConfigValue) error
	DeleteConfigValue(ctx context.Context, kind, scope, scopeID, name string) error
	DeleteConfigPrefix(ctx context.Context, kind, scope, scopeID, namePrefix string) error
	// The mirror writers record completeness in the same transaction as the
	// rows they certify, so a KVStore-only caller needs this half too.
	ConfigMirrorStore
}

// Store is a superset of every port above. These assertions are the contract:
// narrowing a consumer to a port can never fail on *DBStore, and adding a
// parameter to a configs method without updating the port is a compile error
// at this line.
var (
	_ ConfigReader           = (Store)(nil)
	_ ConfigReadStore        = (Store)(nil)
	_ MirrorReader           = (Store)(nil)
	_ ConfigWriter           = (Store)(nil)
	_ ConfigRowWriter        = (Store)(nil)
	_ ConfigMirrorStore      = (Store)(nil)
	_ ConfigMirrorReconciler = (Store)(nil)
	_ ConfigStore            = (Store)(nil)
	_ KVStore                = (Store)(nil)
)

// WithConfigTx is store.WithTx for a caller that typed its store as a port
// rather than as Store: the transaction handle is handed back as a
// ConfigStore, which is all such a caller can use anyway. Stores without
// transaction support get the same no-atomicity fallback as WithTx.
func WithConfigTx(ctx context.Context, st ConfigStore, fn func(ConfigStore) error) error {
	if txer, ok := st.(Txer); ok {
		return txer.WithTx(ctx, func(s Store) error { return fn(s) })
	}
	return fn(st)
}
