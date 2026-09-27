package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
)

// persistedIdentity reúne os campos necessários para reconhecer uma repetição legítima.
type persistedIdentity struct {
	id, externalID, idempotencyKey, status string
	payloadHash                            []byte
	failureCode                            *string
	resultBalance                          *int64
}

// ProcessWagerTransaction grava a transação, o saldo e o ledger no mesmo commit SQL.
// O lock fica restrito à carteira afetada, permitindo paralelismo entre carteiras distintas.
func (store *Store) ProcessWagerTransaction(ctx context.Context, transaction domain.WagerTransaction, reference *domain.WagerTransaction, ledgerEntryID string, eventIDs application.WagerEventIDs, delivery *application.InboxDelivery, now time.Time) (domain.WagerProcessingResult, error) {
	if store == nil || store.pool == nil {
		return domain.WagerProcessingResult{}, errors.New("postgres store is not initialized")
	}
	if transaction.Status() != domain.TransactionPending && transaction.Status() != domain.TransactionPendingReference {
		return domain.WagerProcessingResult{}, fmt.Errorf("transaction must be pending or pending reference before persistence")
	}

	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("begin financial transaction: %w", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	if delivery != nil {
		if err := registerInboxDelivery(ctx, dbtx, *delivery); err != nil {
			return domain.WagerProcessingResult{}, err
		}
	}

	// Operações que podem movimentar saldo bloqueiam a carteira antes de inserir
	// a transação. Essa ordem única evita o ciclo entre a FK da transação e o
	// SELECT FOR UPDATE quando processos independentes disputam a mesma carteira.
	var lockedWallet domain.Wallet
	if transaction.Status() != domain.TransactionPendingReference {
		lockedWallet, err = lockWallet(ctx, dbtx, transaction.WalletID(), transaction.PlayerID(), transaction.Money().Currency())
		if err != nil {
			return domain.WagerProcessingResult{}, err
		}
	}

	inserted, err := insertPendingTransaction(ctx, dbtx, transaction)
	if err != nil {
		return domain.WagerProcessingResult{}, err
	}
	if !inserted {
		result, err := loadIdempotentReplay(ctx, dbtx, transaction)
		if err != nil {
			return domain.WagerProcessingResult{}, err
		}
		if delivery != nil {
			if err := completeInboxDelivery(ctx, dbtx, *delivery, now); err != nil {
				return domain.WagerProcessingResult{}, err
			}
		}
		if err := dbtx.Commit(ctx); err != nil {
			return domain.WagerProcessingResult{}, fmt.Errorf("finish idempotent replay: %w", err)
		}
		return result, nil
	}
	if transaction.Status() == domain.TransactionPendingReference {
		result := domain.WagerProcessingResult{TransactionID: transaction.ID(), Status: domain.TransactionPendingReference}
		events, err := application.BuildWagerOutboxEvents(transaction, result, eventIDs, now)
		if err != nil {
			return domain.WagerProcessingResult{}, fmt.Errorf("build pending reference event: %w", err)
		}
		if err := insertOutboxEvents(ctx, dbtx, events); err != nil {
			return domain.WagerProcessingResult{}, err
		}
		if delivery != nil {
			if err := completeInboxDelivery(ctx, dbtx, *delivery, now); err != nil {
				return domain.WagerProcessingResult{}, err
			}
		}
		if err := dbtx.Commit(ctx); err != nil {
			return domain.WagerProcessingResult{}, fmt.Errorf("commit pending reference: %w", err)
		}
		return result, nil
	}

	wallet := lockedWallet
	initialWalletVersion := wallet.Version()

	result, err := domain.ProcessWagerTransaction(&wallet, &transaction, reference, ledgerEntryID, now)
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("process transaction in domain: %w", err)
	}
	if result.WalletVersion != wallet.Version() {
		return domain.WagerProcessingResult{}, errors.New("domain result wallet version is inconsistent")
	}
	if wallet.Version() != initialWalletVersion {
		if err := persistWallet(ctx, dbtx, wallet, initialWalletVersion); err != nil {
			return domain.WagerProcessingResult{}, err
		}
	}
	if result.LedgerEntry != nil {
		if err := insertLedgerEntry(ctx, dbtx, *result.LedgerEntry); err != nil {
			return domain.WagerProcessingResult{}, err
		}
	}
	if err := persistTransactionResult(ctx, dbtx, transaction); err != nil {
		return domain.WagerProcessingResult{}, err
	}
	events, err := application.BuildWagerOutboxEvents(transaction, result, eventIDs, now)
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("build wager outbox events: %w", err)
	}
	if err := insertOutboxEvents(ctx, dbtx, events); err != nil {
		return domain.WagerProcessingResult{}, err
	}
	if delivery != nil {
		if err := completeInboxDelivery(ctx, dbtx, *delivery, now); err != nil {
			return domain.WagerProcessingResult{}, err
		}
	}
	if err := dbtx.Commit(ctx); err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("commit financial transaction: %w", err)
	}
	return result, nil
}

