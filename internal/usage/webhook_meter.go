package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"time"
)

// ModelCost represents the pricing for a model (cents per 1M tokens)
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// tokensForJSON is the JSON representation of Tokens for webhook payload
type tokensForJSON struct {
	InputTokens         int `json:"inputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheReadTokens     int `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int `json:"cacheCreationTokens,omitempty"`
}

// UsageReport is the payload sent to webhook endpoint
type UsageReport struct {
	UserID       string        `json:"userId"`
	AgentID      string        `json:"agentId"`
	SessionID    string        `json:"sessionId,omitempty"`
	ProviderID   string        `json:"providerId"`
	ModelID      string        `json:"modelId"`
	Usage        tokensForJSON `json:"usage"`
	Cost         ModelCost     `json:"cost"`
	RawCostCents int           `json:"rawCostCents"`
	Timestamp    string        `json:"timestamp"`
}

// CalculateTokenCost computes cost in cents from token usage and model pricing.
// Formula: (tokens / 1M) × price for each category
// Returns ceiled integer cents to avoid fractional billing.
func CalculateTokenCost(tokens Tokens, cost ModelCost) int {
	inputCost := (float64(tokens.Input) / 1_000_000) * cost.Input
	outputCost := (float64(tokens.Output) / 1_000_000) * cost.Output
	cacheReadCost := (float64(tokens.CacheRead) / 1_000_000) * cost.CacheRead
	cacheCreationCost := (float64(tokens.CacheCreation) / 1_000_000) * cost.CacheWrite

	totalCost := inputCost + outputCost + cacheReadCost + cacheCreationCost

	// Return ceiled integer to avoid fractional cents
	return int(math.Ceil(totalCost))
}

// WebhookMeter wraps a local Meter and sends usage reports to a webhook endpoint.
// This is a generic implementation that doesn't depend on any specific backend.
type WebhookMeter struct {
	localMeter     Meter
	webhookURL     string
	webhookToken   string
	httpClient     *http.Client
	getCostFunc    func(provider, model string) *ModelCost // injected function to get model pricing
}

// NewWebhookMeter creates a meter that records locally and reports to a webhook.
func NewWebhookMeter(localMeter Meter, webhookURL, webhookToken string, getCostFunc func(string, string) *ModelCost) *WebhookMeter {
	return &WebhookMeter{
		localMeter:   localMeter,
		webhookURL:   webhookURL,
		webhookToken: webhookToken,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		getCostFunc:  getCostFunc,
	}
}

func (m *WebhookMeter) RecordTokens(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens) error {
	// 1. Record locally (existing behavior)
	err := m.localMeter.RecordTokens(ctx, userID, agentID, sessionKey, provider, model, t)
	if err != nil {
		return err
	}

	// 2. Send webhook asynchronously (new behavior)
	if m.webhookURL != "" && m.getCostFunc != nil {
		go m.sendWebhook(userID, agentID, sessionKey, provider, model, t)
	}

	return nil
}

func (m *WebhookMeter) sendWebhook(userID, agentID, sessionKey, provider, model string, t Tokens) {
	// Get model cost
	cost := m.getCostFunc(provider, model)
	if cost == nil {
		log.Printf("[WebhookMeter] No cost data for provider=%s model=%s, skipping webhook", provider, model)
		return
	}

	// Calculate cost
	rawCostCents := CalculateTokenCost(t, *cost)

	// Build payload with proper JSON field names
	report := UsageReport{
		UserID:     userID,
		AgentID:    agentID,
		SessionID:  sessionKey,
		ProviderID: provider,
		ModelID:    model,
		Usage: tokensForJSON{
			InputTokens:         t.Input,
			OutputTokens:        t.Output,
			CacheReadTokens:     t.CacheRead,
			CacheCreationTokens: t.CacheCreation,
		},
		Cost:         *cost,
		RawCostCents: rawCostCents,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(report)
	if err != nil {
		log.Printf("[WebhookMeter] Failed to marshal webhook payload: %v", err)
		return
	}

	// Send HTTP request
	req, err := http.NewRequest("POST", m.webhookURL, bytes.NewBuffer(body))
	if err != nil {
		log.Printf("[WebhookMeter] Failed to create webhook request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	if m.webhookToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.webhookToken)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.Printf("[WebhookMeter] Webhook request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[WebhookMeter] Webhook returned status %d", resp.StatusCode)
		return
	}

	log.Printf("[WebhookMeter] Usage reported: user=%s agent=%s model=%s cost=%d cents",
		userID, agentID, model, rawCostCents)
}

// Proxy all other methods to localMeter
func (m *WebhookMeter) Totals(ctx context.Context, r Range) (Totals, error) {
	return m.localMeter.Totals(ctx, r)
}

func (m *WebhookMeter) TopAgents(ctx context.Context, r Range, limit int) ([]Rank, error) {
	return m.localMeter.TopAgents(ctx, r, limit)
}

func (m *WebhookMeter) TopUsers(ctx context.Context, r Range, limit int) ([]Rank, error) {
	return m.localMeter.TopUsers(ctx, r, limit)
}

func (m *WebhookMeter) SessionsForAgent(ctx context.Context, agentID, userID string, r Range, limit int) ([]Rank, error) {
	return m.localMeter.SessionsForAgent(ctx, agentID, userID, r, limit)
}

func (m *WebhookMeter) Close() error {
	return m.localMeter.Close()
}
