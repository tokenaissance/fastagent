package setup

import "testing"

func TestIdempotencyScopeKeyDoesNotCollideOnDelimiters(t *testing.T) {
	a := idempotencyScopeKey("run", "u", "a", "s", "client|tail", "key")
	b := idempotencyScopeKey("run", "u", "a", "s", "client", "tail|key")
	if a == b {
		t.Fatalf("scope key collision: %q", a)
	}
}
