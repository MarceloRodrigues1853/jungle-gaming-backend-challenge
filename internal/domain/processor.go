package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	// FailureInsufficientFunds identifica a rejeição de uma aposta sem saldo.
	FailureInsufficientFunds = "INSUFFICIENT_FUNDS"
	// FailureReversalInsufficientBalance identifica uma reversão que excederia o saldo.
	FailureReversalInsufficientBalance = "REVERSAL_INSUFFICIENT_BALANCE"
)

// WagerProcessingResult resume o resultado financeiro aplicado à carteira.
// LedgerEntry fica nil quando a operação não produz movimento ou é rejeitada.
type WagerProcessingResult struct {
	Status        TransactionStatus
	Balance       Money
	WalletVersion int64
	LedgerEntry   *WalletLedgerEntry
}

// ProcessWagerTransaction aplica a regra da transação à carteira e finaliza seu estado.
// Os agregados só são alterados depois que todas as validações concluem com sucesso.
func ProcessWagerTransaction(wallet *Wallet, transaction *WagerTransaction, reference *WagerTransaction, ledgerEntryID string, now time.Time) (WagerProcessingResult, error) {
	if wallet == nil || transaction == nil {
		return WagerProcessingResult{}, fmt.Errorf("%w: wallet and transaction are required", ErrInvalidTransaction)
	}
	if transaction.walletID != wallet.id || transaction.playerID != wallet.playerID {
		return WagerProcessingResult{}, fmt.Errorf("%w: transaction and wallet ownership differ", ErrInvalidTransaction)
	}
	if _, err := transaction.money.Compare(wallet.balance); err != nil {
		return WagerProcessingResult{}, fmt.Errorf("%w: transaction and wallet currencies differ", ErrInvalidTransaction)
	}

	direction, amount, hasMovement, err := transaction.Movement(reference)
	if err != nil {
		return WagerProcessingResult{}, err
	}

	// Cópias locais impedem que um erro posterior deixe somente um agregado atualizado.
	walletCandidate := *wallet
	transactionCandidate := *transaction
	var ledgerEntry *WalletLedgerEntry

	if hasMovement {
		if strings.TrimSpace(ledgerEntryID) == "" {
			return WagerProcessingResult{}, fmt.Errorf("%w: ledger entry id is required", ErrInvalidLedgerEntry)
		}
		balanceBefore := walletCandidate.balance
		switch direction {
		case LedgerDebit:
			err = walletCandidate.Debit(amount, now)
		case LedgerCredit:
			err = walletCandidate.Credit(amount, now)
		default:
			return WagerProcessingResult{}, fmt.Errorf("%w: unknown movement direction", ErrInvalidLedgerEntry)
		}
		if err != nil {
			if err == ErrInsufficientFunds {
				failureCode := FailureInsufficientFunds
				if transaction.kind == TransactionRollback {
					failureCode = FailureReversalInsufficientBalance
				}
				if rejectErr := transactionCandidate.MarkRejected(failureCode, now); rejectErr != nil {
					return WagerProcessingResult{}, rejectErr
				}
				*transaction = transactionCandidate
				return processingResult(wallet, transaction), nil
			}
			return WagerProcessingResult{}, err
		}

		entry, entryErr := NewWalletLedgerEntry(ledgerEntryID, wallet.id, transaction.id, direction, amount, balanceBefore, walletCandidate.balance, now)
		if entryErr != nil {
			return WagerProcessingResult{}, entryErr
		}
		ledgerEntry = &entry
	}

	if err := transactionCandidate.MarkProcessed(walletCandidate.balance, now); err != nil {
		return WagerProcessingResult{}, err
	}

	*wallet = walletCandidate
	*transaction = transactionCandidate
	return WagerProcessingResult{
		Status:        transaction.status,
		Balance:       wallet.balance,
		WalletVersion: wallet.version,
		LedgerEntry:   ledgerEntry,
	}, nil
}

// processingResult representa uma rejeição sem alterar o estado financeiro da carteira.
func processingResult(wallet *Wallet, transaction *WagerTransaction) WagerProcessingResult {
	return WagerProcessingResult{
		Status:        transaction.status,
		Balance:       wallet.balance,
		WalletVersion: wallet.version,
	}
}
