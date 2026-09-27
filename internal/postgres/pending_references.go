package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ClaimPendingReferences reserva operações elegíveis sem bloquear outras
// instâncias que estejam buscando trabalho ao mesmo tempo.
func (store *Store) ClaimPendingReferences(ctx context.Context, workerID string, now time.Time, lockDuration time.Duration, limit int) ([]application.PendingReference, error) {
	rows, err := store.pool.Query(ctx, `WITH candidates AS (
		SELECT id FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE'
		  AND next_reference_attempt_at <= $1
		  AND (reference_locked_until IS NULL OR reference_locked_until <= $1)
		ORDER BY next_reference_attempt_at, created_at, id
		LIMIT $2 FOR UPDATE SKIP LOCKED
	)
	UPDATE wager_transactions AS wager
	SET reference_locked_by = $3, reference_locked_until = $1 + $4::interval,
		reference_attempts = reference_attempts + 1
	FROM candidates WHERE wager.id = candidates.id
	RETURNING wager.id, wager.reference_attempts, wager.reference_expires_at`,
		now.UTC(), limit, workerID, lockDuration.String())
	if err != nil {
		return nil, fmt.Errorf("claim pending references: %w", err)
	}
	defer rows.Close()
	var items []application.PendingReference
	for rows.Next() {
		var item application.PendingReference
		if err := rows.Scan(&item.TransactionID, &item.Attempts, &item.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan pending reference: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ResolvePendingReference confirma a referência, o movimento e os eventos em
// uma única transação. False significa que a referência ainda não está pronta.
func (store *Store) ResolvePendingReference(ctx context.Context, transactionID, workerID, ledgerID string, eventIDs application.WagerEventIDs, now time.Time) (bool, error) {
	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	pending, exists, err := loadPendingReference(ctx, dbtx, transactionID, workerID)
	if err != nil || !exists {
		return false, err
	}
	reference, found, err := findTransactionWithQuery(ctx, dbtx, pending.ProviderID(), pending.ReferenceExternalID(), true)
	if err != nil {
		return false, err
	}
	if !found || reference.Status() != domain.TransactionProcessed {
		return false, nil
	}
	if err := pending.ResolveReference(reference, now); err != nil {
		return false, fmt.Errorf("resolve transaction reference: %w", err)
	}
	wallet, err := lockWallet(ctx, dbtx, pending.WalletID(), pending.PlayerID(), pending.Money().Currency())
	if err != nil {
		return false, err
	}
	previousVersion := wallet.Version()
	result, duplicateReversal, err := rejectAlreadyReversed(ctx, dbtx, &pending, &reference, wallet.Version(), now)
	if err != nil {
		return false, err
	}
	if !duplicateReversal {
		result, err = domain.ProcessWagerTransaction(&wallet, &pending, &reference, ledgerID, now)
		if err != nil {
			return false, err
		}
	}
	if wallet.Version() != previousVersion {
		if err := persistWallet(ctx, dbtx, wallet, previousVersion); err != nil {
			return false, err
		}
	}
	if result.LedgerEntry != nil {
		if err := insertLedgerEntry(ctx, dbtx, *result.LedgerEntry); err != nil {
			return false, err
		}
	}
	if err := finishPendingReference(ctx, dbtx, pending, workerID); err != nil {
		return false, err
	}
	events, err := application.BuildWagerOutboxEvents(pending, result, eventIDs, now)
	if err != nil {
		return false, err
	}
	if err := insertOutboxEvents(ctx, dbtx, events); err != nil {
		return false, err
	}
	if err := dbtx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit resolved reference: %w", err)
	}
	return true, nil
}

func (store *Store) ReschedulePendingReference(ctx context.Context, transactionID, workerID string, next time.Time) error {
	tag, err := store.pool.Exec(ctx, `UPDATE wager_transactions
		SET next_reference_attempt_at = $1, reference_locked_by = NULL, reference_locked_until = NULL
		WHERE id = $2 AND status = 'PENDING_REFERENCE' AND reference_locked_by = $3`, next.UTC(), transactionID, workerID)
	if err != nil {
		return fmt.Errorf("reschedule pending reference: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("pending reference reservation was lost")
	}
	return nil
}

func (store *Store) RejectPendingReference(ctx context.Context, transactionID, workerID, eventID string, now time.Time) error {
	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = dbtx.Rollback(ctx) }()
	pending, exists, err := loadPendingReference(ctx, dbtx, transactionID, workerID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("pending reference reservation was lost")
	}
	if err := pending.MarkRejected(application.FailureReferenceNotFound, now); err != nil {
		return err
	}
	if err := finishPendingReference(ctx, dbtx, pending, workerID); err != nil {
		return err
	}
	result := domain.WagerProcessingResult{TransactionID: pending.ID(), Status: domain.TransactionRejected,
		FailureCode: application.FailureReferenceNotFound}
	events, err := application.BuildWagerOutboxEvents(pending, result, application.WagerEventIDs{Transaction: eventID}, now)
	if err != nil {
		return err
	}
	if err := insertOutboxEvents(ctx, dbtx, events); err != nil {
		return err
	}
	return dbtx.Commit(ctx)
}

func loadPendingReference(ctx context.Context, tx pgx.Tx, transactionID, workerID string) (domain.WagerTransaction, bool, error) {
	return findTransactionByClause(ctx, tx, `id = $1 AND status = 'PENDING_REFERENCE' AND reference_locked_by = $2`, transactionID, workerID)
}

func finishPendingReference(ctx context.Context, tx pgx.Tx, transaction domain.WagerTransaction, workerID string) error {
	snapshot := transaction.Snapshot()
	tag, err := tx.Exec(ctx, `UPDATE wager_transactions SET
		reference_transaction_id = $1, status = $2, failure_code = $3,
		result_balance_minor = $4, updated_at = $5, processed_at = $6,
		next_reference_attempt_at = NULL, reference_expires_at = NULL,
		reference_locked_by = NULL, reference_locked_until = NULL
		WHERE id = $7 AND status = 'PENDING_REFERENCE' AND reference_locked_by = $8`,
		nullableText(snapshot.ReferenceID), string(snapshot.Status), nullableText(snapshot.FailureCode),
		nullableMoney(snapshot.ResultBalance, snapshot.HasResultBalance), snapshot.UpdatedAt,
		nullableTime(snapshot.ProcessedAt), snapshot.ID, workerID)
	if err != nil {
		return fmt.Errorf("finish pending reference: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("pending reference reservation was lost")
	}
	return nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findTransactionWithQuery(ctx context.Context, queryer queryRower, providerID, externalID string, lock bool) (domain.WagerTransaction, bool, error) {
	clause := "source = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2"
	if lock {
		clause += " FOR UPDATE"
	}
	return findTransactionByClause(ctx, queryer, clause, providerID, externalID)
}

func findTransactionByClause(ctx context.Context, queryer queryRower, clause string, args ...any) (domain.WagerTransaction, bool, error) {
	var snapshot domain.WagerTransactionSnapshot
	var payloadHash []byte
	var amountMinor int64
	var currency, kind, status string
	var referenceExternalID, referenceID, failureCode *string
	var resultBalanceMinor *int64
	var processedAt *time.Time
	err := queryer.QueryRow(ctx, `SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
		wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
		reference_external_transaction_id, reference_transaction_id, status,
		failure_code, result_balance_minor, created_at, updated_at, processed_at
		FROM wager_transactions WHERE `+clause, args...).Scan(
		&snapshot.ID, &snapshot.ProviderID, &snapshot.ExternalTransactionID, &snapshot.IdempotencyKey,
		&payloadHash, &snapshot.WalletID, &snapshot.PlayerID, &snapshot.RoundID, &snapshot.GameID,
		&kind, &amountMinor, &currency, &referenceExternalID, &referenceID, &status,
		&failureCode, &resultBalanceMinor, &snapshot.CreatedAt, &snapshot.UpdatedAt, &processedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WagerTransaction{}, false, nil
	}
	if err != nil {
		return domain.WagerTransaction{}, false, fmt.Errorf("load wager transaction: %w", err)
	}
	money, err := domain.MoneyFromMinorUnits(amountMinor, currency)
	if err != nil {
		return domain.WagerTransaction{}, false, err
	}
	snapshot.PayloadHash, snapshot.Kind, snapshot.Money = payloadHash, domain.TransactionKind(kind), money
	snapshot.ReferenceExternalID, snapshot.ReferenceID = dereferenceString(referenceExternalID), dereferenceString(referenceID)
	snapshot.Status, snapshot.FailureCode = domain.TransactionStatus(status), dereferenceString(failureCode)
	if resultBalanceMinor != nil {
		snapshot.ResultBalance, err = domain.MoneyFromMinorUnits(*resultBalanceMinor, currency)
		if err != nil {
			return domain.WagerTransaction{}, false, err
		}
		snapshot.HasResultBalance = true
	}
	if processedAt != nil {
		snapshot.ProcessedAt = *processedAt
	}
	transaction, err := domain.RehydrateWagerTransaction(snapshot)
	if err != nil {
		return domain.WagerTransaction{}, false, err
	}
	return transaction, true, nil
}
