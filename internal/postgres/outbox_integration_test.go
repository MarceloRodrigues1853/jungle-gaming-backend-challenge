package postgres

import (
	"context"
	"testing"
	"time"
)

// TestPostgresOutboxClaimOwnershipAndRecovery cobre disputa, confirmação e reserva expirada.
func TestPostgresOutboxClaimOwnershipAndRecovery(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	earliest := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	eventID := "it-claim-" + time.Now().Format("150405.000000000")
	_, err := pool.Exec(context.Background(), `INSERT INTO outbox_events
		(event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
		VALUES ($1, 'wallet-claim', 'TestEvent', '{}'::jsonb, $2, $2)`, eventID, earliest)
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.ClaimOutbox(context.Background(), "worker-a", now, time.Second, 1)
	if err != nil || len(first) != 1 || first[0].ID != eventID || first[0].Attempts != 1 {
		t.Fatalf("first claim = %#v, error %v", first, err)
	}
	if err := store.MarkOutboxPublished(context.Background(), eventID, "worker-b", now); err == nil {
		t.Fatal("a worker that does not own the reservation marked the event as published")
	}
	recovered, err := store.ClaimOutbox(context.Background(), "worker-b", now.Add(2*time.Second), time.Second, 1)
	if err != nil || len(recovered) != 1 || recovered[0].Attempts != 2 {
		t.Fatalf("recovered claim = %#v, error %v", recovered, err)
	}
	if err := store.MarkOutboxPublished(context.Background(), eventID, "worker-b", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var publishedAt *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT published_at FROM outbox_events WHERE event_id = $1`, eventID).Scan(&publishedAt); err != nil || publishedAt == nil {
		t.Fatalf("published_at = %v, error %v", publishedAt, err)
	}
}
