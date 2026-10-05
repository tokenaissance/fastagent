package store

import (
	"context"
	"sync"
	"testing"
)

// The counter starts at 0 on a database with no row, and every bump returns a
// strictly greater value. Both properties are what the read cache leans on:
// "no row" and "epoch 0" must mean the same thing, and two writers must never
// see the same value.
func TestConfigEpochIsMonotoneAndStartsAtZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	got, err := db.CurrentConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("read empty epoch: %v", err)
	}
	if got != 0 {
		t.Fatalf("empty epoch = %d; want 0", got)
	}

	first, err := db.BumpConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("first bump: %v", err)
	}
	second, err := db.BumpConfigEpoch(ctx)
	if err != nil {
		t.Fatalf("second bump: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("bumps = (%d, %d); want (1, 2)", first, second)
	}
	if cur, err := db.CurrentConfigEpoch(ctx); err != nil || cur != second {
		t.Fatalf("read after bumps = (%d, %v); want (%d, nil)", cur, err, second)
	}
}

// Concurrent bumps return distinct values. The adapter does not need a lock:
// the upsert is one statement and the database serializes it.
func TestConfigEpochConcurrentBumpsAreDistinct(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	const writers = 16
	seen := make([]int64, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := db.BumpConfigEpoch(ctx)
			if err != nil {
				t.Errorf("bump %d: %v", i, err)
				return
			}
			seen[i] = v
		}(i)
	}
	wg.Wait()

	unique := map[int64]bool{}
	for _, v := range seen {
		if v == 0 {
			t.Fatalf("a bump returned 0 (a bump must be strictly greater than the start value)")
		}
		if unique[v] {
			t.Fatalf("two bumps returned %d; values must be distinct", v)
		}
		unique[v] = true
	}
	if cur, err := db.CurrentConfigEpoch(ctx); err != nil || cur != writers {
		t.Fatalf("final epoch = (%d, %v); want (%d, nil)", cur, err, writers)
	}
}
