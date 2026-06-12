package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCalculateTokenCost(t *testing.T) {
	tests := []struct {
		name   string
		tokens Tokens
		cost   ModelCost
		want   int
	}{
		{
			name: "basic input/output tokens",
			tokens: Tokens{
				Input:  1000,
				Output: 500,
			},
			cost: ModelCost{
				Input:      15,   // $0.015 per 1M = 1.5 cents per 100k
				Output:     75,   // $0.075 per 1M = 7.5 cents per 100k
				CacheRead:  1.5,
				CacheWrite: 18.75,
			},
			// (1000/1M * 15) + (500/1M * 75) = 0.015 + 0.0375 = 0.0525 cents
			// Ceiled = 1 cent
			want: 1,
		},
		{
			name: "with cache tokens",
			tokens: Tokens{
				Input:         10000,
				Output:        5000,
				CacheRead:     20000,
				CacheCreation: 15000,
			},
			cost: ModelCost{
				Input:      15,
				Output:     75,
				CacheRead:  1.5,
				CacheWrite: 18.75,
			},
			// (10000/1M * 15) + (5000/1M * 75) + (20000/1M * 1.5) + (15000/1M * 18.75)
			// = 0.15 + 0.375 + 0.03 + 0.28125 = 0.83625 cents
			// Ceiled = 1 cent
			want: 1,
		},
		{
			name: "large token counts",
			tokens: Tokens{
				Input:  1_000_000, // 1M tokens
				Output: 500_000,   // 500K tokens
			},
			cost: ModelCost{
				Input:      15, // 15 cents per 1M
				Output:     75, // 75 cents per 1M
				CacheRead:  0,
				CacheWrite: 0,
			},
			// (1M/1M * 15) + (500K/1M * 75) = 15 + 37.5 = 52.5 cents
			// Ceiled = 53 cents
			want: 53,
		},
		{
			name: "zero tokens",
			tokens: Tokens{
				Input:  0,
				Output: 0,
			},
			cost: ModelCost{
				Input:      15,
				Output:     75,
				CacheRead:  0,
				CacheWrite: 0,
			},
			want: 0,
		},
		{
			name: "fractional cents should ceil",
			tokens: Tokens{
				Input:  100,
				Output: 50,
			},
			cost: ModelCost{
				Input:      15,
				Output:     75,
				CacheRead:  0,
				CacheWrite: 0,
			},
			// (100/1M * 15) + (50/1M * 75) = 0.0015 + 0.00375 = 0.00525 cents
			// Ceiled = 1 cent
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateTokenCost(tt.tokens, tt.cost)
			if got != tt.want {
				t.Errorf("CalculateTokenCost() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWebhookMeter_RecordTokens tests the webhook callback functionality
func TestWebhookMeter_RecordTokens(t *testing.T) {
	tests := []struct {
		name              string
		webhookURL        string
		webhookToken      string
		expectedAuthToken string
		tokens            Tokens
		cost              *ModelCost
		wantErr           bool
		validatePayload   func(t *testing.T, report UsageReport)
	}{
		{
			name:              "successful webhook with auth token",
			webhookToken:      "test-token-123",
			expectedAuthToken: "Bearer test-token-123",
			tokens: Tokens{
				Input:  10000,
				Output: 5000,
			},
			cost: &ModelCost{
				Input:      15,
				Output:     75,
				CacheRead:  1.5,
				CacheWrite: 18.75,
			},
			wantErr: false,
			validatePayload: func(t *testing.T, report UsageReport) {
				if report.UserID != "test-user" {
					t.Errorf("UserID = %v, want test-user", report.UserID)
				}
				if report.AgentID != "test-agent" {
					t.Errorf("AgentID = %v, want test-agent", report.AgentID)
				}
				if report.ModelID != "gpt-4o" {
					t.Errorf("ModelID = %v, want gpt-4o", report.ModelID)
				}
				if report.Usage.InputTokens != 10000 {
					t.Errorf("InputTokens = %v, want 10000", report.Usage.InputTokens)
				}
				if report.Usage.OutputTokens != 5000 {
					t.Errorf("OutputTokens = %v, want 5000", report.Usage.OutputTokens)
				}
				// (10000/1M * 15) + (5000/1M * 75) = 0.15 + 0.375 = 0.525 cents → ceiled to 1
				if report.RawCostCents != 1 {
					t.Errorf("RawCostCents = %v, want 1", report.RawCostCents)
				}
			},
		},
		{
			name:              "webhook with cache tokens",
			webhookToken:      "test-token-456",
			expectedAuthToken: "Bearer test-token-456",
			tokens: Tokens{
				Input:         1000,
				Output:        500,
				CacheRead:     2000,
				CacheCreation: 1500,
			},
			cost: &ModelCost{
				Input:      15,
				Output:     75,
				CacheRead:  1.5,
				CacheWrite: 18.75,
			},
			wantErr: false,
			validatePayload: func(t *testing.T, report UsageReport) {
				if report.Usage.CacheReadTokens != 2000 {
					t.Errorf("CacheReadTokens = %v, want 2000", report.Usage.CacheReadTokens)
				}
				if report.Usage.CacheCreationTokens != 1500 {
					t.Errorf("CacheCreationTokens = %v, want 1500", report.Usage.CacheCreationTokens)
				}
			},
		},
		{
			name:         "webhook without cost data (should skip)",
			webhookToken: "test-token-789",
			tokens: Tokens{
				Input:  1000,
				Output: 500,
			},
			cost:    nil, // No cost data
			wantErr: false,
			validatePayload: func(t *testing.T, report UsageReport) {
				t.Error("Should not receive webhook when cost is nil")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedReport UsageReport
			var receivedAuthHeader string
			var mu sync.Mutex
			webhookCalled := false

			// Create mock webhook server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()

				// Verify request method and content type
				if r.Method != "POST" {
					t.Errorf("Expected POST request, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Expected Content-Type: application/json, got %s", r.Header.Get("Content-Type"))
				}

				// Verify auth header
				receivedAuthHeader = r.Header.Get("Authorization")
				if tt.expectedAuthToken != "" && receivedAuthHeader != tt.expectedAuthToken {
					t.Errorf("Authorization = %v, want %v", receivedAuthHeader, tt.expectedAuthToken)
				}

				// Parse body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("Failed to read request body: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				if err := json.Unmarshal(body, &receivedReport); err != nil {
					t.Errorf("Failed to unmarshal request body: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				webhookCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			// Create mock local meter
			localMeter := NewMemMeter()

			// Create cost function
			getCostFunc := func(provider, model string) *ModelCost {
				return tt.cost
			}

			// Create WebhookMeter
			webhookMeter := NewWebhookMeter(localMeter, server.URL, tt.webhookToken, getCostFunc)

			// Record tokens
			ctx := context.Background()
			err := webhookMeter.RecordTokens(ctx, "test-user", "test-agent", "test-session", "openai", "gpt-4o", tt.tokens)

			if (err != nil) != tt.wantErr {
				t.Errorf("RecordTokens() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Wait for async webhook (with timeout)
			if tt.cost != nil {
				time.Sleep(100 * time.Millisecond)

				mu.Lock()
				called := webhookCalled
				mu.Unlock()

				if !called {
					t.Error("Webhook was not called")
				} else {
					tt.validatePayload(t, receivedReport)
				}
			}
		})
	}
}

// TestWebhookMeter_Integration tests the full integration with gateway setup
func TestWebhookMeter_Integration(t *testing.T) {
	// Track all webhook calls
	var webhookCalls []UsageReport
	var mu sync.Mutex

	// Create mock webhook server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		var report UsageReport
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &report); err != nil {
			t.Errorf("Failed to unmarshal: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		webhookCalls = append(webhookCalls, report)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create local meter
	localMeter := NewMemMeter()

	// Create cost function
	getCostFunc := func(provider, model string) *ModelCost {
		return &ModelCost{
			Input:      15,
			Output:     75,
			CacheRead:  1.5,
			CacheWrite: 18.75,
		}
	}

	// Create WebhookMeter (simulating gateway.go setup)
	meter := NewWebhookMeter(localMeter, server.URL, "integration-test-token", getCostFunc)

	// Simulate multiple LLM calls
	ctx := context.Background()
	testCases := []struct {
		userID    string
		agentID   string
		sessionID string
		tokens    Tokens
	}{
		{
			userID:    "user1",
			agentID:   "agent1",
			sessionID: "session1",
			tokens:    Tokens{Input: 1000, Output: 500},
		},
		{
			userID:    "user1",
			agentID:   "agent1",
			sessionID: "session1",
			tokens:    Tokens{Input: 2000, Output: 1000},
		},
		{
			userID:    "user2",
			agentID:   "agent2",
			sessionID: "session2",
			tokens:    Tokens{Input: 5000, Output: 2500},
		},
	}

	for _, tc := range testCases {
		if err := meter.RecordTokens(ctx, tc.userID, tc.agentID, tc.sessionID, "anthropic", "claude-3-sonnet", tc.tokens); err != nil {
			t.Errorf("RecordTokens failed: %v", err)
		}
	}

	// Wait for all webhooks to complete
	time.Sleep(200 * time.Millisecond)

	// Verify all webhooks were called
	mu.Lock()
	defer mu.Unlock()

	if len(webhookCalls) != len(testCases) {
		t.Errorf("Expected %d webhook calls, got %d", len(testCases), len(webhookCalls))
	}

	// Build a list of expected calls per userID (order-independent check)
	type expectedCall struct {
		userID string
		tokens Tokens
	}
	remaining := make([]expectedCall, len(testCases))
	for i, tc := range testCases {
		remaining[i] = expectedCall{tc.userID, tc.tokens}
	}
	// Verify each webhook call — match by userID since goroutine order is non-deterministic
	for _, call := range webhookCalls {
		found := false
		for i, exp := range remaining {
			if exp.userID == call.UserID && exp.tokens.Input == call.Usage.InputTokens {
				remaining = append(remaining[:i], remaining[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Unexpected webhook call: UserID=%v InputTokens=%v", call.UserID, call.Usage.InputTokens)
		}
	}
	if len(remaining) > 0 {
		for _, r := range remaining {
			t.Errorf("Missing webhook call: UserID=%v InputTokens=%v", r.userID, r.tokens.Input)
		}
	}

	// Verify local meter also recorded
	totals, err := meter.Totals(ctx, Range{})
	if err != nil {
		t.Errorf("Failed to get totals: %v", err)
	}

	expectedInput := int64(1000 + 2000 + 5000)
	expectedOutput := int64(500 + 1000 + 2500)
	if totals.Input != expectedInput {
		t.Errorf("Local meter Input = %v, want %v", totals.Input, expectedInput)
	}
	if totals.Output != expectedOutput {
		t.Errorf("Local meter Output = %v, want %v", totals.Output, expectedOutput)
	}
}

// TestWebhookMeter_ErrorHandling tests error scenarios
func TestWebhookMeter_ErrorHandling(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse int
		serverDelay    time.Duration
		expectLog      string
	}{
		{
			name:           "webhook returns 500",
			serverResponse: http.StatusInternalServerError,
			expectLog:      "Webhook returned status 500",
		},
		{
			name:           "webhook returns 401",
			serverResponse: http.StatusUnauthorized,
			expectLog:      "Webhook returned status 401",
		},
		{
			name:           "webhook timeout",
			serverResponse: http.StatusOK,
			serverDelay:    15 * time.Second, // Longer than 10s timeout
			expectLog:      "Webhook request failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock webhook server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.serverDelay > 0 {
					time.Sleep(tt.serverDelay)
				}
				w.WriteHeader(tt.serverResponse)
			}))
			defer server.Close()

			localMeter := NewMemMeter()
			getCostFunc := func(provider, model string) *ModelCost {
				return &ModelCost{Input: 15, Output: 75}
			}

			meter := NewWebhookMeter(localMeter, server.URL, "test-token", getCostFunc)

			// Record tokens (should not fail even if webhook fails)
			ctx := context.Background()
			err := meter.RecordTokens(ctx, "test-user", "test-agent", "test-session", "openai", "gpt-4o", Tokens{Input: 1000, Output: 500})

			if err != nil {
				t.Errorf("RecordTokens should not fail when webhook fails: %v", err)
			}

			// Wait for webhook to complete
			time.Sleep(200 * time.Millisecond)

			// Verify local meter still recorded
			totals, _ := meter.Totals(ctx, Range{})
			if totals.Input != 1000 {
				t.Errorf("Local meter should still record: Input = %v, want 1000", totals.Input)
			}
		})
	}
}
