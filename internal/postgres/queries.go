package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (store *Store) ListWalletLedger(ctx context.Context, walletID, cursor string, limit int) (application.LedgerPage, bool, error) {
	if limit < 1 || limit > 100 {
		return application.LedgerPage{}, false, fmt.Errorf("%w: limit must be between 1 and 100", application.ErrInvalidLedgerQuery)
	}
	if _, exists, err := store.GetWallet(ctx, walletID); err != nil || !exists {
		return application.LedgerPage{}, exists, err
	}
	cursorTime, cursorID, err := decodeLedgerCursor(cursor)
	if err != nil {
		return application.LedgerPage{}, true, fmt.Errorf("%w: %v", application.ErrInvalidLedgerQuery, err)
	}
	rows, err := store.pool.Query(ctx, `SELECT id, transaction_id, direction, amount_minor,
		balance_before, balance_after, created_at, (SELECT currency FROM wallets WHERE id = $1)
		FROM wallet_ledger_entries
		WHERE wallet_id = $1 AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC LIMIT $4`, walletID, nullableCursorTime(cursorTime), cursorID, limit+1)
	if err != nil {
		return application.LedgerPage{}, true, fmt.Errorf("list wallet ledger: %w", err)
	}
	defer rows.Close()
	var items []application.LedgerItem
	for rows.Next() {
		var item application.LedgerItem
		var direction, currency string
		var amount, before, after int64
		if err := rows.Scan(&item.ID, &item.TransactionID, &direction, &amount, &before, &after, &item.CreatedAt, &currency); err != nil {
			return application.LedgerPage{}, true, err
		}
		item.WalletID, item.Direction = walletID, domain.LedgerDirection(direction)
		item.Money, err = domain.MoneyFromMinorUnits(amount, currency)
		if err != nil {
			return application.LedgerPage{}, true, err
		}
		item.BalanceBefore, err = domain.MoneyFromMinorUnits(before, currency)
		if err != nil {
			return application.LedgerPage{}, true, err
		}
		item.BalanceAfter, err = domain.MoneyFromMinorUnits(after, currency)
		if err != nil {
			return application.LedgerPage{}, true, err
		}
		items = append(items, item)
	}
	page := application.LedgerPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeLedgerCursor(last.CreatedAt, last.ID)
	}
	return page, true, rows.Err()
}

func (store *Store) GetProviderTransactionByID(ctx context.Context, providerID, transactionID string) (application.TransactionView, bool, error) {
	return store.transactionView(ctx, `provider_id = $1 AND id = $2`, providerID, transactionID)
}

func (store *Store) GetProviderTransactionByExternalID(ctx context.Context, providerID, externalID string) (application.TransactionView, bool, error) {
	return store.transactionView(ctx, `provider_id = $1 AND external_transaction_id = $2`, providerID, externalID)
}

func (store *Store) transactionView(ctx context.Context, clause string, first, second string) (application.TransactionView, bool, error) {
	var view application.TransactionView
	var kind, status, currency string
	var amount int64
	var referenceExternalID, referenceID, failureCode *string
	var resultBalance *int64
	err := store.pool.QueryRow(ctx, `SELECT id, provider_id, external_transaction_id, wallet_id,
		player_id, round_id, game_id, kind, amount_minor, currency,
		reference_external_transaction_id, reference_transaction_id, status, failure_code,
		result_balance_minor, created_at, updated_at FROM wager_transactions
		WHERE source = 'EXTERNAL' AND `+clause, first, second).Scan(
		&view.ID, &view.ProviderID, &view.ExternalTransactionID, &view.WalletID, &view.PlayerID,
		&view.RoundID, &view.GameID, &kind, &amount, &currency, &referenceExternalID,
		&referenceID, &status, &failureCode, &resultBalance, &view.CreatedAt, &view.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TransactionView{}, false, nil
	}
	if err != nil {
		return application.TransactionView{}, false, err
	}
	view.Kind, view.Status = domain.TransactionKind(kind), domain.TransactionStatus(status)
	view.ReferenceExternalID, view.ReferenceID = dereferenceString(referenceExternalID), dereferenceString(referenceID)
	view.FailureCode = dereferenceString(failureCode)
	view.Money, err = domain.MoneyFromMinorUnits(amount, currency)
	if err != nil {
		return application.TransactionView{}, false, err
	}
	if resultBalance != nil {
		view.ResultBalance, err = domain.MoneyFromMinorUnits(*resultBalance, currency)
		view.HasResultBalance = err == nil
		if err != nil {
			return application.TransactionView{}, false, err
		}
	}
	return view, true, nil
}

func (store *Store) ReconcileWallet(ctx context.Context, walletID string) (application.ReconciliationResult, bool, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return application.ReconciliationResult{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var stored, credits, debits, count int64
	var currency string
	err = tx.QueryRow(ctx, `SELECT balance, currency FROM wallets WHERE id = $1`, walletID).Scan(&stored, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReconciliationResult{}, false, nil
	}
	if err != nil {
		return application.ReconciliationResult{}, false, err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_minor) FILTER (WHERE direction='CREDIT'),0),
		COALESCE(sum(amount_minor) FILTER (WHERE direction='DEBIT'),0), count(*)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&credits, &debits, &count); err != nil {
		return application.ReconciliationResult{}, false, err
	}
	calculated := credits - debits
	storedMoney, err := domain.MoneyFromMinorUnits(stored, currency)
	if err != nil {
		return application.ReconciliationResult{}, false, err
	}
	calculatedMoney, err := domain.MoneyFromMinorUnits(calculated, currency)
	if err != nil {
		return application.ReconciliationResult{}, false, err
	}
	difference, err := storedMoney.Subtract(calculatedMoney)
	if err != nil {
		return application.ReconciliationResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ReconciliationResult{}, false, err
	}
	return application.ReconciliationResult{WalletID: walletID, StoredBalance: storedMoney,
		CalculatedBalance: calculatedMoney, Difference: difference, Consistent: stored == calculated, CheckedEntries: count}, true, nil
}

func encodeLedgerCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "\n" + id))
}

func decodeLedgerCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", errors.New("invalid ledger cursor")
	}
	parts := strings.SplitN(string(decoded), "\n", 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", errors.New("invalid ledger cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", errors.New("invalid ledger cursor")
	}
	return createdAt, parts[1], nil
}

func nullableCursorTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
