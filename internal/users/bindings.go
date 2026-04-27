package users

import (
	"context"
	"errors"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// AgentBindings maps each agent to at most one API key. Agents without a
// binding are "admin-owned by default" — only the admin token can touch
// them. Persistence goes through the configured store, so SaaS deployments
// don't lose bindings on pod restart.
//
// Bindings are control-plane only; the data plane (sessions, memory,
// workspace files) still keys purely on agent_id.
type AgentBindings struct {
	store store.Store
	mu    sync.RWMutex
	// Local cache of the binding map. Refreshed lazily on read; mutations
	// also update the cache in place to avoid a follow-up DB round-trip.
	cache       map[string]string
	cacheLoaded bool
}

// LoadBindings returns a binding registry backed by the given store. nil
// store is rejected — file-backed callers should pass an explicit FileStore.
func LoadBindings(st store.Store) (*AgentBindings, error) {
	if st == nil {
		return nil, errors.New("users.LoadBindings: store is required")
	}
	return &AgentBindings{store: st, cache: map[string]string{}}, nil
}

// ensure populates the local cache from the store on first access. Called
// under b.mu.
func (b *AgentBindings) ensureLocked() {
	if b.cacheLoaded {
		return
	}
	if m, err := b.store.ListAgentBindings(context.Background()); err == nil && m != nil {
		b.cache = m
	}
	b.cacheLoaded = true
}

// Save is a no-op kept for backward compatibility — all mutations persist
// immediately through the store.
func (b *AgentBindings) Save() error { return nil }

// Bind associates agentID with apiKeyID, replacing any prior binding. An
// empty apiKeyID is treated as Unbind(agentID).
func (b *AgentBindings) Bind(agentID, apiKeyID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureLocked()
	if apiKeyID == "" {
		_ = b.store.DeleteAgentBinding(context.Background(), agentID)
		delete(b.cache, agentID)
		return
	}
	if err := b.store.SetAgentBinding(context.Background(), agentID, apiKeyID); err == nil {
		b.cache[agentID] = apiKeyID
	}
}

// Unbind removes any binding for agentID. Safe to call even if none exists.
func (b *AgentBindings) Unbind(agentID string) {
	b.Bind(agentID, "")
}

// OwnerOf returns the api key id that owns agentID, or "" if unbound. Hot
// path on every authenticated request — served from the local cache.
func (b *AgentBindings) OwnerOf(agentID string) string {
	b.mu.Lock()
	b.ensureLocked()
	owner := b.cache[agentID]
	b.mu.Unlock()
	return owner
}

// AgentsOf returns every agent id bound to apiKeyID, in no particular order.
func (b *AgentBindings) AgentsOf(apiKeyID string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureLocked()
	var out []string
	for agentID, ownerID := range b.cache {
		if ownerID == apiKeyID {
			out = append(out, agentID)
		}
	}
	return out
}

// All returns the full binding map (copy, safe to mutate).
func (b *AgentBindings) All() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureLocked()
	out := make(map[string]string, len(b.cache))
	for k, v := range b.cache {
		out[k] = v
	}
	return out
}

// Refresh forces a reload from the store. Useful after operations that
// mutate bindings outside this process (e.g. a separate pod in SaaS).
func (b *AgentBindings) Refresh() {
	b.mu.Lock()
	b.cacheLoaded = false
	b.ensureLocked()
	b.mu.Unlock()
}
