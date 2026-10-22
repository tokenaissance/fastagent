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

	// Expired key is a new instruction.
	verdict, turn, err = db.ClaimChatIdempotency(ctx, "run|u|a|s|client|k", "d1", "t4", time.Nanosecond)
	if err != nil || verdict != IdempotencyMiss || turn != "t4" {
		t.Fatalf("after expiry = %q %q %v", verdict, turn, err)
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
