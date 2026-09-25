package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TransactionKind identifica uma operação financeira recebida ou interna.
type TransactionKind string

const (
	TransactionOpening  TransactionKind = "OPENING"
	TransactionBet      TransactionKind = "BET"
	TransactionWin      TransactionKind = "WIN"
	TransactionLoss     TransactionKind = "LOSS"
	TransactionRefund   TransactionKind = "REFUND"
	TransactionRollback TransactionKind = "ROLLBACK"
)

// TransactionStatus representa o ciclo de vida persistido da operação.
type TransactionStatus string

const (
	TransactionPending          TransactionStatus = "PENDING"
	TransactionPendingReference TransactionStatus = "PENDING_REFERENCE"
	TransactionProcessed        TransactionStatus = "PROCESSED"
	TransactionRejected         TransactionStatus = "REJECTED"
	TransactionFailed           TransactionStatus = "FAILED"
)

var (
	// ErrInvalidTransaction indica metadados ou uma transição inválida.
	ErrInvalidTransaction = errors.New("invalid transaction")
	// ErrTerminalTransaction indica que uma operação terminal não pode mudar novamente.
	ErrTerminalTransaction = errors.New("transaction is terminal")
)

// ExternalTransactionInput contém os dados de negócio e idempotência da entrada externa.
// PayloadHash deve conter os 32 bytes do SHA-256 canônico definido pela aplicação.
type ExternalTransactionInput struct {
	ID                    string
	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PayloadHash           []byte
	WalletID              string
	PlayerID              string
	RoundID               string
	GameID                string
	Kind                  TransactionKind
	Money                 Money
	ReferenceExternalID   string
	Now                   time.Time
}

// OpeningTransactionInput contém apenas os dados aplicáveis à abertura interna.
type OpeningTransactionInput struct {
	ID       string
	WalletID string
	PlayerID string
	Money    Money
	Now      time.Time
}

// WagerTransaction encapsula uma operação financeira e suas transições de estado.
// Campos privados evitam mutações que contornem as regras de domínio.
type WagerTransaction struct {
	id                    string
	providerID            string
	externalTransactionID string
	idempotencyKey        string
	payloadHash           [32]byte
	hasPayloadHash        bool
	walletID              string
	playerID              string
	roundID               string
	gameID                string
	kind                  TransactionKind
	money                 Money
	referenceExternalID   string
	referenceID           string
	status                TransactionStatus
	failureCode           string
	resultBalance         Money
	hasResultBalance      bool
	createdAt             time.Time
	updatedAt             time.Time
	processedAt           time.Time
}

// WagerTransactionSnapshot é a representação explícita para persistência e reidratação.
// Alterar o snapshot não altera uma entidade já criada.
type WagerTransactionSnapshot struct {
	ID                    string
	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PayloadHash           []byte
	WalletID              string
	PlayerID              string
	RoundID               string
	GameID                string
	Kind                  TransactionKind
	Money                 Money
	ReferenceExternalID   string
	ReferenceID           string
	Status                TransactionStatus
	FailureCode           string
	ResultBalance         Money
	HasResultBalance      bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
	ProcessedAt           time.Time
}

// NewExternalTransaction valida e cria uma operação externa em PENDING.
func NewExternalTransaction(input ExternalTransactionInput) (WagerTransaction, error) {
	tx := WagerTransaction{
		id:                    input.ID,
		providerID:            input.ProviderID,
		externalTransactionID: input.ExternalTransactionID,
		idempotencyKey:        input.IdempotencyKey,
		walletID:              input.WalletID,
		playerID:              input.PlayerID,
		roundID:               input.RoundID,
		gameID:                input.GameID,
		kind:                  input.Kind,
		money:                 input.Money,
		referenceExternalID:   input.ReferenceExternalID,
		status:                TransactionPending,
		createdAt:             input.Now,
		updatedAt:             input.Now,
	}
	if len(input.PayloadHash) != len(tx.payloadHash) {
		return WagerTransaction{}, fmt.Errorf("%w: payload hash must be SHA-256", ErrInvalidTransaction)
	}
	copy(tx.payloadHash[:], input.PayloadHash)
	tx.hasPayloadHash = true
	if err := tx.validate(); err != nil {
		return WagerTransaction{}, err
	}
	return tx, nil
}

// NewOpeningTransaction cria a operação interna de abertura de carteira.
func NewOpeningTransaction(input OpeningTransactionInput) (WagerTransaction, error) {
	tx := WagerTransaction{
		id:        input.ID,
		walletID:  input.WalletID,
		playerID:  input.PlayerID,
		kind:      TransactionOpening,
		money:     input.Money,
		status:    TransactionPending,
		createdAt: input.Now,
		updatedAt: input.Now,
	}
	if err := tx.validate(); err != nil {
		return WagerTransaction{}, err
	}
	return tx, nil
}

