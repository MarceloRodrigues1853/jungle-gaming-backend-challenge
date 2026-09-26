package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// CreateWallet confirma carteira e artefatos da abertura em uma única transação SQL.
func (store *Store) CreateWallet(ctx context.Context, wallet domain.Wallet, opening *domain.WagerTransaction, ledger *domain.WalletLedgerEntry, events []application.OutboxEvent) error {
	if store == nil || store.pool == nil {
		return errors.New("postgres store is not initialized")
	}
	dbtx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin wallet opening: %w", err)
	}
	defer func() { _ = dbtx.Rollback(ctx) }()

	_, err = dbtx.Exec(ctx, `INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, wallet.ID(), wallet.PlayerID(), wallet.Balance().Currency(),
		wallet.Balance().MinorUnits(), wallet.Version(), wallet.CreatedAt(), wallet.UpdatedAt())
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23505" {
			return fmt.Errorf("%w: player and currency already have a wallet", application.ErrWalletConflict)
		}
		return fmt.Errorf("insert wallet: %w", err)
	}
	if opening != nil {
		if ledger == nil || len(events) != 2 {
			return errors.New("positive opening requires ledger and two outbox events")
		}
		if _, err := insertPendingTransaction(ctx, dbtx, *opening); err != nil {
			return err
		}
		if err := insertLedgerEntry(ctx, dbtx, *ledger); err != nil {
			return err
		}
		if err := insertOutboxEvents(ctx, dbtx, events); err != nil {
			return err
		}
	} else if ledger != nil || len(events) != 0 {
		return errors.New("zero opening cannot contain financial artifacts")
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("commit wallet opening: %w", err)
	}
	return nil
}

// GetWallet reidrata a carteira sem executar nenhuma movimentação de domínio.
func (store *Store) GetWallet(ctx context.Context, walletID string) (domain.Wallet, bool, error) {
	if store == nil || store.pool == nil {
		return domain.Wallet{}, false, errors.New("postgres store is not initialized")
	}
	var playerID, currency string
	var balanceMinor, version int64
	var createdAt, updatedAt time.Time
	err := store.pool.QueryRow(ctx, `SELECT player_id, currency, balance, version, created_at, updated_at
		FROM wallets WHERE id = $1`, walletID).Scan(&playerID, &currency, &balanceMinor, &version, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Wallet{}, false, nil
	}
	if err != nil {
		return domain.Wallet{}, false, fmt.Errorf("get wallet: %w", err)
	}
	balance, err := domain.MoneyFromMinorUnits(balanceMinor, currency)
	if err != nil {
		return domain.Wallet{}, false, fmt.Errorf("restore wallet balance: %w", err)
	}
	wallet, err := domain.RehydrateWallet(walletID, playerID, balance, version, createdAt, updatedAt)
	if err != nil {
		return domain.Wallet{}, false, fmt.Errorf("rehydrate wallet: %w", err)
	}
	return wallet, true, nil
}
