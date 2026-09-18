package store

// The "this instance's /workspace was never filled" bit (docs 10 §4, G19).
//
// It is a fact about the INSTANCE, so it has to ride the row that names the
// instance, be readable by whichever replica adopts it, and never be pinned
// onto a successor. Those three properties are what these tests pin.

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

func TestSandboxLeaseUnhydratedRidesTheInstance(t *testing.T) {
	ctx := context.Background()
	db := newTestSandboxLeaseDB(t)
	var st sandbox.SandboxLeaseStore = db
	const scope = "agt_unhydrated:s:sess_1"
	ttl := time.Minute

	if _, acquired, err := st.AcquireSandboxLease(ctx, scope, "pod-a", "sb-a", "tok-a", "tpl", ttl); err != nil || !acquired {
		t.Fatalf("acquire: acquired=%v err=%v", acquired, err)
	}
	// A fresh instance starts as "unknown, assume filled": the pool re-marks it
	// if its own hydrate failed.
	if got, _ := st.GetSandboxLease(ctx, scope); got == nil || got.Unhydrated {
		t.Fatalf("fresh lease = %+v; want Unhydrated=false", got)
	}

	// The owner records what its hydrate found, and any replica reads it back —
	// this is the read an adopting pod performs instead of replaying hydration.
	if err := st.SetSandboxLeaseUnhydrated(ctx, scope, "pod-a", "sb-a", true); err != nil {
		t.Fatalf("mark unhydrated: %v", err)
	}
	got, err := st.GetSandboxLease(ctx, scope)
	if err != nil || got == nil || !got.Unhydrated {
		t.Fatalf("after marking: rec=%+v err=%v; want Unhydrated=true", got, err)
	}

	// A pod that no longer owns the scope, or that names the wrong instance,
	// must not be able to annotate it.
	if err := st.SetSandboxLeaseUnhydrated(ctx, scope, "pod-b", "sb-a", false); err != nil {
		t.Fatalf("foreign write: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); !got.Unhydrated {
		t.Fatal("a foreign owner cleared the flag")
	}
	if err := st.SetSandboxLeaseUnhydrated(ctx, scope, "pod-a", "sb-OTHER", false); err != nil {
		t.Fatalf("wrong-instance write: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); !got.Unhydrated {
		t.Fatal("a write naming another instance cleared the flag")
	}

	// A replacement publishes a NEW identity into the row, so the flag cannot
	// survive onto the successor: the successor has its own hydrate.
	epoch, err := st.ReplaceSandboxLease(ctx, scope, "pod-a", "sb-b", "tok-b", "tpl", ttl)
	if err != nil || epoch == 0 {
		t.Fatalf("replace: epoch=%d err=%v", epoch, err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.Unhydrated {
		t.Fatal("the flag was pinned onto the replacement instance")
	}

	// …and a delayed write carrying the OLD instance id is a no-op rather than a
	// claim about the successor.
	if err := st.SetSandboxLeaseUnhydrated(ctx, scope, "pod-a", "sb-a", true); err != nil {
		t.Fatalf("late write: %v", err)
	}
	if got, _ := st.GetSandboxLease(ctx, scope); got.Unhydrated {
		t.Fatal("a late write about the old instance marked the successor unhydrated")
	}
}
