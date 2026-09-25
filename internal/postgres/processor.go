package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ProcessWagerTransaction grava a transação, o saldo e o ledger no mesmo commit SQL.
// O lock fica restrito à carteira afetada, permitindo paralelismo entre carteiras distintas.
func (store *Store) ProcessWagerTransaction(ctx context.Context, transaction domain.WagerTransaction, reference *domain.WagerTransaction, ledgerEntryID string, now time.Time) (domain.WagerProcessingResult, error) {
	if store == nil || store.pool == nil {
		return domain.WagerProcessingResult{}, errors.New("postgres store is not initialized")
	}
	if transaction.Status() != domain.TransactionPending {
		return domain.WagerProcessingResult{}, fmt.Errorf("transaction must be pending before persistence")
	}

	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("begin financial transaction: %w", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	wallet, err := lockWallet(ctx, dbtx, transaction.WalletID(), transaction.PlayerID(), transaction.Money().Currency())
	if err != nil {
		return domain.WagerProcessingResult{}, err
	}
	initialWalletVersion := wallet.Version()
	if err := insertPendingTransaction(ctx, dbtx, transaction); err != nil {
		return domain.WagerProcessingResult{}, err
	}

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
	if err := dbtx.Commit(ctx); err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("commit financial transaction: %w", err)
	}
	return result, nil
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
func insertPendingTransaction(ctx context.Context, tx pgx.Tx, transaction domain.WagerTransaction) error {
	snapshot := transaction.Snapshot()
	source := "EXTERNAL"
	if snapshot.Kind == domain.TransactionOpening {
		source = "INTERNAL"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, source, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, currency, round_id, game_id, kind, amount_minor,
			reference_external_transaction_id, reference_transaction_id, status, failure_code,
			result_balance_minor, created_at, updated_at, processed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21
		)`,
		snapshot.ID, source, nullableText(snapshot.ProviderID), nullableText(snapshot.ExternalTransactionID),
		nullableText(snapshot.IdempotencyKey), nullableBytes(snapshot.PayloadHash), snapshot.WalletID,
		snapshot.PlayerID, snapshot.Money.Currency(), nullableText(snapshot.RoundID), nullableText(snapshot.GameID),
		string(snapshot.Kind), snapshot.Money.MinorUnits(), nullableText(snapshot.ReferenceExternalID),
		nullableText(snapshot.ReferenceID), string(snapshot.Status), nullableText(snapshot.FailureCode),
		nullableMoney(snapshot.ResultBalance, snapshot.HasResultBalance), snapshot.CreatedAt, snapshot.UpdatedAt,
		nullableTime(snapshot.ProcessedAt),
	)
	if err != nil {
		return fmt.Errorf("insert pending wager transaction: %w", err)
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

// walletUpdatedAt devolve o instante persistido da última mudança no agregado.
func walletUpdatedAt(wallet domain.Wallet) time.Time {
	return wallet.UpdatedAt()
}