// RehydrateWagerTransaction restaura um snapshot persistido sem reaplicar efeitos.
// Transições normais devem usar os métodos de estado, não esta função.
func RehydrateWagerTransaction(snapshot WagerTransactionSnapshot) (WagerTransaction, error) {
	tx := WagerTransaction{
		id:                    snapshot.ID,
		providerID:            snapshot.ProviderID,
		externalTransactionID: snapshot.ExternalTransactionID,
		idempotencyKey:        snapshot.IdempotencyKey,
		walletID:              snapshot.WalletID,
		playerID:              snapshot.PlayerID,
		roundID:               snapshot.RoundID,
		gameID:                snapshot.GameID,
		kind:                  snapshot.Kind,
		money:                 snapshot.Money,
		referenceExternalID:   snapshot.ReferenceExternalID,
		referenceID:           snapshot.ReferenceID,
		status:                snapshot.Status,
		failureCode:           snapshot.FailureCode,
		resultBalance:         snapshot.ResultBalance,
		hasResultBalance:      snapshot.HasResultBalance,
		createdAt:             snapshot.CreatedAt,
		updatedAt:             snapshot.UpdatedAt,
		processedAt:           snapshot.ProcessedAt,
	}
	if len(snapshot.PayloadHash) != 0 {
		if len(snapshot.PayloadHash) != len(tx.payloadHash) {
			return WagerTransaction{}, fmt.Errorf("%w: payload hash must be SHA-256", ErrInvalidTransaction)
		}
		copy(tx.payloadHash[:], snapshot.PayloadHash)
		tx.hasPayloadHash = true
	}
	if err := tx.validate(); err != nil {
		return WagerTransaction{}, err
	}
	return tx, nil
}

// MarkPendingReference registra que uma referência necessária ainda não chegou.
func (tx *WagerTransaction) MarkPendingReference(now time.Time) error {
	if tx.status != TransactionPending {
		return tx.transitionError()
	}
	if strings.TrimSpace(tx.referenceExternalID) == "" {
		return fmt.Errorf("%w: pending reference requires a reference id", ErrInvalidTransaction)
	}
	if err := tx.validateUpdateTime(now); err != nil {
		return err
	}
	tx.status = TransactionPendingReference
	tx.updatedAt = now
	return nil
}

// ResolveReference registra a referência interna e devolve a operação à fila de processamento.
func (tx *WagerTransaction) ResolveReference(referenceID string, now time.Time) error {
	if tx.status != TransactionPendingReference {
		return tx.transitionError()
	}
	if strings.TrimSpace(referenceID) == "" {
		return fmt.Errorf("%w: resolved reference id is required", ErrInvalidTransaction)
	}
	if err := tx.validateUpdateTime(now); err != nil {
		return err
	}
	tx.referenceID = referenceID
	tx.status = TransactionPending
	tx.updatedAt = now
	return nil
}

// MarkProcessed finaliza a operação e guarda o saldo observado no processamento.
func (tx *WagerTransaction) MarkProcessed(balance Money, now time.Time) error {
	if !tx.canFinish() {
		return tx.transitionError()
	}
	if tx.referenceExternalID != "" && tx.referenceID == "" {
		return fmt.Errorf("%w: referenced transaction must be resolved before processing", ErrInvalidTransaction)
	}
	if balance.IsNegative() || strings.TrimSpace(balance.Currency()) == "" {
		return fmt.Errorf("%w: result balance must be initialized and non-negative", ErrInvalidTransaction)
	}
	if _, err := tx.money.Compare(balance); err != nil {
		return fmt.Errorf("%w: result balance currency differs from operation", ErrInvalidTransaction)
	}
	if err := tx.validateUpdateTime(now); err != nil {
		return err
	}
	tx.status = TransactionProcessed
	tx.failureCode = ""
	tx.resultBalance = balance
	tx.hasResultBalance = true
	tx.processedAt = now
	tx.updatedAt = now
	return nil
}

// MarkRejected finaliza a operação por uma regra de negócio com código estável.
func (tx *WagerTransaction) MarkRejected(code string, now time.Time) error {
	return tx.finishWithFailure(TransactionRejected, code, now)
}

// MarkFailed finaliza a operação por uma falha permanente de infraestrutura.
func (tx *WagerTransaction) MarkFailed(code string, now time.Time) error {
	return tx.finishWithFailure(TransactionFailed, code, now)
}

