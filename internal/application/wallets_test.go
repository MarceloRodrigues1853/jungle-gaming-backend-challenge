package application

import (
	"context"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestOpenWalletWithBalanceBuildsAtomicArtifacts cobre a abertura financeira completa.
func TestOpenWalletWithBalanceBuildsAtomicArtifacts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	repository := &walletRepositorySpy{}
	service, err := NewWalletService(repository, &sequenceIDs{values: []string{"wallet-1", "opening-1", "ledger-1", "event-1", "event-2"}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := service.Open(context.Background(), OpenWalletCommand{PlayerID: "player-1", InitialBalance: MoneyInput{Amount: "100.00", Currency: "BRL"}})
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance().String() != "100.00" || wallet.Version() != 1 {
		t.Fatalf("wallet = %s/v%d", wallet.Balance(), wallet.Version())
	}
	if repository.opening == nil || repository.opening.Status() != domain.TransactionProcessed || repository.ledger == nil || len(repository.events) != 2 {
		t.Fatalf("opening artifacts = %#v / %#v / %d", repository.opening, repository.ledger, len(repository.events))
	}
}

// TestOpenZeroWalletDoesNotCreateFinancialArtifacts preserva o contrato de saldo zero.
func TestOpenZeroWalletDoesNotCreateFinancialArtifacts(t *testing.T) {
	t.Parallel()
	repository := &walletRepositorySpy{}
	service, _ := NewWalletService(repository, &sequenceIDs{values: []string{"wallet-zero"}}, func() time.Time { return time.Now().UTC() })
	_, err := service.Open(context.Background(), OpenWalletCommand{PlayerID: "player-1", InitialBalance: MoneyInput{Amount: "0.00", Currency: "BRL"}})
	if err != nil {
		t.Fatal(err)
	}
	if repository.opening != nil || repository.ledger != nil || len(repository.events) != 0 {
		t.Fatal("zero wallet created financial artifacts")
	}
}

type walletRepositorySpy struct {
	wallet  domain.Wallet
	opening *domain.WagerTransaction
	ledger  *domain.WalletLedgerEntry
	events  []OutboxEvent
}

func (spy *walletRepositorySpy) CreateWallet(_ context.Context, wallet domain.Wallet, opening *domain.WagerTransaction, ledger *domain.WalletLedgerEntry, events []OutboxEvent) error {
	spy.wallet, spy.opening, spy.ledger, spy.events = wallet, opening, ledger, events
	return nil
}

func (spy *walletRepositorySpy) GetWallet(context.Context, string) (domain.Wallet, bool, error) {
	return spy.wallet, true, nil
}
