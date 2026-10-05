package agent

/**
 * [INPUT]: the capability ports internal/store declares, plus goal.Store.
 * [OUTPUT]: Store — the whole slice of the relational store this package and
 *           the tools it registers depend on.
 * [POS]: Consumer-side port. The runtime used to take store.Store, a
 *        131-method interface, which meant a test double had to embed a
 *        database and every reader had to guess which tables the agent loop
 *        touches. This declaration answers that question, and the compiler
 *        checks it against the real store at every construction site.
 *        The store package states the capability ports; this file states the
 *        combination the agent runtime needs.
 * [PROTOCOL]: On change, update this header, then check
 *        docs/fastagent/design/15-agent-config-consistency.md §11.1 and the
 *        capability ports in internal/store/ports.go.
 */

import (
	"github.com/fastclaw-ai/fastclaw/internal/agent/goal"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Store is what the agent runtime needs from the database: the agent row, the
// identity and memory files, the knowledge corpus, MCP server rows, cron rows,
// the chatter message counter, the session event log, the configs domain, and
// the goal rows. Nothing here lets the agent create users, sessions, or
// channels.
type Store interface {
	store.AgentRuntimeStore
	goal.Store
}

// The port stays a subset of the real store. This line fails if the agent port
// grows a method that store.Store does not have, which is the mistake that would
// otherwise surface as a broken call site somewhere else.
var _ Store = (store.Store)(nil)
