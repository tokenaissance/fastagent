package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// A retryable failure must not retry forever: after maxAttempts the row is
// marked dead so an operator can inspect or replay it deliberately.
func TestWebhookMeterMarksRetryExhaustionDead(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	outbox := NewMemOutbox()
	meter := newWebhookMeter(NewMemMeter(), outbox, server.URL, "token", func(string, string) *config.ModelCost {
		return &config.ModelCost{Input: 15, Output: 75}
	}, webhookMeterOptions{
		retryBase:    5 * time.Millisecond,
		retryMax:     10 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
		maxAttempts:  3,
	})
	t.Cleanup(func() { _ = meter.Close() })

	if err := meter.RecordTokens(context.Background(), "u1", "a1", "s1", "openai", "gpt", Tokens{Input: 1000, Output: 500}); err != nil {
		t.Fatalf("RecordTokens: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		outbox.mu.Lock()
		var status string
		var attempts int
		for _, entry := range outbox.entries {
			status = entry.status
			attempts = entry.attempts
		}
		outbox.mu.Unlock()

		if status == "dead" {
			if attempts != 3 {
				t.Fatalf("attempts = %d, want 3", attempts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox row not marked dead (status=%q attempts=%d)", status, attempts)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("webhook calls = %d, want 3 (one initial + two retries)", got)
	}
}