// registerInboxDelivery cria a identidade durável ou valida uma reentrega do
// mesmo conteúdo. O lock serializa entregas concorrentes do mesmo messageId.
func registerInboxDelivery(ctx context.Context, tx pgx.Tx, delivery application.InboxDelivery) error {
	if _, err := tx.Exec(ctx, `INSERT INTO inbox_messages
		(consumer_name, message_id, payload_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		delivery.ConsumerName, delivery.MessageID, delivery.PayloadHash[:], delivery.ReceivedAt.UTC()); err != nil {
		return fmt.Errorf("insert inbox message: %w", err)
	}
	var persistedHash []byte
	if err := tx.QueryRow(ctx, `SELECT payload_hash FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2 FOR UPDATE`,
		delivery.ConsumerName, delivery.MessageID).Scan(&persistedHash); err != nil {
		return fmt.Errorf("lock inbox message: %w", err)
	}
	if !bytes.Equal(persistedHash, delivery.PayloadHash[:]) {
		return fmt.Errorf("%w: message id was reused with different content", application.ErrIdempotencyConflict)
	}
	return nil
}

func completeInboxDelivery(ctx context.Context, tx pgx.Tx, delivery application.InboxDelivery, completedAt time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE inbox_messages SET completed_at = COALESCE(completed_at, $1)
		WHERE consumer_name = $2 AND message_id = $3 AND payload_hash = $4`,
		completedAt.UTC(), delivery.ConsumerName, delivery.MessageID, delivery.PayloadHash[:])
	if err != nil {
		return fmt.Errorf("complete inbox message: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("inbox message was not completed")
	}
	return nil
}

// insertOutboxEvents grava snapshots que só ficarão visíveis após o commit financeiro.
func insertOutboxEvents(ctx context.Context, tx pgx.Tx, events []application.OutboxEvent) error {
	for _, event := range events {
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events
			(event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $5)`, event.ID, event.AggregateID, event.Type, event.Payload, event.OccurredAt); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

// lockWallet bloqueia apenas a linha da carteira e reidrata seu estado persistido.
func lockWallet(ctx context.Context, tx pgx.Tx, walletID, playerID, currency string) (domain.Wallet, error) {
	var balanceMinor, version int64
	var createdAt, updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT balance, version, created_at, updated_at
		FROM wallets
		WHERE id = $1 AND player_id = $2 AND currency = $3
		FOR UPDATE`, walletID, playerID, currency,
	).Scan(&balanceMinor, &version, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, fmt.Errorf("%w: %s", ErrWalletNotFound, walletID)
	}
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("lock wallet: %w", err)
	}
	balance, err := domain.MoneyFromMinorUnits(balanceMinor, currency)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("rehydrate wallet balance: %w", err)
	}
	wallet, err := domain.RehydrateWallet(walletID, playerID, balance, version, createdAt, updatedAt)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("rehydrate wallet: %w", err)
	}
	return wallet, nil
}

