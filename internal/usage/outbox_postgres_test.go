package usage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/google/uuid"
)

func TestSQLOutboxPostgres(t *testing.T) {
	dsn := os.Getenv("FASTAGENT_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set FASTAGENT_TEST_PG_DSN to run the Postgres outbox test")
	}
	ctx := context.Background()
	st, err := store.NewDBStore("postgres", dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	outbox := NewSQLOutbox(st.DB(), "postgres")
	id := "outbox-test-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(context.Background(), `DELETE FROM usage_outbox WHERE id = $1`, id)
	})

	if err := outbox.Enqueue(ctx, id, []byte(`{"reportId":"`+id+`"}`)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	pending, err := outbox.Pending(ctx, time.Now().Add(time.Second), 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if !containsOutboxID(pending, id) {
		t.Fatalf("pending does not contain %s", id)
	}

	if err := outbox.MarkRetry(ctx, id, 1, time.Now().Add(time.Hour), "temporary"); err != nil {
		t.Fatalf("mark retry: %v", err)
	}
	pending, err = outbox.Pending(ctx, time.Now(), 100)
	if err != nil {
		t.Fatalf("pending after retry: %v", err)
	}
	if containsOutboxID(pending, id) {
		t.Fatalf("future retry row %s is already pending", id)
	}

	if err := outbox.MarkSent(ctx, id); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	var status string
	if err := st.DB().QueryRowContext(ctx, `SELECT status FROM usage_outbox WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "sent" {
		t.Fatalf("status = %q, want sent", status)
	}

	if err := outbox.MarkRetry(ctx, id, 2, time.Now(), "late failure"); err != nil {
		t.Fatalf("late mark retry: %v", err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT status FROM usage_outbox WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("read status after late retry: %v", err)
	}
	if status != "sent" {
		t.Fatalf("status after late retry = %q, want sent", status)
	}
}

func containsOutboxID(entries []OutboxEntry, id string) bool {
	for _, entry := range entries {
		if entry.ID == id {
			return true
		}
	}
	return false
}
