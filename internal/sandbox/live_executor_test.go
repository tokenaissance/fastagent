package sandbox

// LiveExecutor is the pool's read-only half: it answers "is there an instance
// for this scope RIGHT NOW" without touching the network, the lease registry, or
// the create path. The panel's delete depends on that (docs 10 §4 G21 / d1):
// deleting a file in a scope nobody is running must not mint a sandbox.

import "testing"

func TestE2BPoolLiveExecutorDoesNotCreate(t *testing.T) {
	pool := NewE2BExecutorPool("api-key", "tpl", "", 0)

	ex, ok := pool.LiveExecutor("agt_none", "", "chat_none")
	if ok || ex != nil {
		t.Fatalf("LiveExecutor returned (%v,%v) for a scope with no instance", ex, ok)
	}
	if n := len(pool.executors); n != 0 {
		t.Fatalf("LiveExecutor left %d executors behind; it must be a pure read", n)
	}

	// With an instance cached, it hands that one back and still creates nothing.
	cached := &E2BExecutor{}
	pool.executors[poolKey("agt_1", "", "chat_1")] = cached
	got, ok := pool.LiveExecutor("agt_1", "", "chat_1")
	if !ok || got != Executor(cached) {
		t.Fatalf("LiveExecutor = (%v,%v); want the cached instance", got, ok)
	}
	if n := len(pool.executors); n != 1 {
		t.Fatalf("executors = %d; want the one that was already there", n)
	}
}

func TestDockerPoolLiveExecutorDoesNotCreate(t *testing.T) {
	pool := NewDockerExecutorPool("image", "", nil)
	if ex, ok := pool.LiveExecutor("agt_none", "", "chat_none"); ok || ex != nil {
		t.Fatalf("LiveExecutor returned (%v,%v) for a scope with no container", ex, ok)
	}
	if n := len(pool.executors); n != 0 {
		t.Fatalf("LiveExecutor created %d containers; it must be a pure read", n)
	}
}
