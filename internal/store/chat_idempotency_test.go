package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestChatIdempotencyClaim(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	verdict, turn, err := db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d1", "t1", time.Minute)
	if err != nil || verdict != IdempotencyMiss || turn != "t1" {
		t.Fatalf("first = %q %q %v", verdict, turn, err)
	}
	verdict, turn, err = db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d1", "t2", time.Minute)
	if err != nil || verdict != IdempotencyDuplicate || turn != "t1" {
		t.Fatalf("retry = %q %q %v", verdict, turn, err)
	}
	verdict, _, err = db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d2", "t3", time.Minute)
	if err != nil || verdict != IdempotencyConflict {
		t.Fatalf("conflict = %q %v", verdict, err)
	}

	// Releasing a claim lets the key be used again (a failed steer).
	if err := db.DeleteChatIdempotency(ctx, "run|u|a|s|client|k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	verdict, turn, err = db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d1", "t5", time.Minute)
	if err != nil || verdict != IdempotencyMiss || turn != "t5" {
		t.Fatalf("after delete = %q %q %v", verdict, turn, err)
	}

	// Expired key is a new instruction.
	verdict, turn, err = db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d1", "t4", time.Nanosecond)
	if err != nil || verdict != IdempotencyMiss || turn != "t4" {
		t.Fatalf("after expiry = %q %q %v", verdict, turn, err)
	}
}

func TestChatIdempotencyCleanupBoundsTheTable(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	if _, _, err := db.ClaimChatIdempotency(ctx, "old", "d", "t1", time.Minute); err != nil {
		t.Fatalf("old claim: %v", err)
	}
	if _, err := db.handle().ExecContext(ctx, "UPDATE chat_idempotency SET created_at = ? WHERE scope_key = ?", time.Now().Add(-2*time.Minute), "old"); err != nil {
		t.Fatalf("age row: %v", err)
	}
	if _, _, err := db.ClaimChatIdempotency(ctx, "new", "d", "t2", time.Minute); err != nil {
		t.Fatalf("new claim: %v", err)
	}
	var n int
	if err := db.handle().QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_idempotency").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows after cleanup = %d; want 1", n)
	}
}

func TestChatIdempotencyConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()

	const n = 8
	var wg sync.WaitGroup
	verdicts := make(chan string, n)
	firsts := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, turn, err := db.ClaimChatIdempotency(ctx, "run|u|a|s|c|k", "same", "t", time.Minute)
			if err != nil {
				t.Errorf("claim %d: %v", i, err)
			}
			verdicts <- v
			firsts <- turn
		}(i)
	}
	wg.Wait()
	close(verdicts)
	close(firsts)
	misses := 0
	for v := range verdicts {
		if v == IdempotencyMiss {
			misses++
		}
	}
	if misses != 1 {
		t.Fatalf("misses = %d; want exactly one", misses)
	}
}
