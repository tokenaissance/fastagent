package gateway

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

// modelCostCache caches provider model costs to avoid DB queries on every LLM call
type modelCostCache struct {
	mu    sync.RWMutex
	cache map[string]*usage.ModelCost // key: "provider:model"
	ttl   time.Duration
	store store.Store
}

func newModelCostCache(st store.Store) *modelCostCache {
	return &modelCostCache{
		cache: make(map[string]*usage.ModelCost),
		ttl:   5 * time.Minute, // Cache for 5 minutes
		store: st,
	}
}

// getModelCost retrieves the cost for a given provider and model.
// Returns nil if the model is not found or has no cost data.
func (c *modelCostCache) getModelCost(provider, model string) *usage.ModelCost {
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

	// Query system-level provider config
	// Kind: "provider", Name: provider name (e.g., "anthropic")
	configRec, err := c.store.GetConfigByName(ctx, store.KindProvider, "", "", provider)
	if err != nil {
		log.Printf("[ModelCostCache] Failed to get provider config: provider=%s error=%v", provider, err)
		return nil
	}

	if configRec == nil || !configRec.Enabled {
		log.Printf("[ModelCostCache] Provider not found or disabled: %s", provider)
		return nil
	}

	// Parse provider config from Data field
	var providerCfg config.ProviderConfig
	dataJSON, err := json.Marshal(configRec.Data)
	if err != nil {
		log.Printf("[ModelCostCache] Failed to marshal provider data: %v", err)
		return nil
	}

	if err := json.Unmarshal(dataJSON, &providerCfg); err != nil {
		log.Printf("[ModelCostCache] Failed to unmarshal provider config: %v", err)
		return nil
	}

	// Find the model in the provider's models list
	var modelCost *usage.ModelCost
	for _, m := range providerCfg.Models {
		if m.ID == model {
			modelCost = &usage.ModelCost{
				Input:      m.Cost.Input,
				Output:     m.Cost.Output,
				CacheRead:  m.Cost.CacheRead,
				CacheWrite: m.Cost.CacheWrite,
			}
			break
		}
	}

	if modelCost == nil {
		log.Printf("[ModelCostCache] Model not found in provider config: provider=%s model=%s", provider, model)
		return nil
	}

	// Cache the result
	c.mu.Lock()
	c.cache[key] = modelCost
	c.mu.Unlock()

	log.Printf("[ModelCostCache] Cached model cost: provider=%s model=%s input=%.2f output=%.2f",
		provider, model, modelCost.Input, modelCost.Output)

	return modelCost
}

// invalidate clears the cache (useful for config updates)
func (c *modelCostCache) invalidate() {
	c.mu.Lock()
	c.cache = make(map[string]*usage.ModelCost)
	c.mu.Unlock()
}
