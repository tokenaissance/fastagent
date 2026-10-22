package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/google/uuid"
)

// tokensForJSON is the JSON representation of Tokens for webhook payload
type tokensForJSON struct {
	InputTokens         int `json:"inputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheReadTokens     int `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens int `json:"cacheCreationTokens,omitempty"`
}

// UsageReport is the payload sent to webhook endpoint.
//
// ReportID is the idempotency key. The outbox retries the same payload, so
// the receiver can deduplicate a retry that arrives after a committed charge.
type UsageReport struct {
	ReportID     string           `json:"reportId"`
	UserID       string           `json:"userId"`
	AgentID      string           `json:"agentId"`
	SessionID    string           `json:"sessionId,omitempty"`
	ProviderID   string           `json:"providerId"`
	ModelID      string           `json:"modelId"`
	Usage        tokensForJSON    `json:"usage"`
	Cost         config.ModelCost `json:"cost"`
	RawCostCents int              `json:"rawCostCents"`
	Timestamp    string           `json:"timestamp"`
}

// CalculateTokenCost computes cost in cents from token usage and model pricing.
//
// Billing modes are mutually exclusive:
//   - PerCall > 0: flat fee per API call, token counts are ignored.
//   - Otherwise: (tokens / 1M) × price per category, result ceiled to integer cents.
func CalculateTokenCost(tokens Tokens, cost config.ModelCost) int {
	if cost.PerCall > 0 {
		return int(math.Ceil(cost.PerCall))
	}
	inputCost := (float64(tokens.Input) / 1_000_000) * cost.Input
	outputCost := (float64(tokens.Output) / 1_000_000) * cost.Output
	cacheReadCost := (float64(tokens.CacheRead) / 1_000_000) * cost.CacheRead
	cacheCreationCost := (float64(tokens.CacheCreation) / 1_000_000) * cost.CacheWrite
	return int(math.Ceil(inputCost + outputCost + cacheReadCost + cacheCreationCost))
}

// atomicOutboxRecorder is implemented by a local meter that can write its
// aggregate row and the outbox row in one transaction.
type atomicOutboxRecorder interface {
	RecordTokensAndEnqueue(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens, reportID string, payload []byte) error
}

