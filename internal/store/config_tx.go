package store

import (
	"context"
	"errors"

	storeport "github.com/fastclaw-ai/fastclaw/internal/store/port"
)

// WithConfigTx is WithTx for a caller that typed its store as a configs port
// rather than as Store.
func WithConfigTx(ctx context.Context, st storeport.ConfigStore, fn func(storeport.ConfigTxStore) error) error {
	if txer, ok := st.(Txer); ok {
		return txer.WithTx(ctx, func(s Store) error { return fn(s) })
	}
	stamped, ok := st.(storeport.ConfigTxStore)
	if !ok {
		return errors.New("store: this store cannot stamp a config write (no BumpConfigEpoch)")
	}
	return fn(stamped)
}

var (
	_ storeport.ConfigReader           = (Store)(nil)
	_ storeport.ConfigReadStore        = (Store)(nil)
	_ storeport.MirrorReader           = (Store)(nil)
	_ storeport.ConfigWriter           = (Store)(nil)
	_ storeport.ConfigRowWriter        = (Store)(nil)
	_ storeport.ConfigMirrorStore      = (Store)(nil)
	_ storeport.ConfigMirrorReconciler = (Store)(nil)
	_ storeport.ConfigStore            = (Store)(nil)
	_ storeport.KVStore                = (Store)(nil)
	_ storeport.AgentReader            = (Store)(nil)
	_ storeport.AgentFileStore         = (Store)(nil)
	_ storeport.AgentKnowledgeStore    = (Store)(nil)
	_ storeport.AgentMCPServerStore    = (Store)(nil)
	_ storeport.CronStore              = (Store)(nil)
	_ storeport.ChatterCounter         = (Store)(nil)
	_ storeport.SessionEventReader     = (Store)(nil)
	_ storeport.ConfigValueReader      = (Store)(nil)
	_ storeport.ConfigEpochWriter      = (Store)(nil)
	_ storeport.ConfigTxStore          = (Store)(nil)
	_ storeport.AgentRuntimeStore      = (Store)(nil)
)
