package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresIdempotentReplayReturnsOriginalBalance valida replay, conflito e saldo original no PostgreSQL real.
func TestPostgresIdempotentReplayReturnsOriginalBalance(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)

	bet := integrationTransaction(t, "bet-original", "external-bet", "provider-a:bet", walletID, playerID, domain.TransactionBet, "25.00", "same-payload", now)
	first, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second))
	if err != nil {
		t.Fatalf("first ProcessWagerTransaction() error = %v", err)
	}
	if first.Status != domain.TransactionProcessed || first.IdempotentReplay || first.Balance.String() != "75.00" {
		t.Fatalf("first result = status %s, replay %v, balance %s", first.Status, first.IdempotentReplay, first.Balance)
	}

	win := integrationTransaction(t, "win-after-bet", "external-win", "provider-a:win", walletID, playerID, domain.TransactionWin, "10.00", "win-payload", now.Add(2*time.Second))
	if _, err := store.ProcessWagerTransaction(context.Background(), win, nil, "ledger-"+win.ID(), integrationEventIDs(win.ID()), nil, now.Add(3*time.Second)); err != nil {
		t.Fatalf("WIN ProcessWagerTransaction() error = %v", err)
	}

	replay, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ignored-ledger", integrationEventIDs("ignored-replay"), nil, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("replay ProcessWagerTransaction() error = %v", err)
	}
	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID || replay.Balance.String() != "75.00" {
		t.Fatalf("replay = id %s, replay %v, balance %s; want original id and balance 75.00", replay.TransactionID, replay.IdempotentReplay, replay.Balance)
	}

	changedPayload := integrationTransaction(t, "bet-conflict", "external-bet", "provider-a:bet", walletID, playerID, domain.TransactionBet, "25.00", "different-payload", now.Add(5*time.Second))
	if _, err := store.ProcessWagerTransaction(context.Background(), changedPayload, nil, "unused-ledger", integrationEventIDs("unused-conflict"), nil, now.Add(6*time.Second)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload error = %v, want idempotency conflict", err)
	}

	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 8_500 || ledgerCount != 2 {
		t.Fatalf("final balance/ledger count = %d/%d, want 8500/2", balance, ledgerCount)
	}
	if outboxCount := readWalletOutboxCount(t, pool, walletID); outboxCount != 4 {
		t.Fatalf("outbox count after replay = %d, want 4 without duplicate events", outboxCount)
	}
}

// TestPostgresInboxCompletesAtomicallyWithFinancialEffects comprova que uma
// mensagem e seus efeitos financeiros compartilham o mesmo commit SQL.
func TestPostgresInboxCompletesAtomicallyWithFinancialEffects(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	bet := integrationTransaction(t, "bet-inbox", "external-inbox", "provider-a:inbox", walletID, playerID, domain.TransactionBet, "25.00", "inbox-payload", now)
	payloadHash := sha256.Sum256([]byte("complete-sqs-envelope"))
	delivery := &application.InboxDelivery{ConsumerName: "wager-transactions-v1",
		MessageID: "message-" + strings.TrimPrefix(walletID, "it-wallet-"), PayloadHash: payloadHash, ReceivedAt: now}

	result, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), delivery, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}
	if result.Status != domain.TransactionProcessed {
		t.Fatalf("result status = %s, want PROCESSED", result.Status)
	}
	var completedAt *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT completed_at FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, delivery.ConsumerName, delivery.MessageID).Scan(&completedAt); err != nil {
		t.Fatalf("read inbox message: %v", err)
	}
	if completedAt == nil {
		t.Fatal("inbox completed_at is nil after financial commit")
	}

	replay, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "unused-ledger", integrationEventIDs("unused-inbox"), delivery, now.Add(2*time.Second))
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("inbox replay = replay %v, error %v", replay.IdempotentReplay, err)
	}
	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 7_500 || ledgerCount != 1 {
		t.Fatalf("balance/ledger after replay = %d/%d, want 7500/1", balance, ledgerCount)
	}
}