// WebhookMeter wraps a local Meter, persists each usage report in an outbox,
// and delivers it with bounded exponential backoff.
//
// RecordTokens returns after the report is durable. Delivery happens on a
// dispatcher goroutine, so a slow or unavailable billing endpoint cannot
// block a chat turn.
type WebhookMeter struct {
	localMeter   Meter
	outbox       Outbox
	webhookURL   string
	webhookToken string
	httpClient   *http.Client
	getCostFunc  func(provider, model string) *config.ModelCost

	retryBase    time.Duration
	retryMax     time.Duration
	pollInterval time.Duration
	// maxAttempts bounds retries for retryable failures (5xx/429/timeout).
	// Without a cap a poison row retried forever; after this many attempts the
	// row is marked dead and can be inspected or replayed deliberately.
	maxAttempts int

	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewWebhookMeter keeps the historical constructor for callers that do not
// own a durable outbox. It uses an in-process outbox, which is suitable for
// tests and dev runs only.
func NewWebhookMeter(localMeter Meter, webhookURL, webhookToken string, getCostFunc func(string, string) *config.ModelCost) *WebhookMeter {
	return NewWebhookMeterWithOutbox(localMeter, NewMemOutbox(), webhookURL, webhookToken, getCostFunc)
}

// webhookMeterOptions are fixed at construction: the dispatcher goroutine
// reads them, so tests must not mutate them after the meter starts.
type webhookMeterOptions struct {
	retryBase    time.Duration
	retryMax     time.Duration
	pollInterval time.Duration
	maxAttempts  int
}

var defaultWebhookMeterOptions = webhookMeterOptions{
	retryBase:    2 * time.Second,
	retryMax:     5 * time.Minute,
	pollInterval: 5 * time.Second,
	maxAttempts:  20,
}

// NewWebhookMeterWithOutbox creates a meter backed by a durable outbox.
func NewWebhookMeterWithOutbox(localMeter Meter, outbox Outbox, webhookURL, webhookToken string, getCostFunc func(string, string) *config.ModelCost) *WebhookMeter {
	return newWebhookMeter(localMeter, outbox, webhookURL, webhookToken, getCostFunc, defaultWebhookMeterOptions)
}

func newWebhookMeter(localMeter Meter, outbox Outbox, webhookURL, webhookToken string, getCostFunc func(string, string) *config.ModelCost, opts webhookMeterOptions) *WebhookMeter {
	if outbox == nil {
		outbox = NewMemOutbox()
	}
	m := &WebhookMeter{
		localMeter:   localMeter,
		outbox:       outbox,
		webhookURL:   webhookURL,
		webhookToken: webhookToken,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		getCostFunc:  getCostFunc,
		retryBase:    opts.retryBase,
		retryMax:     opts.retryMax,
		pollInterval: opts.pollInterval,
		maxAttempts:  opts.maxAttempts,
		wake:         make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go m.run()
	return m
}

func (m *WebhookMeter) RecordTokens(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens) error {
	if m.webhookURL == "" || m.getCostFunc == nil {
		return m.localMeter.RecordTokens(ctx, userID, agentID, sessionKey, provider, model, t)
	}

	cost := m.getCostFunc(provider, model)
	if cost == nil {
		log.Printf("[WebhookMeter] No cost data for provider=%s model=%s, skipping webhook", provider, model)
		return m.localMeter.RecordTokens(ctx, userID, agentID, sessionKey, provider, model, t)
	}

	report := UsageReport{
		ReportID:   uuid.NewString(),
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
		RawCostCents: CalculateTokenCost(t, *cost),
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal usage report: %w", err)
	}

	if atomic, ok := m.localMeter.(atomicOutboxRecorder); ok {
		if err := atomic.RecordTokensAndEnqueue(ctx, userID, agentID, sessionKey, provider, model, t, report.ReportID, payload); err != nil {
			return err
		}
	} else {
		if err := m.localMeter.RecordTokens(ctx, userID, agentID, sessionKey, provider, model, t); err != nil {
			return err
		}
		if err := m.outbox.Enqueue(ctx, report.ReportID, payload); err != nil {
			return err
		}
	}
	m.signal()
	return nil
}

func (m *WebhookMeter) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *WebhookMeter) run() {
	defer close(m.done)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	m.dispatch()
	for {
		select {
		case <-m.stop:
			return
		case <-m.wake:
		case <-ticker.C:
		}
		m.dispatch()
	}
}

type deliveryKind int

const (
	deliverySuccess deliveryKind = iota
	deliveryRetry
	deliveryPermanent
)

type deliveryOutcome struct {
	kind deliveryKind
	err  string
}

func (m *WebhookMeter) dispatch() {
	ctx := context.Background()

	entries, err := m.outbox.Pending(ctx, time.Now(), 50)
	if err != nil {
		log.Printf("[WebhookMeter] Read outbox failed: %v", err)
		return
	}
	for _, entry := range entries {
		outcome := m.post(ctx, entry.Payload)
		switch outcome.kind {
		case deliverySuccess:
			if err := m.outbox.MarkSent(ctx, entry.ID); err != nil {
				log.Printf("[WebhookMeter] Mark report %s sent failed: %v", entry.ID, err)
			}
		case deliveryPermanent:
			attempts := entry.Attempts + 1
			if err := m.outbox.MarkDead(ctx, entry.ID, attempts, outcome.err); err != nil {
				log.Printf("[WebhookMeter] Mark report %s dead failed: %v", entry.ID, err)
			}
			log.Printf("[WebhookMeter] Report %s rejected permanently: %s", entry.ID, outcome.err)
		default:
			attempts := entry.Attempts + 1
			if m.maxAttempts > 0 && attempts >= m.maxAttempts {
				if err := m.outbox.MarkDead(ctx, entry.ID, attempts, outcome.err); err != nil {
					log.Printf("[WebhookMeter] Mark report %s dead failed: %v", entry.ID, err)
				}
				log.Printf("[WebhookMeter] Report %s exhausted %d attempts, marked dead: %s", entry.ID, attempts, outcome.err)
				continue
			}
			next := time.Now().Add(m.backoff(attempts))
			if err := m.outbox.MarkRetry(ctx, entry.ID, attempts, next, outcome.err); err != nil {
				log.Printf("[WebhookMeter] Schedule retry for report %s failed: %v", entry.ID, err)
			}
			if delay := time.Until(next); delay > 0 {
				time.AfterFunc(delay, m.signal)
			} else {
				m.signal()
			}
			log.Printf("[WebhookMeter] Report %s failed (attempt %d), retry after %s: %s", entry.ID, attempts, next.Format(time.RFC3339), outcome.err)
		}
	}
}

func (m *WebhookMeter) post(ctx context.Context, payload []byte) deliveryOutcome {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, m.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return deliveryOutcome{kind: deliveryPermanent, err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if m.webhookToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.webhookToken)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return deliveryOutcome{kind: deliveryRetry, err: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var ack struct {
			OK    *bool  `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &ack) == nil && ack.OK != nil && !*ack.OK {
			return deliveryOutcome{kind: deliveryPermanent, err: ack.Error}
		}
		return deliveryOutcome{kind: deliverySuccess}
	}

	msg := fmt.Sprintf("webhook status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500 {
		return deliveryOutcome{kind: deliveryRetry, err: msg}
	}
	return deliveryOutcome{kind: deliveryPermanent, err: msg}
}

func (m *WebhookMeter) backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := m.retryBase
	for i := 1; i < attempts && d < m.retryMax; i++ {
		d *= 2
	}
	if d > m.retryMax {
		d = m.retryMax
	}
	return d
}

// Close stops the dispatcher and closes the local meter. Reports that are
// still pending stay in the outbox and are delivered by the next process.
func (m *WebhookMeter) Close() error {
	var err error
	m.closeOnce.Do(func() {
		close(m.stop)
		select {
		case <-m.done:
		case <-time.After(5 * time.Second):
		}
		_ = m.outbox.Close()
		err = m.localMeter.Close()
	})
	return err
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

// TotalsForUser, DailyForUser, RecordTokenLog delegate to the local
// meter. They exist so WebhookMeter satisfies the expanded Meter
// interface (added with upstream billing); the webhook path only fires
// per-call from RecordTokens, so per-user readbacks and the append-only
// log stay on the local backend.
func (m *WebhookMeter) TotalsForUser(ctx context.Context, userID string, r Range) (Totals, error) {
	return m.localMeter.TotalsForUser(ctx, userID, r)
}

func (m *WebhookMeter) DailyForUser(ctx context.Context, userID string, r Range) ([]DailyUsage, error) {
	return m.localMeter.DailyForUser(ctx, userID, r)
}

func (m *WebhookMeter) RecordTokenLog(ctx context.Context, userID, agentID, sessionKey, provider, model string, t Tokens, durationMs int64) error {
	return m.localMeter.RecordTokenLog(ctx, userID, agentID, sessionKey, provider, model, t, durationMs)
}
