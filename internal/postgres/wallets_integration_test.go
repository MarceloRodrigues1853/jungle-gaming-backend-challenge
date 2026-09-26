package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
)

// TestPostgresWalletOpeningPersistsAllArtifactsAtomically cobre o fluxo completo no banco real.
func TestPostgresWalletOpeningPersistsAllArtifactsAtomically(t *testing.T) {
	store, pool := integrationStore(t)
	identity := fmt.Sprintf("%d", time.Now().UnixNano())
	ids := &walletIntegrationIDs{prefix: identity}
	service, err := application.NewWalletService(store, ids, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) })
	if err != nil {
		t.Fatal(err)
	}

	wallet, err := service.Open(context.Background(), application.OpenWalletCommand{
		PlayerID:       "it-opening-player-" + identity,
		InitialBalance: application.MoneyInput{Amount: "100.00", Currency: "BRL"},
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var openingCount, ledgerCount, outboxCount int
	err = pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'),
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1),
		(SELECT count(*) FROM outbox_events WHERE aggregate_id = $1)`, wallet.ID()).Scan(&openingCount, &ledgerCount, &outboxCount)
	if err != nil {
		t.Fatal(err)
	}
	if openingCount != 1 || ledgerCount != 1 || outboxCount != 2 {
		t.Fatalf("opening/ledger/outbox = %d/%d/%d, want 1/1/2", openingCount, ledgerCount, outboxCount)
	}

	loaded, exists, err := service.Get(context.Background(), wallet.ID())
	if err != nil || !exists || loaded.Balance().String() != "100.00" || loaded.Version() != 1 {
		t.Fatalf("Get() = exists %v, balance %s, version %d, error %v", exists, loaded.Balance(), loaded.Version(), err)
	}

	_, err = service.Open(context.Background(), application.OpenWalletCommand{
		PlayerID: wallet.PlayerID(), InitialBalance: application.MoneyInput{Amount: "0.00", Currency: "BRL"},
	})
	if !errors.Is(err, application.ErrWalletConflict) {
		t.Fatalf("duplicate opening error = %v, want wallet conflict", err)
	}
}

// TestPostgresZeroWalletDoesNotPersistOpeningArtifacts garante a exceção de saldo zero.
func TestPostgresZeroWalletDoesNotPersistOpeningArtifacts(t *testing.T) {
	store, pool := integrationStore(t)
	identity := fmt.Sprintf("%d", time.Now().UnixNano())
	service, _ := application.NewWalletService(store, &walletIntegrationIDs{prefix: identity}, time.Now)
	wallet, err := service.Open(context.Background(), application.OpenWalletCommand{
		PlayerID:       "it-zero-player-" + identity,
		InitialBalance: application.MoneyInput{Amount: "0.00", Currency: "BRL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var artifacts int
	if err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM wager_transactions WHERE wallet_id = $1) +
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1) +
		(SELECT count(*) FROM outbox_events WHERE aggregate_id = $1)`, wallet.ID()).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if artifacts != 0 {
		t.Fatalf("zero wallet financial artifacts = %d, want 0", artifacts)
	}
}

type walletIntegrationIDs struct {
	prefix string
	next   int
}

func (ids *walletIntegrationIDs) NewID() (string, error) {
	ids.next++
	return fmt.Sprintf("it-opening-%s-%d", ids.prefix, ids.next), nil
}