// insertPendingTransaction grava o estado aceito antes de persistir seu resultado final na mesma transação SQL.
func insertPendingTransaction(ctx context.Context, tx pgx.Tx, transaction domain.WagerTransaction) (bool, error) {
	snapshot := transaction.Snapshot()
	source := "EXTERNAL"
	if snapshot.Kind == domain.TransactionOpening {
		source = "INTERNAL"
	}
	query := `
		INSERT INTO wager_transactions (
			id, source, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, currency, round_id, game_id, kind, amount_minor,
			reference_external_transaction_id, reference_transaction_id, status, failure_code,
			result_balance_minor, created_at, updated_at, processed_at,
			next_reference_attempt_at, reference_expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23
		)`
	args := []any{
		snapshot.ID, source, nullableText(snapshot.ProviderID), nullableText(snapshot.ExternalTransactionID),
		nullableText(snapshot.IdempotencyKey), nullableBytes(snapshot.PayloadHash), snapshot.WalletID,
		snapshot.PlayerID, snapshot.Money.Currency(), nullableText(snapshot.RoundID), nullableText(snapshot.GameID),
		string(snapshot.Kind), snapshot.Money.MinorUnits(), nullableText(snapshot.ReferenceExternalID),
		nullableText(snapshot.ReferenceID), string(snapshot.Status), nullableText(snapshot.FailureCode),
		nullableMoney(snapshot.ResultBalance, snapshot.HasResultBalance), snapshot.CreatedAt, snapshot.UpdatedAt,
		nullableTime(snapshot.ProcessedAt), nullablePendingTime(snapshot.Status, snapshot.UpdatedAt),
		nullablePendingTime(snapshot.Status, snapshot.UpdatedAt.Add(24*time.Hour)),
	}
	if source == "EXTERNAL" {
		var insertedID string
		err := tx.QueryRow(ctx, query+` ON CONFLICT DO NOTHING RETURNING id`, args...).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("insert pending wager transaction: %w", err)
		}
		return true, nil
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return false, fmt.Errorf("insert pending wager transaction: %w", err)
	}
	return true, nil
}

// loadIdempotentReplay devolve o resultado persistido somente quando identidades e conteúdo coincidem.
func loadIdempotentReplay(ctx context.Context, tx pgx.Tx, candidate domain.WagerTransaction) (domain.WagerProcessingResult, error) {
	snapshot := candidate.Snapshot()
	rows, err := tx.Query(ctx, `
		SELECT id, external_transaction_id, idempotency_key, payload_hash,
			status, failure_code, result_balance_minor
		FROM wager_transactions
		WHERE source = 'EXTERNAL' AND provider_id = $1
			AND (external_transaction_id = $2 OR idempotency_key = $3)
		ORDER BY id
		LIMIT 2
		FOR UPDATE`, snapshot.ProviderID, snapshot.ExternalTransactionID, snapshot.IdempotencyKey)
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("lookup idempotent transaction: %w", err)
	}
	defer rows.Close()

	var matches []persistedIdentity
	for rows.Next() {
		var item persistedIdentity
		if err := rows.Scan(&item.id, &item.externalID, &item.idempotencyKey, &item.payloadHash,
			&item.status, &item.failureCode, &item.resultBalance); err != nil {
			return domain.WagerProcessingResult{}, fmt.Errorf("scan idempotent transaction: %w", err)
		}
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("iterate idempotent transactions: %w", err)
	}
	if len(matches) != 1 {
		return domain.WagerProcessingResult{}, fmt.Errorf("%w: external identity does not resolve to one transaction", ErrIdempotencyConflict)
	}

	existing := matches[0]
	if err := validateReplayIdentity(snapshot, existing); err != nil {
		return domain.WagerProcessingResult{}, err
	}

	result := domain.WagerProcessingResult{
		TransactionID:    existing.id,
		Status:           domain.TransactionStatus(existing.status),
		IdempotentReplay: true,
	}
	if existing.failureCode != nil {
		result.FailureCode = *existing.failureCode
	}
	if existing.resultBalance != nil {
		result.Balance, err = domain.MoneyFromMinorUnits(*existing.resultBalance, snapshot.Money.Currency())
		if err != nil {
			return domain.WagerProcessingResult{}, fmt.Errorf("restore replay result balance: %w", err)
		}
		result.HasBalance = true
	}
	return result, nil
}