// TestPostgresConcurrentBetsSerializePerWallet demonstra que duas conexões não permitem saldo negativo nem débito duplicado.
func TestPostgresConcurrentBetsSerializePerWallet(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	transactions := []domain.WagerTransaction{
		integrationTransaction(t, "bet-concurrent-a", "external-concurrent-a", "provider-a:concurrent-a", walletID, playerID, domain.TransactionBet, "80.00", "concurrent-a", now),
		integrationTransaction(t, "bet-concurrent-b", "external-concurrent-b", "provider-a:concurrent-b", walletID, playerID, domain.TransactionBet, "80.00", "concurrent-b", now),
	}

	results := make([]domain.WagerProcessingResult, len(transactions))
	errorsByTransaction := make([]error, len(transactions))
	var wait sync.WaitGroup
	for index := range transactions {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsByTransaction[index] = store.ProcessWagerTransaction(
				context.Background(), transactions[index], nil, "ledger-"+transactions[index].ID(), integrationEventIDs(transactions[index].ID()), nil, now.Add(time.Second),
			)
		}(index)
	}
	wait.Wait()

	processed, rejected := 0, 0
	for index, err := range errorsByTransaction {
		if err != nil {
			t.Fatalf("concurrent transaction %d error = %v", index, err)
		}
		switch results[index].Status {
		case domain.TransactionProcessed:
			processed++
		case domain.TransactionRejected:
			if results[index].FailureCode != domain.FailureInsufficientFunds {
				t.Errorf("rejected transaction failure code = %q", results[index].FailureCode)
			}
			rejected++
		default:
			t.Errorf("unexpected concurrent transaction status = %s", results[index].Status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected = %d/%d, want 1/1", processed, rejected)
	}

	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 2_000 || ledgerCount != 1 {
		t.Fatalf("final balance/ledger count = %d/%d, want 2000/1", balance, ledgerCount)
	}
	if outboxCount := readWalletOutboxCount(t, pool, walletID); outboxCount != 3 {
		t.Fatalf("processed plus rejected outbox count = %d, want 3", outboxCount)
	}
}

// TestPostgresFindProviderTransactionIsolatesProvider valida a busca usada por reversões.
func TestPostgresFindProviderTransactionIsolatesProvider(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	bet := integrationTransaction(t, "bet-reference", "external-reference", "provider-a:reference", walletID, playerID, domain.TransactionBet, "25.00", "reference-payload", now)
	if _, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second)); err != nil {
		t.Fatalf("ProcessWagerTransaction() error = %v", err)
	}

	found, exists, err := store.FindProviderTransaction(context.Background(), "provider-a", bet.ExternalTransactionID())
	if err != nil {
		t.Fatalf("FindProviderTransaction() error = %v", err)
	}
	if !exists || found.ID() != bet.ID() || found.Status() != domain.TransactionProcessed {
		t.Fatalf("found transaction = %s/%s, exists %v", found.ID(), found.Status(), exists)
	}

	if _, exists, err := store.FindProviderTransaction(context.Background(), "provider-b", bet.ExternalTransactionID()); err != nil || exists {
		t.Fatalf("cross-provider lookup exists/error = %v/%v, want false/nil", exists, err)
	}
}

