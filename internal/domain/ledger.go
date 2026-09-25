package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidLedgerEntry indica que um lançamento não representa uma movimentação coerente.
var ErrInvalidLedgerEntry = errors.New("invalid ledger entry")

// LedgerDirection define como o lançamento afeta o saldo da carteira.
type LedgerDirection string

const (
	// LedgerDebit representa uma saída de fundos da carteira.
	LedgerDebit LedgerDirection = "DEBIT"
	// LedgerCredit representa uma entrada de fundos na carteira.
	LedgerCredit LedgerDirection = "CREDIT"
)

// WalletLedgerEntry registra uma movimentação e os saldos antes e depois dela.
// Após persistido, o lançamento deve ser tratado como imutável.
type WalletLedgerEntry struct {
	ID            string
	WalletID      string
	TransactionID string
	Direction     LedgerDirection
	Money         Money
	BalanceBefore Money
	BalanceAfter  Money
	CreatedAt     time.Time
}

// NewWalletLedgerEntry cria um lançamento somente se a equação financeira estiver correta.
// A mesma validação também será reforçada por constraints no banco de dados.
func NewWalletLedgerEntry(id, walletID, transactionID string, direction LedgerDirection, money, before, after Money, now time.Time) (WalletLedgerEntry, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(walletID) == "" || strings.TrimSpace(transactionID) == "" || now.IsZero() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: identifiers and timestamp are required", ErrInvalidLedgerEntry)
	}
	if money.IsNegative() || money.IsZero() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: movement must be positive", ErrInvalidLedgerEntry)
	}

	var expected Money
	var err error
	switch direction {
	case LedgerDebit:
		expected, err = before.Subtract(money)
	case LedgerCredit:
		expected, err = before.Add(money)
	default:
		return WalletLedgerEntry{}, fmt.Errorf("%w: unknown direction", ErrInvalidLedgerEntry)
	}
	if err != nil {
		return WalletLedgerEntry{}, fmt.Errorf("%w: %v", ErrInvalidLedgerEntry, err)
	}
	equal, err := expected.Compare(after)
	if err != nil || equal != 0 || after.IsNegative() {
		return WalletLedgerEntry{}, fmt.Errorf("%w: inconsistent balances", ErrInvalidLedgerEntry)
	}

	return WalletLedgerEntry{
		ID: id, WalletID: walletID, TransactionID: transactionID,
		Direction: direction, Money: money, BalanceBefore: before,
		BalanceAfter: after, CreatedAt: now,
	}, nil
}