// validateReplayIdentity impede reutilizar um resultado quando chave, ID externo ou conteúdo mudam.
func validateReplayIdentity(candidate domain.WagerTransactionSnapshot, existing persistedIdentity) error {
	if existing.externalID != candidate.ExternalTransactionID || existing.idempotencyKey != candidate.IdempotencyKey || !bytes.Equal(existing.payloadHash, candidate.PayloadHash) {
		return fmt.Errorf("%w: key, external id, or payload differs", ErrIdempotencyConflict)
	}
	return nil
}

// persistWallet grava saldo e versão usando também uma condição de versão como proteção adicional.
func persistWallet(ctx context.Context, tx pgx.Tx, wallet domain.Wallet, previousVersion int64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE wallets
		SET balance = $1, version = $2, updated_at = $3
		WHERE id = $4 AND version = $5`,
		wallet.Balance().MinorUnits(), wallet.Version(), walletUpdatedAt(wallet), wallet.ID(), previousVersion,
	)
	if err != nil {
		return fmt.Errorf("persist wallet balance: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("wallet version changed while holding row lock")
	}
	return nil
}

// persistTransactionResult atualiza o estado final da operação antes do commit financeiro.
func persistTransactionResult(ctx context.Context, tx pgx.Tx, transaction domain.WagerTransaction) error {
	snapshot := transaction.Snapshot()
	tag, err := tx.Exec(ctx, `
		UPDATE wager_transactions
		SET reference_transaction_id = $1, status = $2, failure_code = $3,
			result_balance_minor = $4, updated_at = $5, processed_at = $6
		WHERE id = $7 AND status = 'PENDING'`,
		nullableText(snapshot.ReferenceID), string(snapshot.Status), nullableText(snapshot.FailureCode),
		nullableMoney(snapshot.ResultBalance, snapshot.HasResultBalance), snapshot.UpdatedAt,
		nullableTime(snapshot.ProcessedAt), snapshot.ID,
	)
	if err != nil {
		return fmt.Errorf("persist transaction result: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("pending transaction was not updated")
	}
	return nil
}

// insertLedgerEntry acrescenta o lançamento imutável junto com o novo saldo.
func insertLedgerEntry(ctx context.Context, tx pgx.Tx, entry domain.WalletLedgerEntry) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor,
			balance_before, balance_after, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.ID, entry.WalletID, entry.TransactionID, string(entry.Direction), entry.Money.MinorUnits(),
		entry.BalanceBefore.MinorUnits(), entry.BalanceAfter.MinorUnits(), entry.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert wallet ledger entry: %w", err)
	}
	return nil
}

// nullableText converte strings opcionais para NULL sem gravar valores vazios.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullableBytes converte um hash ausente, como no OPENING interno, para NULL.
func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

// nullableMoney preserva a ausência de saldo de resultado em operações não concluídas.
func nullableMoney(value domain.Money, present bool) any {
	if !present {
		return nil
	}
	return value.MinorUnits()
}

// nullableTime grava processed_at apenas para uma conclusão financeira bem-sucedida.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullablePendingTime(status domain.TransactionStatus, value time.Time) any {
	if status != domain.TransactionPendingReference {
		return nil
	}
	return value
}

// walletUpdatedAt devolve o instante persistido da última mudança no agregado.
func walletUpdatedAt(wallet domain.Wallet) time.Time {
	return wallet.UpdatedAt()
}