// TestPostgresProcessesWinWithOptionalReference cobre o contrato que permite vincular WIN a BET.
func TestPostgresProcessesWinWithOptionalReference(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	bet := integrationTransaction(t, "bet-for-win", "external-bet-for-win", "provider-a:bet-for-win", walletID, playerID, domain.TransactionBet, "25.00", "bet-for-win-payload", now)
	if _, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second)); err != nil {
		t.Fatalf("process referenced BET: %v", err)
	}
	reference, exists, err := store.FindProviderTransaction(context.Background(), "provider-a", bet.ExternalTransactionID())
	if err != nil || !exists {
		t.Fatalf("load processed BET reference = exists %v, error %v", exists, err)
	}

	identity := strings.TrimPrefix(walletID, "it-wallet-")
	winMoney, _ := domain.ParseMoney("10.00", "BRL")
	winHash := sha256.Sum256([]byte("referenced-win-payload"))
	win, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "win-with-reference-" + identity, ProviderID: "provider-a",
		ExternalTransactionID: "external-win-with-reference-" + identity,
		IdempotencyKey:        "provider-a:win-with-reference-" + identity, PayloadHash: winHash[:],
		WalletID: walletID, PlayerID: playerID, RoundID: "it-round", GameID: "it-game",
		Kind: domain.TransactionWin, Money: winMoney,
		ReferenceExternalID: bet.ExternalTransactionID(), Now: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("NewExternalTransaction() error = %v", err)
	}
	if err := win.ResolveReference(reference, now.Add(2*time.Second)); err != nil {
		t.Fatalf("ResolveReference() error = %v", err)
	}
	result, err := store.ProcessWagerTransaction(context.Background(), win, &reference, "ledger-"+win.ID(), integrationEventIDs(win.ID()), nil, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("process referenced WIN: %v", err)
	}
	if result.Status != domain.TransactionProcessed || result.Balance.String() != "85.00" {
		t.Fatalf("WIN result = %s/%s, want PROCESSED/85.00", result.Status, result.Balance)
	}
}

// integrationStore conecta somente quando uma URL de banco de teste explicitamente configurada está disponível.
func integrationStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	connectionString := os.Getenv("JUNGLE_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("set JUNGLE_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := OpenPool(ctx, connectionString)
	if err != nil {
		t.Fatalf("OpenPool() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return store, pool
}

// seedIntegrationWallet cria uma carteira isolada por teste sem apagar dados de outros testes.
func seedIntegrationWallet(t *testing.T, pool *pgxpool.Pool, balance int64, now time.Time) (string, string) {
	t.Helper()
	identity := fmt.Sprintf("%d", time.Now().UnixNano())
	walletID, playerID := "it-wallet-"+identity, "it-player-"+identity
	_, err := pool.Exec(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', $3, 1, $4, $4)`, walletID, playerID, balance, now)
	if err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return walletID, playerID
}

// integrationTransaction cria uma operação externa válida com hash fixo por conteúdo de teste.
func integrationTransaction(t *testing.T, id, externalID, idempotencyKey, walletID, playerID string, kind domain.TransactionKind, amount, payload string, now time.Time) domain.WagerTransaction {
	t.Helper()
	identity := strings.TrimPrefix(walletID, "it-wallet-")
	id = id + "-" + identity
	externalID = externalID + "-" + identity
	idempotencyKey = idempotencyKey + "-" + identity
	hash := sha256.Sum256([]byte(payload))
	money, err := domain.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseMoney() error = %v", err)
	}
	tx, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: id, ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: idempotencyKey, PayloadHash: hash[:], WalletID: walletID,
		PlayerID: playerID, RoundID: "it-round", GameID: "it-game", Kind: kind,
		Money: money, Now: now,
	})
	if err != nil {
		t.Fatalf("NewExternalTransaction() error = %v", err)
	}
	return tx
}

// readWalletAndLedger consulta o saldo e a quantidade de lançamentos da carteira de integração.
func readWalletAndLedger(t *testing.T, pool *pgxpool.Pool, walletID string) (int64, int64) {
	t.Helper()
	var balance, ledgerCount int64
	if err := pool.QueryRow(context.Background(), `SELECT balance FROM wallets WHERE id = $1`, walletID).Scan(&balance); err != nil {
		t.Fatalf("read wallet balance: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&ledgerCount); err != nil {
		t.Fatalf("read wallet ledger count: %v", err)
	}
	return balance, ledgerCount
}

// readWalletOutboxCount conta eventos financeiros relacionados à carteira pelo payload.
func readWalletOutboxCount(t *testing.T, pool *pgxpool.Pool, walletID string) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events
		WHERE payload -> 'data' ->> 'walletId' = $1`, walletID).Scan(&count); err != nil {
		t.Fatalf("read wallet outbox count: %v", err)
	}
	return count
}

// integrationEventIDs evita colisões entre eventos persistidos pelos cenários.
func integrationEventIDs(identity string) application.WagerEventIDs {
	return application.WagerEventIDs{Transaction: "event-transaction-" + identity, WalletBalance: "event-balance-" + identity}
}
