package gateway

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// modelCostCache caches provider model costs to avoid DB queries on every LLM call
type modelCostCache struct {
	mu    sync.RWMutex
	cache map[string]*config.ModelCost // key: "provider:model"
	ttl   time.Duration
	store store.Store
}

func newModelCostCache(st store.Store) *modelCostCache {
	return &modelCostCache{
		cache: make(map[string]*config.ModelCost),
		ttl:   5 * time.Minute,
		store: st,
	}
}

// getModelCost retrieves the cost for a given provider and model.
// Returns nil if the model is not found or has no cost data.
func (c *modelCostCache) getModelCost(provider, model string) *config.ModelCost {
	key := provider + ":" + model

	// Check cache first
	c.mu.RLock()
	if cost, ok := c.cache[key]; ok {
		c.mu.RUnlock()
		return cost
	}
	c.mu.RUnlock()

	// Cache miss - query from database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// System-scope provider, resolved through the read model rather than the
	// configs table: the resolver answers both "is it in the blob or the
	// mirror" and "is this scope offering it at all" (a disabled row is not).
	providerCfg, _, enabled, err := scope.ProviderStateAt(ctx, c.store, provider, "", "")
	if err != nil {
		log.Printf("[ModelCostCache] Failed to get provider config: provider=%s error=%v", provider, err)
		return nil
	}

	if !enabled {
		log.Printf("[ModelCostCache] Provider not found or disabled: %s", provider)
		return nil
	}

	// Find the model and return a copy of its cost directly — no manual field copying needed
	for _, m := range providerCfg.Models {
		if m.ID == model {
			cost := m.Cost // config.ModelCost, used as-is
			c.mu.Lock()
			c.cache[key] = &cost
			c.mu.Unlock()

			log.Printf("[ModelCostCache] Cached model cost: provider=%s model=%s input=%.2f output=%.2f perCall=%.2f",
				provider, model, cost.Input, cost.Output, cost.PerCall)
			return &cost
		}
	}

	log.Printf("[ModelCostCache] Model not found in provider config: provider=%s model=%s", provider, model)
	return nil
}

// invalidate clears the cache (useful for config updates)
func (c *modelCostCache) invalidate() {
	c.mu.Lock()
	c.cache = make(map[string]*config.ModelCost)
	c.mu.Unlock()
}
