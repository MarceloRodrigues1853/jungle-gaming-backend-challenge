package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/jackc/pgx/v5"
)

// ClaimOutbox reserva eventos elegíveis sem bloquear outros publishers.
func (store *Store) ClaimOutbox(ctx context.Context, workerID string, now time.Time, lockDuration time.Duration, limit int) ([]application.PendingOutboxEvent, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("postgres store is not initialized")
	}
	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()
	rows, err := dbtx.Query(ctx, `WITH candidates AS (
		SELECT event_id FROM outbox_events
		WHERE published_at IS NULL AND next_attempt_at <= $1
		  AND (locked_until IS NULL OR locked_until <= $1)
		ORDER BY next_attempt_at, occurred_at, event_id
		FOR UPDATE SKIP LOCKED
		LIMIT $2
	)
	UPDATE outbox_events AS event
	SET locked_by = $3, locked_until = $4, attempts = event.attempts + 1
	FROM candidates
	WHERE event.event_id = candidates.event_id
	RETURNING event.event_id, event.aggregate_id, event.event_type, event.payload, event.attempts, event.occurred_at`,
		now, limit, workerID, now.Add(lockDuration))
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()
	var events []application.PendingOutboxEvent
	for rows.Next() {
		var event application.PendingOutboxEvent
		if err := rows.Scan(&event.ID, &event.AggregateID, &event.Type, &event.Payload, &event.Attempts, &event.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed outbox events: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return events, nil
}

// MarkOutboxPublished confirma somente uma reserva ainda pertencente ao worker.
func (store *Store) MarkOutboxPublished(ctx context.Context, eventID, workerID string, publishedAt time.Time) error {
	tag, err := store.pool.Exec(ctx, `UPDATE outbox_events
		SET published_at = $1, locked_by = NULL, locked_until = NULL, last_error = NULL
		WHERE event_id = $2 AND locked_by = $3 AND published_at IS NULL`, publishedAt, eventID, workerID)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("outbox event reservation is no longer owned by worker")
	}
	return nil
}

// RescheduleOutbox libera a reserva e registra backoff e erro sem perder o evento.
func (store *Store) RescheduleOutbox(ctx context.Context, eventID, workerID string, nextAttempt time.Time, lastError string) error {
	tag, err := store.pool.Exec(ctx, `UPDATE outbox_events
		SET next_attempt_at = $1, last_error = $2, locked_by = NULL, locked_until = NULL
		WHERE event_id = $3 AND locked_by = $4 AND published_at IS NULL`, nextAttempt, lastError, eventID, workerID)
	if err != nil {
		return fmt.Errorf("reschedule outbox: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("outbox event reservation is no longer owned by worker")
	}
	return nil
}
