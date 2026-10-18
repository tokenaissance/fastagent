package tools

/**
 * [INPUT]: the capability ports internal/store declares.
 * [OUTPUT]: CronToolStore and ConfigToolStore — the two slices the registered
 *           tools need.
 * [POS]: Consumer-side ports. Each tool family states the capability it uses,
 *        so a test double implements a handful of methods and a reader can see
 *        which tables a tool touches.
 * [PROTOCOL]: On change, update this header, then check
 *        internal/store/ports.go (the capability ports) and
 *        docs/fastagent/design/15-agent-config-consistency.md §11.1.
 */

import (
	storeport "github.com/fastclaw-ai/fastclaw/internal/store/port"
)

// CronToolStore is what the cron tools need: the scheduled-task rows, plus the
// timezone read the create path stamps on a new job so "9am" means 9am where
// the chatter lives.
type CronToolStore interface {
	storeport.CronStore
	storeport.ConfigReadStore
}

// ConfigToolStore is what the timezone and preference tools need: the resolver
// reads (a mirrored row is served only when its completeness marker certifies
// it) plus the dual write that keeps the blob and the mirror in step.
type ConfigToolStore interface {
	storeport.ConfigReadStore
	storeport.ConfigWriter
	storeport.ConfigMirrorStore
}
