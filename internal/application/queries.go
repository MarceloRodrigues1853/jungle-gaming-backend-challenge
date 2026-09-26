package application

import (
	"context"
	"errors"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

var ErrInvalidLedgerQuery = errors.New("invalid ledger query")

// LedgerItem é a projeção imutável exposta pela consulta paginada.
type LedgerItem struct {
	ID, WalletID, TransactionID        string
	Direction                          domain.LedgerDirection
	Money, BalanceBefore, BalanceAfter domain.Money
	CreatedAt                          time.Time
}

type LedgerPage struct {
	Items      []LedgerItem
	NextCursor string
}

// TransactionView preserva os dados necessários para acompanhar uma operação.
type TransactionView struct {
	ID, ProviderID, ExternalTransactionID, WalletID, PlayerID string
	RoundID, GameID, ReferenceExternalID, ReferenceID         string
	Kind                                                      domain.TransactionKind
	Money                                                     domain.Money
	Status                                                    domain.TransactionStatus
	FailureCode                                               string
	ResultBalance                                             domain.Money
	HasResultBalance                                          bool
	CreatedAt, UpdatedAt                                      time.Time
}

type ReconciliationResult struct {
	WalletID                                     string
	StoredBalance, CalculatedBalance, Difference domain.Money
	Consistent                                   bool
	CheckedEntries                               int64
}

// FinancialQueries reúne leituras sem permitir alterações financeiras.
type FinancialQueries interface {
	ListWalletLedger(context.Context, string, string, int) (LedgerPage, bool, error)
	GetProviderTransactionByID(context.Context, string, string) (TransactionView, bool, error)
	GetProviderTransactionByExternalID(context.Context, string, string) (TransactionView, bool, error)
	ReconcileWallet(context.Context, string) (ReconciliationResult, bool, error)
}