func (tx *WagerTransaction) finishWithFailure(status TransactionStatus, code string, now time.Time) error {
	if !tx.canFinish() {
		return tx.transitionError()
	}
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("%w: failure code is required", ErrInvalidTransaction)
	}
	if err := tx.validateUpdateTime(now); err != nil {
		return err
	}
	tx.status = status
	tx.failureCode = code
	tx.hasResultBalance = false
	tx.processedAt = time.Time{}
	tx.updatedAt = now
	return nil
}

func (tx WagerTransaction) canFinish() bool {
	return tx.status == TransactionPending || tx.status == TransactionPendingReference
}

func (tx WagerTransaction) transitionError() error {
	if tx.status == TransactionProcessed || tx.status == TransactionRejected || tx.status == TransactionFailed {
		return fmt.Errorf("%w: %s", ErrTerminalTransaction, tx.status)
	}
	return fmt.Errorf("%w: cannot transition from %s", ErrInvalidTransaction, tx.status)
}

func (tx *WagerTransaction) validateUpdateTime(now time.Time) error {
	if now.IsZero() || now.Before(tx.updatedAt) {
		return fmt.Errorf("%w: update time is zero or moved backwards", ErrInvalidTransaction)
	}
	return nil
}

func (tx WagerTransaction) validate() error {
	if strings.TrimSpace(tx.id) == "" || strings.TrimSpace(tx.walletID) == "" || strings.TrimSpace(tx.playerID) == "" {
		return fmt.Errorf("%w: transaction, wallet, and player ids are required", ErrInvalidTransaction)
	}
	if strings.TrimSpace(tx.money.Currency()) == "" || tx.money.IsNegative() {
		return fmt.Errorf("%w: money must be initialized and non-negative", ErrInvalidTransaction)
	}
	if tx.createdAt.IsZero() || tx.updatedAt.IsZero() || tx.updatedAt.Before(tx.createdAt) {
		return fmt.Errorf("%w: invalid timestamps", ErrInvalidTransaction)
	}

	switch tx.kind {
	case TransactionOpening:
		if tx.providerID != "" || tx.externalTransactionID != "" || tx.idempotencyKey != "" || tx.hasPayloadHash || tx.roundID != "" || tx.gameID != "" || tx.referenceExternalID != "" || tx.referenceID != "" {
			return fmt.Errorf("%w: opening must not contain external metadata", ErrInvalidTransaction)
		}
	case TransactionBet, TransactionWin, TransactionLoss, TransactionRefund, TransactionRollback:
		if strings.TrimSpace(tx.providerID) == "" || strings.TrimSpace(tx.externalTransactionID) == "" || strings.TrimSpace(tx.idempotencyKey) == "" || !tx.hasPayloadHash || strings.TrimSpace(tx.roundID) == "" || strings.TrimSpace(tx.gameID) == "" {
			return fmt.Errorf("%w: external transaction metadata is incomplete", ErrInvalidTransaction)
		}
		if (tx.kind == TransactionRefund || tx.kind == TransactionRollback) != (strings.TrimSpace(tx.referenceExternalID) != "") {
			return fmt.Errorf("%w: reference external id is required only for reversals", ErrInvalidTransaction)
		}
		if tx.referenceID != "" && tx.kind != TransactionRefund && tx.kind != TransactionRollback && tx.kind != TransactionWin {
			return fmt.Errorf("%w: this operation kind cannot resolve a reference", ErrInvalidTransaction)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, tx.kind)
	}

	if tx.kind == TransactionLoss {
		if !tx.money.IsZero() {
			return fmt.Errorf("%w: LOSS amount must be zero", ErrInvalidTransaction)
		}
	} else if tx.money.IsZero() {
		return fmt.Errorf("%w: %s amount must be positive", ErrInvalidTransaction, tx.kind)
	}

	switch tx.status {
	case TransactionPending, TransactionPendingReference:
		if tx.failureCode != "" || tx.hasResultBalance || !tx.processedAt.IsZero() {
			return fmt.Errorf("%w: pending transaction has terminal data", ErrInvalidTransaction)
		}
		if tx.status == TransactionPendingReference && strings.TrimSpace(tx.referenceExternalID) == "" {
			return fmt.Errorf("%w: pending reference transaction has no reference", ErrInvalidTransaction)
		}
		if tx.status == TransactionPendingReference && tx.referenceID != "" {
			return fmt.Errorf("%w: pending reference transaction is already resolved", ErrInvalidTransaction)
		}
	case TransactionProcessed:
		if tx.failureCode != "" || !tx.hasResultBalance || tx.resultBalance.IsNegative() || tx.processedAt.IsZero() || tx.processedAt.Before(tx.createdAt) || tx.processedAt.After(tx.updatedAt) {
			return fmt.Errorf("%w: processed transaction has invalid result data", ErrInvalidTransaction)
		}
		if tx.referenceExternalID != "" && tx.referenceID == "" {
			return fmt.Errorf("%w: processed reference transaction has no resolved reference", ErrInvalidTransaction)
		}
		if _, err := tx.money.Compare(tx.resultBalance); err != nil {
			return fmt.Errorf("%w: result balance currency differs from operation", ErrInvalidTransaction)
		}
	case TransactionRejected, TransactionFailed:
		if strings.TrimSpace(tx.failureCode) == "" || tx.hasResultBalance || !tx.processedAt.IsZero() {
			return fmt.Errorf("%w: terminal failure data is incomplete", ErrInvalidTransaction)
		}
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidTransaction, tx.status)
	}
	return nil
}

