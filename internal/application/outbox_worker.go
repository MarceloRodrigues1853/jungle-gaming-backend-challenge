package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// PendingOutboxEvent representa um registro reservado para uma tentativa de publicação.
type PendingOutboxEvent struct {
	ID, AggregateID, Type string
	Payload               []byte
	Attempts              int
	OccurredAt            time.Time
}

// OutboxMetrics recebe somente sinais operacionais, sem conhecer payloads.
type OutboxMetrics interface {
	ObserveOutboxRetry()
	ObserveOutboxLag(time.Duration)
}

// OutboxRepository coordena reservas concorrentes e confirmações duráveis.
type OutboxRepository interface {
	ClaimOutbox(context.Context, string, time.Time, time.Duration, int) ([]PendingOutboxEvent, error)
	MarkOutboxPublished(context.Context, string, string, time.Time) error
	RescheduleOutbox(context.Context, string, string, time.Time, string) error
}

// EventPublisher envia um snapshot sem conhecer o mecanismo de reserva PostgreSQL.
type EventPublisher interface {
	Publish(context.Context, PendingOutboxEvent) error
}

// OutboxWorker publica lotes pequenos e deixa falhas prontas para retry durável.
type OutboxWorker struct {
	repository   OutboxRepository
	publisher    EventPublisher
	logger       *slog.Logger
	workerID     string
	pollInterval time.Duration
	lockDuration time.Duration
	batchSize    int
	clock        func() time.Time
	metrics      OutboxMetrics
}

// NewOutboxWorker valida a configuração necessária ao loop de publicação.
func NewOutboxWorker(repository OutboxRepository, publisher EventPublisher, logger *slog.Logger, workerID string, pollInterval, lockDuration time.Duration, batchSize int, metrics ...OutboxMetrics) (*OutboxWorker, error) {
	if repository == nil || publisher == nil || logger == nil || workerID == "" {
		return nil, errors.New("outbox repository, publisher, logger, and worker id are required")
	}
	if pollInterval <= 0 || lockDuration <= 0 || batchSize <= 0 {
		return nil, errors.New("outbox timing and batch size must be positive")
	}
	worker := &OutboxWorker{repository: repository, publisher: publisher, logger: logger,
		workerID: workerID, pollInterval: pollInterval, lockDuration: lockDuration,
		batchSize: batchSize, clock: time.Now}
	if len(metrics) > 0 {
		worker.metrics = metrics[0]
	}
	return worker, nil
}

// Run continua até o contexto ser cancelado e respeita shutdown sem buscar novo trabalho.
func (worker *OutboxWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(worker.pollInterval)
	defer ticker.Stop()
	for {
		if err := worker.ProcessBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			worker.logger.Error("outbox batch failed", "workerId", worker.workerID, "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ProcessBatch executa uma rodada; fica público para testes e operação controlada.
func (worker *OutboxWorker) ProcessBatch(ctx context.Context) error {
	now := worker.clock().UTC()
	events, err := worker.repository.ClaimOutbox(ctx, worker.workerID, now, worker.lockDuration, worker.batchSize)
	if err != nil {
		return fmt.Errorf("claim outbox: %w", err)
	}
	oldestLag := time.Duration(0)
	for _, event := range events {
		if !event.OccurredAt.IsZero() && now.Sub(event.OccurredAt) > oldestLag {
			oldestLag = now.Sub(event.OccurredAt)
		}
	}
	if worker.metrics != nil {
		worker.metrics.ObserveOutboxLag(oldestLag)
	}
	for _, event := range events {
		if err := worker.publisher.Publish(ctx, event); err != nil {
			nextAttempt := now.Add(outboxBackoff(event.Attempts))
			if retryErr := worker.repository.RescheduleOutbox(ctx, event.ID, worker.workerID, nextAttempt, err.Error()); retryErr != nil {
				return fmt.Errorf("reschedule outbox event %s after publish error: %w", event.ID, retryErr)
			}
			worker.logger.Warn("outbox event rescheduled", "eventId", event.ID, "eventType", event.Type, "attempt", event.Attempts)
			if worker.metrics != nil {
				worker.metrics.ObserveOutboxRetry()
			}
			continue
		}
		if err := worker.repository.MarkOutboxPublished(ctx, event.ID, worker.workerID, worker.clock().UTC()); err != nil {
			return fmt.Errorf("mark outbox event %s published: %w", event.ID, err)
		}
		worker.logger.Info("outbox event published", "eventId", event.ID, "eventType", event.Type, "attempt", event.Attempts)
	}
	return nil
}

// outboxBackoff cresce exponencialmente e limita o intervalo a cinco minutos.
func outboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	return time.Second * time.Duration(1<<uint(attempt-1))
}
