// Capability ports for the configs / configs_kv domain.
//
// Store is one 115-method interface. A consumer that needs "read a config
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
//	ConfigReader  -> configs + configs_kv reads
//	ConfigWriter  -> configs + configs_kv writes
//	ConfigStore   -> both (the whole configs domain)
//	KVStore       -> configs_kv only (the legacy blob stays untouched)
package store

import "context"

// ConfigReader is the read half of the configs domain: the legacy JSON-blob
// table (GetConfigByName, ListConfigs) plus its configs_kv mirror
// (ListConfigValues). Both tables belong in this port because every reader in
// the system resolves the same way — blob first, mirror as the fallback — so
// a reader that could only see one of them would be wrong, not narrow.
type ConfigReader interface {
	GetConfigByName(ctx context.Context, kind, userID, agentID, name string) (*ConfigRecord, error)
	ListConfigs(ctx context.Context, kind, userID, agentID string) ([]ConfigRecord, error)
	ListConfigValues(ctx context.Context, kind, scope, scopeID, namePrefix string) (map[string]ConfigValue, error)
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

// ConfigStore is what a caller needs to read and write the configs domain.
// It is deliberately not Store: a handler that resolves settings has no
// business creating users or querying sessions.
type ConfigStore interface {
	ConfigReader
	ConfigWriter
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
}

// Store is a superset of every port above. These assertions are the contract:
// narrowing a consumer to a port can never fail on *DBStore, and adding a
// parameter to a configs method without updating the port is a compile error
// at this line.
var (
	_ ConfigReader    = (Store)(nil)
	_ ConfigWriter    = (Store)(nil)
	_ ConfigRowWriter = (Store)(nil)
	_ ConfigStore     = (Store)(nil)
	_ KVStore         = (Store)(nil)
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
