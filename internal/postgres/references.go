package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5"
)

// FindProviderTransaction localiza uma operação pela identidade composta do provedor.
// O booleano diferencia ausência legítima de indisponibilidade ou corrupção dos dados.
func (store *Store) FindProviderTransaction(ctx context.Context, providerID, externalID string) (domain.WagerTransaction, bool, error) {
	if store == nil || store.pool == nil {
		return domain.WagerTransaction{}, false, errors.New("postgres store is not initialized")
	}

	var snapshot domain.WagerTransactionSnapshot
	var payloadHash []byte
	var amountMinor int64
	var currency, kind, status string
	var referenceExternalID, referenceID, failureCode *string
	var resultBalanceMinor *int64
	var processedAt *time.Time
	err := store.pool.QueryRow(ctx, `
		SELECT id, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, amount_minor, currency,
			reference_external_transaction_id, reference_transaction_id, status,
			failure_code, result_balance_minor, created_at, updated_at, processed_at
		FROM wager_transactions
		WHERE source = 'EXTERNAL' AND provider_id = $1 AND external_transaction_id = $2`,
		providerID, externalID,
	).Scan(
		&snapshot.ID, &snapshot.ProviderID, &snapshot.ExternalTransactionID,
		&snapshot.IdempotencyKey, &payloadHash, &snapshot.WalletID, &snapshot.PlayerID,
		&snapshot.RoundID, &snapshot.GameID, &kind, &amountMinor, &currency,
		&referenceExternalID, &referenceID, &status, &failureCode, &resultBalanceMinor,
		&snapshot.CreatedAt, &snapshot.UpdatedAt, &processedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WagerTransaction{}, false, nil
	}
	if err != nil {
		return domain.WagerTransaction{}, false, fmt.Errorf("find provider transaction: %w", err)
	}

	money, err := domain.MoneyFromMinorUnits(amountMinor, currency)
	if err != nil {
		return domain.WagerTransaction{}, false, fmt.Errorf("rehydrate referenced transaction money: %w", err)
	}
	snapshot.PayloadHash = payloadHash
	snapshot.Kind = domain.TransactionKind(kind)
	snapshot.Money = money
	snapshot.ReferenceExternalID = dereferenceString(referenceExternalID)
	snapshot.ReferenceID = dereferenceString(referenceID)
	snapshot.Status = domain.TransactionStatus(status)
	snapshot.FailureCode = dereferenceString(failureCode)
	if resultBalanceMinor != nil {
		snapshot.ResultBalance, err = domain.MoneyFromMinorUnits(*resultBalanceMinor, currency)
		if err != nil {
			return domain.WagerTransaction{}, false, fmt.Errorf("rehydrate referenced transaction result: %w", err)
		}
		snapshot.HasResultBalance = true
	}
	if processedAt != nil {
		snapshot.ProcessedAt = *processedAt
	}

	transaction, err := domain.RehydrateWagerTransaction(snapshot)
	if err != nil {
		return domain.WagerTransaction{}, false, fmt.Errorf("rehydrate referenced transaction: %w", err)
	}
	return transaction, true, nil
}

// dereferenceString converte colunas opcionais sem espalhar verificações de nil.
func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
