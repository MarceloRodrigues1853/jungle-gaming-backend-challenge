package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const FailureReferenceNotFound = "REFERENCE_NOT_FOUND"

// PendingReference representa uma reversão reservada por uma instância.
type PendingReference struct {
	TransactionID string
	Attempts      int
	ExpiresAt     time.Time
}

// PendingReferenceRepository mantém reserva, retry e conclusão no PostgreSQL.
type PendingReferenceRepository interface {
	ClaimPendingReferences(context.Context, string, time.Time, time.Duration, int) ([]PendingReference, error)
	ResolvePendingReference(context.Context, string, string, string, WagerEventIDs, time.Time) (bool, error)
	ReschedulePendingReference(context.Context, string, string, time.Time) error
	RejectPendingReference(context.Context, string, string, string, time.Time) error
}

// ReferenceWorker retoma operações cuja referência chegou fora de ordem.
type ReferenceWorker struct {
	repository  PendingReferenceRepository
	ids         IDGenerator
	logger      *slog.Logger
	workerID    string
	poll        time.Duration
	lock        time.Duration
	batchSize   int
	maxAttempts int
	clock       func() time.Time
}

func NewReferenceWorker(repository PendingReferenceRepository, ids IDGenerator, logger *slog.Logger, workerID string, poll, lock time.Duration, batchSize, maxAttempts int) (*ReferenceWorker, error) {
	if repository == nil || ids == nil || logger == nil || workerID == "" {
		return nil, errors.New("reference repository, id generator, logger, and worker id are required")
	}
	if poll <= 0 || lock <= 0 || batchSize <= 0 || maxAttempts <= 0 {
		return nil, errors.New("reference worker configuration must be positive")
	}
	return &ReferenceWorker{repository: repository, ids: ids, logger: logger, workerID: workerID,
		poll: poll, lock: lock, batchSize: batchSize, maxAttempts: maxAttempts, clock: time.Now}, nil
}

func (worker *ReferenceWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(worker.poll)
	defer ticker.Stop()
	for {
		if err := worker.ProcessBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
			worker.logger.Error("pending reference batch failed", "workerId", worker.workerID, "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (worker *ReferenceWorker) ProcessBatch(ctx context.Context) error {
	now := worker.clock().UTC()
	items, err := worker.repository.ClaimPendingReferences(ctx, worker.workerID, now, worker.lock, worker.batchSize)
	if err != nil {
		return fmt.Errorf("claim pending references: %w", err)
	}
	for _, item := range items {
		if !now.Before(item.ExpiresAt) || item.Attempts >= worker.maxAttempts {
			eventID, idErr := worker.ids.NewID()
			if idErr != nil {
				return idErr
			}
			if err := worker.repository.RejectPendingReference(ctx, item.TransactionID, worker.workerID, eventID, now); err != nil {
				return fmt.Errorf("reject expired reference %s: %w", item.TransactionID, err)
			}
			worker.logger.Warn("pending reference expired", "transactionId", item.TransactionID, "attempt", item.Attempts)
			continue
		}
		ledgerID, transactionEventID, balanceEventID, err := worker.nextIDs()
		if err != nil {
			return err
		}
		resolved, err := worker.repository.ResolvePendingReference(ctx, item.TransactionID, worker.workerID,
			ledgerID, WagerEventIDs{Transaction: transactionEventID, WalletBalance: balanceEventID}, now)
		if err != nil {
			return fmt.Errorf("resolve pending reference %s: %w", item.TransactionID, err)
		}
		if resolved {
			worker.logger.Info("pending reference resolved", "transactionId", item.TransactionID, "attempt", item.Attempts)
			continue
		}
		next := now.Add(referenceBackoff(item.Attempts))
		if next.After(item.ExpiresAt) {
			next = item.ExpiresAt
		}
		if err := worker.repository.ReschedulePendingReference(ctx, item.TransactionID, worker.workerID, next); err != nil {
			return fmt.Errorf("reschedule pending reference %s: %w", item.TransactionID, err)
		}
	}
	return nil
}

func (worker *ReferenceWorker) nextIDs() (string, string, string, error) {
	values := make([]string, 3)
	for index := range values {
		value, err := worker.ids.NewID()
		if err != nil {
			return "", "", "", err
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func referenceBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	return time.Second * time.Duration(1<<uint(attempt-1))
}
