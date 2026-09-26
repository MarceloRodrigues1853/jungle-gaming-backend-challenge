package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestPostgresFinancialQueries valida paginação estável, isolamento do provedor
// e reconstrução do saldo em uma visão consistente do banco real.
func TestPostgresFinancialQueries(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 0, now)
	first := integrationTransaction(t, "query-win-a", "query-external-a", "provider-a:query-a", walletID, playerID, domain.TransactionWin, "10.00", "query-a", now)
	second := integrationTransaction(t, "query-win-b", "query-external-b", "provider-a:query-b", walletID, playerID, domain.TransactionWin, "5.00", "query-b", now.Add(time.Second))
	for index, transaction := range []domain.WagerTransaction{first, second} {
		processedAt := now.Add(time.Duration(index+1) * time.Second)
		if _, err := store.ProcessWagerTransaction(context.Background(), transaction, nil,
			"ledger-"+transaction.ID(), integrationEventIDs(transaction.ID()), nil, processedAt); err != nil {
			t.Fatalf("process query transaction %d: %v", index, err)
		}
	}

	firstPage, exists, err := store.ListWalletLedger(context.Background(), walletID, "", 1)
	if err != nil || !exists || len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first ledger page = %+v, exists %v, error %v", firstPage, exists, err)
	}
	secondPage, exists, err := store.ListWalletLedger(context.Background(), walletID, firstPage.NextCursor, 1)
	if err != nil || !exists || len(secondPage.Items) != 1 || secondPage.NextCursor != "" {
		t.Fatalf("second ledger page = %+v, exists %v, error %v", secondPage, exists, err)
	}
	if firstPage.Items[0].ID == secondPage.Items[0].ID {
		t.Fatal("cursor pagination returned the same ledger entry twice")
	}

	view, exists, err := store.GetProviderTransactionByID(context.Background(), "provider-a", first.ID())
	if err != nil || !exists || view.ExternalTransactionID != first.ExternalTransactionID() {
		t.Fatalf("transaction by id = %+v, exists %v, error %v", view, exists, err)
	}
	if _, exists, err := store.GetProviderTransactionByID(context.Background(), "provider-b", first.ID()); err != nil || exists {
		t.Fatalf("cross-provider query exists/error = %v/%v", exists, err)
	}
	byExternal, exists, err := store.GetProviderTransactionByExternalID(context.Background(), "provider-a", second.ExternalTransactionID())
	if err != nil || !exists || byExternal.ID != second.ID() {
		t.Fatalf("transaction by external id = %+v, exists %v, error %v", byExternal, exists, err)
	}

	reconciliation, exists, err := store.ReconcileWallet(context.Background(), walletID)
	if err != nil || !exists || !reconciliation.Consistent || reconciliation.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v, exists %v, error %v", reconciliation, exists, err)
	}
	if reconciliation.StoredBalance.String() != "15.00" || reconciliation.CalculatedBalance.String() != "15.00" || reconciliation.Difference.String() != "0.00" {
		t.Fatalf("reconciliation balances = %s/%s/%s", reconciliation.StoredBalance, reconciliation.CalculatedBalance, reconciliation.Difference)
	}
}
