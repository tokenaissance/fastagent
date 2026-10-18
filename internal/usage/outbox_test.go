package usage

import (
	"context"
	"testing"
	"time"
)

func TestMemOutboxDoesNotRevertSentRows(t *testing.T) {
	ctx := context.Background()
	outbox := NewMemOutbox()
	if err := outbox.Enqueue(ctx, "report-1", []byte(`{"reportId":"report-1"}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := outbox.MarkSent(ctx, "report-1"); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := outbox.MarkRetry(ctx, "report-1", 1, time.Now(), "late failure"); err != nil {
		t.Fatalf("mark retry: %v", err)
	}
	pending, err := outbox.Pending(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want no rows after a late failure", pending)
	}
}