// ID retorna o identificador interno estável.
func (tx WagerTransaction) ID() string { return tx.id }

// Kind retorna o tipo de operação.
func (tx WagerTransaction) Kind() TransactionKind { return tx.kind }

// Status retorna o estado persistível atual.
func (tx WagerTransaction) Status() TransactionStatus { return tx.status }

// Money retorna o valor imutável da operação.
func (tx WagerTransaction) Money() Money { return tx.money }

// WalletID retorna a carteira à qual a operação pertence.
func (tx WagerTransaction) WalletID() string { return tx.walletID }

// PlayerID retorna o jogador da operação.
func (tx WagerTransaction) PlayerID() string { return tx.playerID }

// ProviderID retorna o provedor externo, vazio para OPENING.
func (tx WagerTransaction) ProviderID() string { return tx.providerID }

// ExternalTransactionID retorna o identificador do provedor, vazio para OPENING.
func (tx WagerTransaction) ExternalTransactionID() string { return tx.externalTransactionID }

// IdempotencyKey retorna a chave externa, vazia para OPENING.
func (tx WagerTransaction) IdempotencyKey() string { return tx.idempotencyKey }

// PayloadHash retorna uma cópia do hash, evitando expor o estado interno.
func (tx WagerTransaction) PayloadHash() ([]byte, bool) {
	if !tx.hasPayloadHash {
		return nil, false
	}
	return append([]byte(nil), tx.payloadHash[:]...), true
}

// Snapshot retorna uma cópia explícita do estado para persistência.
func (tx WagerTransaction) Snapshot() WagerTransactionSnapshot {
	var payloadHash []byte
	if tx.hasPayloadHash {
		payloadHash = append([]byte(nil), tx.payloadHash[:]...)
	}
	return WagerTransactionSnapshot{
		ID: tx.id, ProviderID: tx.providerID, ExternalTransactionID: tx.externalTransactionID,
		IdempotencyKey: tx.idempotencyKey, PayloadHash: payloadHash,
		WalletID: tx.walletID, PlayerID: tx.playerID, RoundID: tx.roundID, GameID: tx.gameID,
		Kind: tx.kind, Money: tx.money, ReferenceExternalID: tx.referenceExternalID,
		ReferenceID: tx.referenceID, Status: tx.status, FailureCode: tx.failureCode,
		ResultBalance: tx.resultBalance, HasResultBalance: tx.hasResultBalance,
		CreatedAt: tx.createdAt, UpdatedAt: tx.updatedAt, ProcessedAt: tx.processedAt,
	}
}

// ReferenceExternalID retorna a referência do provedor, se aplicável.
func (tx WagerTransaction) ReferenceExternalID() string { return tx.referenceExternalID }

// ReferenceID retorna a referência interna resolvida, se já persistida.
func (tx WagerTransaction) ReferenceID() string { return tx.referenceID }

// FailureCode retorna o código estável de rejeição ou falha.
func (tx WagerTransaction) FailureCode() string { return tx.failureCode }

// ResultBalance retorna o saldo observado no processamento original, se processada.
func (tx WagerTransaction) ResultBalance() (Money, bool) {
	return tx.resultBalance, tx.hasResultBalance
}

// CreatedAt retorna o instante de criação.
func (tx WagerTransaction) CreatedAt() time.Time { return tx.createdAt }

// UpdatedAt retorna o instante da última transição.
func (tx WagerTransaction) UpdatedAt() time.Time { return tx.updatedAt }

// ProcessedAt retorna o instante de conclusão bem-sucedida, ou zero.
func (tx WagerTransaction) ProcessedAt() time.Time { return tx.processedAt }
