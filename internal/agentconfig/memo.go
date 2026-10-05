package agentconfig

/**
 * [INPUT]: nothing beyond the package's Memo port and a size cap.
 * [OUTPUT]: MemoryMemo — the in-process cache adapter: a map behind one mutex,
 *           with the monotone rule that a newer entry is never replaced by an
 *           older one.
 * [POS]: Adapter for the Memo port. The use case decides what to keep. This file
 *        decides where, and today the answer is process memory. A shared level
 *        (Redis) is a future adapter behind the same port, not a change here.
 * [PROTOCOL]: On change, update this header, then check internal/agentconfig
 *        (the port) and docs/fastagent/design/15-agent-config-consistency.md §4.
 */

import (
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// defaultMemoCapacity bounds the map when a caller does not size it. The counter
// makes an eviction harmless: a missing entry costs one rebuild, never a stale
// read.
const defaultMemoCapacity = 4096

type memoEntry struct {
	cfg     config.ResolvedAgent
	version Version
}

// MemoryMemo is a bounded, process-local Memo.
type MemoryMemo struct {
	mu       sync.RWMutex
	entries  map[Scope]memoEntry
	capacity int
}

// NewMemoryMemo returns an empty memo. A capacity <= 0 uses the default.
func NewMemoryMemo(capacity int) *MemoryMemo {
	if capacity <= 0 {
		capacity = defaultMemoCapacity
	}
	return &MemoryMemo{entries: map[Scope]memoEntry{}, capacity: capacity}
}

func (m *MemoryMemo) Get(scope Scope) (config.ResolvedAgent, Version, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[scope]
	if !ok {
		return config.ResolvedAgent{}, 0, false
	}
	return entry.cfg, entry.version, true
}

func (m *MemoryMemo) Put(scope Scope, cfg config.ResolvedAgent, version Version) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.entries[scope]; ok && version < current.version {
		// A slow rebuild finished after a newer entry landed. Keep the newer.
		return
	}
	if len(m.entries) >= m.capacity {
		// Drop everything rather than track an LRU: the counter makes the cost
		// of a miss one rebuild, and this path is rare by construction.
		m.entries = map[Scope]memoEntry{}
	}
	m.entries[scope] = memoEntry{cfg: cfg, version: version}
}

func (m *MemoryMemo) Drop(scope Scope) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, scope)
}
