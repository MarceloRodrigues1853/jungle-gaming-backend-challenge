package domain

import (
	"errors"
	"testing"
	"time"
)

// newProcessingWallet cria uma carteira reidratada com saldo e versão conhecidos para os testes.
func newProcessingWallet(t *testing.T, balance string, version int64) Wallet {
	t.Helper()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	money, err := ParseMoney(balance, "BRL")
	if err != nil {
		t.Fatalf("ParseMoney() error = %v", err)
	}
	wallet, err := RehydrateWallet("wallet-1", "player-1", money, version, now, now)
	if err != nil {
		t.Fatalf("RehydrateWallet() error = %v", err)
	}
	return wallet
}

// newProcessingTransaction cria uma transação externa com timestamp de referência para os testes.
func newProcessingTransaction(t *testing.T, kind TransactionKind, amount string, now time.Time) WagerTransaction {
	t.Helper()
	input := transactionInput(kind, amount)
	input.Now = now
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatalf("NewExternalTransaction() error = %v", err)
	}
	return tx
}

// TestProcessWagerTransactionAppliesBetAndBuildsLedger verifica débito, ledger e resultado final da aposta.
func TestProcessWagerTransactionAppliesBetAndBuildsLedger(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "100.00", 1)
	tx := newProcessingTransaction(t, TransactionBet, "25.00", now.Add(-time.Minute))

	result, err := ProcessWagerTransaction(&wallet, &tx, nil, "ledger-1", now)
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Status != TransactionProcessed || result.Balance.String() != "75.00" || result.WalletVersion != 2 {
		t.Fatalf("result = status %s, balance %s, version %d", result.Status, result.Balance, result.WalletVersion)
	}
	if result.LedgerEntry == nil || result.LedgerEntry.Direction != LedgerDebit || result.LedgerEntry.BalanceBefore.String() != "100.00" || result.LedgerEntry.BalanceAfter.String() != "75.00" {
		t.Fatalf("ledger entry = %#v", result.LedgerEntry)
	}
	if tx.Status() != TransactionProcessed {
		t.Fatalf("transaction status = %s, want processed", tx.Status())
	}
	if balance, ok := tx.ResultBalance(); !ok || balance.String() != "75.00" {
		t.Fatalf("transaction result balance = %s, %v", balance, ok)
	}
}

// TestProcessWagerTransactionRejectsInsufficientBet registra a rejeição sem mudar saldo, versão ou ledger.
func TestProcessWagerTransactionRejectsInsufficientBet(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "20.00", 3)
	tx := newProcessingTransaction(t, TransactionBet, "25.00", now.Add(-time.Minute))

	result, err := ProcessWagerTransaction(&wallet, &tx, nil, "ledger-1", now)
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Status != TransactionRejected || tx.FailureCode() != FailureInsufficientFunds {
		t.Fatalf("rejection = status %s, code %q", result.Status, tx.FailureCode())
	}
	if wallet.Balance().String() != "20.00" || wallet.Version() != 3 || result.LedgerEntry != nil {
		t.Fatalf("rejection changed wallet or created ledger: balance %s, version %d, ledger %#v", wallet.Balance(), wallet.Version(), result.LedgerEntry)
	}
}

// TestProcessWagerTransactionProcessesLossWithoutMovement garante que LOSS finaliza sem saldo, versão ou ledger alterados.
func TestProcessWagerTransactionProcessesLossWithoutMovement(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "20.00", 3)
	tx := newProcessingTransaction(t, TransactionLoss, "0.00", now.Add(-time.Minute))

	result, err := ProcessWagerTransaction(&wallet, &tx, nil, "", now)
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Status != TransactionProcessed || wallet.Balance().String() != "20.00" || wallet.Version() != 3 || result.LedgerEntry != nil {
		t.Fatalf("LOSS result = status %s, balance %s, version %d, ledger %#v", result.Status, wallet.Balance(), wallet.Version(), result.LedgerEntry)
	}
}

// TestProcessWagerTransactionCreditsWinAppliesCredit verifies que WIN credita saldo e cria lançamento de crédito.
func TestProcessWagerTransactionCreditsWinAppliesCredit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "20.00", 1)
	tx := newProcessingTransaction(t, TransactionWin, "7.50", now.Add(-time.Minute))

	result, err := ProcessWagerTransaction(&wallet, &tx, nil, "ledger-win", now)
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Balance.String() != "27.50" || result.LedgerEntry == nil || result.LedgerEntry.Direction != LedgerCredit {
		t.Fatalf("WIN result = balance %s, ledger %#v", result.Balance, result.LedgerEntry)
	}
}

// TestProcessWagerTransactionRejectsInsufficientRollback distingue a reversão sem saldo da aposta sem saldo.
func TestProcessWagerTransactionRejectsInsufficientRollback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "5.00", 2)
	reference := processedTransaction(t, TransactionWin, "25.00", "external-original")
	input := transactionInput(TransactionRollback, "25.00")
	input.Now = now.Add(-time.Minute)
	input.ReferenceExternalID = "external-original"
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatalf("NewExternalTransaction() error = %v", err)
	}
	if err := tx.ResolveReference(reference, now.Add(-30*time.Second)); err != nil {
		t.Fatalf("ResolveReference() error = %v", err)
	}

	result, err := ProcessWagerTransaction(&wallet, &tx, &reference, "ledger-rollback", now)
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Status != TransactionRejected || tx.FailureCode() != FailureReversalInsufficientBalance {
		t.Fatalf("rejection = status %s, code %q", result.Status, tx.FailureCode())
	}
	if wallet.Balance().String() != "5.00" || wallet.Version() != 2 || result.LedgerEntry != nil {
		t.Fatalf("rollback rejection changed wallet or created ledger: balance %s, version %d, ledger %#v", wallet.Balance(), wallet.Version(), result.LedgerEntry)
	}
}

// TestProcessWagerTransactionErrorsDoNotPartiallyMutateState confirma atomicidade local quando falta o ID do ledger.
func TestProcessWagerTransactionErrorsDoNotPartiallyMutateState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "20.00", 1)
	tx := newProcessingTransaction(t, TransactionBet, "5.00", now.Add(-time.Minute))

	_, err := ProcessWagerTransaction(&wallet, &tx, nil, "", now)
	if !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Fatalf("ProcessWagerTransaction() error = %v, want invalid ledger entry", err)
	}
	if wallet.Balance().String() != "20.00" || wallet.Version() != 1 || tx.Status() != TransactionPending {
		t.Fatalf("error partially mutated state: balance %s, version %d, status %s", wallet.Balance(), wallet.Version(), tx.Status())
	}
}

// TestProcessWagerTransactionRejectsWalletOwnershipMismatch impede aplicar uma operação na carteira de outro jogador.
func TestProcessWagerTransactionRejectsWalletOwnershipMismatch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)
	wallet := newProcessingWallet(t, "20.00", 1)
	tx := newProcessingTransaction(t, TransactionBet, "5.00", now.Add(-time.Minute))
	tx.playerID = "player-other"

	_, err := ProcessWagerTransaction(&wallet, &tx, nil, "ledger-1", now)
	if !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("ProcessWagerTransaction() error = %v, want invalid transaction", err)
	}
	if wallet.Balance().String() != "20.00" || wallet.Version() != 1 || tx.Status() != TransactionPending {
		t.Fatalf("ownership error mutated state: balance %s, version %d, status %s", wallet.Balance(), wallet.Version(), tx.Status())
	}
}
