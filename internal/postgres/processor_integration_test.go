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

// TestPostgresRejectsConcurrentDoubleReversal demonstra que duas instâncias não
// conseguem devolver a mesma operação e que a perdedora recebe código auditável.
func TestPostgresRejectsConcurrentDoubleReversal(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	bet := integrationTransaction(t, "bet-double-reversal", "external-bet-double-reversal",
		"provider-a:bet-double-reversal", walletID, playerID, domain.TransactionBet, "25.00", "bet-double-reversal", now)
	if _, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second)); err != nil {
		t.Fatalf("process referenced BET: %v", err)
	}
	reference, exists, err := store.FindProviderTransaction(context.Background(), "provider-a", bet.ExternalTransactionID())
	if err != nil || !exists {
		t.Fatalf("load processed BET reference = exists %v, error %v", exists, err)
	}

	reversals := []domain.WagerTransaction{
		integrationReversal(t, "refund-double-a", "external-refund-double-a", walletID, playerID, reference, now.Add(2*time.Second)),
		integrationReversal(t, "rollback-double-b", "external-rollback-double-b", walletID, playerID, reference, now.Add(2*time.Second)),
	}
	results := make([]domain.WagerProcessingResult, len(reversals))
	errorsByReversal := make([]error, len(reversals))
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range reversals {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsByReversal[index] = store.ProcessWagerTransaction(context.Background(), reversals[index], &reference,
				"ledger-"+reversals[index].ID(), integrationEventIDs(reversals[index].ID()), nil, now.Add(3*time.Second))
		}(index)
	}
	close(start)
	group.Wait()

	processed, rejected := 0, 0
	for index, processErr := range errorsByReversal {
		if processErr != nil {
			t.Fatalf("reversal %d returned infrastructure error: %v", index, processErr)
		}
		switch results[index].Status {
		case domain.TransactionProcessed:
			processed++
		case domain.TransactionRejected:
			rejected++
			if results[index].FailureCode != domain.FailureReversalAlreadyProcessed {
				t.Fatalf("reversal %d failure code = %q", index, results[index].FailureCode)
			}
		default:
			t.Fatalf("reversal %d status = %s", index, results[index].Status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected reversals = %d/%d, want 1/1", processed, rejected)
	}
	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 10_000 || ledgerCount != 2 {
		t.Fatalf("balance/ledger after double reversal = %d/%d, want 10000/2", balance, ledgerCount)
	}
	if outboxCount := readWalletOutboxCount(t, pool, walletID); outboxCount != 5 {
		t.Fatalf("outbox events after double reversal = %d, want 5", outboxCount)
	}
}

// TestPostgresResolvesOutOfOrderRefund valida a chegada da reversão antes da
// aposta, seguida de retomada durável por outro worker.
func TestPostgresResolvesOutOfOrderRefund(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	identity := strings.TrimPrefix(walletID, "it-wallet-")

	refundMoney, _ := domain.ParseMoney("25.00", "BRL")
	refundHash := sha256.Sum256([]byte("early-refund-" + identity))
	refund, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "early-refund-" + identity, ProviderID: "provider-a",
		ExternalTransactionID: "external-refund-" + identity,
		IdempotencyKey:        "provider-a:refund-" + identity, PayloadHash: refundHash[:],
		WalletID: walletID, PlayerID: playerID, RoundID: "it-round", GameID: "it-game",
		Kind: domain.TransactionRefund, Money: refundMoney,
		ReferenceExternalID: "external-late-bet-" + identity, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refund.MarkPendingReference(now); err != nil {
		t.Fatal(err)
	}
	result, err := store.ProcessWagerTransaction(context.Background(), refund, nil, "", integrationEventIDs(refund.ID()), nil, now)
	if err != nil || result.Status != domain.TransactionPendingReference {
		t.Fatalf("persist pending refund = status %s, error %v", result.Status, err)
	}

	bet := integrationTransaction(t, "late-bet", "external-late-bet", "provider-a:late-bet", walletID, playerID, domain.TransactionBet, "25.00", "late-bet-payload", now.Add(time.Second))
	if _, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second)); err != nil {
		t.Fatalf("process late BET: %v", err)
	}
	claimed, err := store.ClaimPendingReferences(context.Background(), "reference-worker-test", now.Add(2*time.Second), 30*time.Second, 10)
	if err != nil || len(claimed) != 1 || claimed[0].TransactionID != refund.ID() {
		t.Fatalf("claimed references = %+v, error %v", claimed, err)
	}
	resolved, err := store.ResolvePendingReference(context.Background(), refund.ID(), "reference-worker-test",
		"ledger-"+refund.ID(), integrationEventIDs("resolved-"+refund.ID()), now.Add(2*time.Second))
	if err != nil || !resolved {
		t.Fatalf("ResolvePendingReference() = %v, %v", resolved, err)
	}
	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 10_000 || ledgerCount != 2 {
		t.Fatalf("balance/ledger = %d/%d, want 10000/2", balance, ledgerCount)
	}
	var status, referenceID string
	if err := pool.QueryRow(context.Background(), `SELECT status, reference_transaction_id
		FROM wager_transactions WHERE id = $1`, refund.ID()).Scan(&status, &referenceID); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.TransactionProcessed) || referenceID != bet.ID() {
		t.Fatalf("refund status/reference = %s/%s", status, referenceID)
	}
}

// TestPostgresRejectsOutOfOrderReversalWhenReferenceWasAlreadyReversed garante
// a mesma política quando a operação é retomada pelo worker de referências.
func TestPostgresRejectsOutOfOrderReversalWhenReferenceWasAlreadyReversed(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	identity := strings.TrimPrefix(walletID, "it-wallet-")

	money, _ := domain.ParseMoney("25.00", "BRL")
	pendingHash := sha256.Sum256([]byte("pending-duplicate-reversal-" + identity))
	pending, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "pending-duplicate-reversal-" + identity, ProviderID: "provider-a",
		ExternalTransactionID: "external-pending-duplicate-" + identity,
		IdempotencyKey:        "provider-a:pending-duplicate-" + identity, PayloadHash: pendingHash[:],
		WalletID: walletID, PlayerID: playerID, RoundID: "it-round", GameID: "it-game",
		Kind: domain.TransactionRefund, Money: money,
		ReferenceExternalID: "external-late-duplicate-bet-" + identity, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.MarkPendingReference(now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProcessWagerTransaction(context.Background(), pending, nil, "", integrationEventIDs(pending.ID()), nil, now); err != nil {
		t.Fatalf("persist pending reversal: %v", err)
	}

	bet := integrationTransaction(t, "late-duplicate-bet", "external-late-duplicate-bet",
		"provider-a:late-duplicate-bet", walletID, playerID, domain.TransactionBet, "25.00", "late-duplicate-bet", now.Add(time.Second))
	if _, err := store.ProcessWagerTransaction(context.Background(), bet, nil, "ledger-"+bet.ID(), integrationEventIDs(bet.ID()), nil, now.Add(time.Second)); err != nil {
		t.Fatalf("process late BET: %v", err)
	}
	reference, exists, err := store.FindProviderTransaction(context.Background(), "provider-a", bet.ExternalTransactionID())
	if err != nil || !exists {
		t.Fatalf("load late BET = exists %v, error %v", exists, err)
	}
	firstReversal := integrationReversal(t, "rollback-before-worker", "external-rollback-before-worker",
		walletID, playerID, reference, now.Add(2*time.Second))
	if _, err := store.ProcessWagerTransaction(context.Background(), firstReversal, &reference,
		"ledger-"+firstReversal.ID(), integrationEventIDs(firstReversal.ID()), nil, now.Add(3*time.Second)); err != nil {
		t.Fatalf("process first reversal: %v", err)
	}

	claimed, err := store.ClaimPendingReferences(context.Background(), "duplicate-reference-worker", now.Add(4*time.Second), 30*time.Second, 10)
	if err != nil || len(claimed) != 1 || claimed[0].TransactionID != pending.ID() {
		t.Fatalf("claimed pending reversal = %+v, error %v", claimed, err)
	}
	resolved, err := store.ResolvePendingReference(context.Background(), pending.ID(), "duplicate-reference-worker",
		"ledger-"+pending.ID(), integrationEventIDs("resolved-"+pending.ID()), now.Add(4*time.Second))
	if err != nil || !resolved {
		t.Fatalf("resolve duplicate pending reversal = %v, %v", resolved, err)
	}

	var status, failureCode string
	if err := pool.QueryRow(context.Background(), `SELECT status, failure_code FROM wager_transactions WHERE id = $1`, pending.ID()).Scan(&status, &failureCode); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.TransactionRejected) || failureCode != domain.FailureReversalAlreadyProcessed {
		t.Fatalf("pending reversal status/failure = %s/%s", status, failureCode)
	}
	balance, ledgerCount := readWalletAndLedger(t, pool, walletID)
	if balance != 10_000 || ledgerCount != 2 {
		t.Fatalf("balance/ledger after pending duplicate = %d/%d, want 10000/2", balance, ledgerCount)
	}
}

// TestPostgresRejectsExpiredPendingReference registra resultado terminal e
// libera a mensagem original que já foi concluída pela inbox.
func TestPostgresRejectsExpiredPendingReference(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID, playerID := seedIntegrationWallet(t, pool, 10_000, now)
	identity := strings.TrimPrefix(walletID, "it-wallet-")
	money, _ := domain.ParseMoney("25.00", "BRL")
	hash := sha256.Sum256([]byte("expired-refund-" + identity))
	refund, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "expired-refund-" + identity, ProviderID: "provider-a",
		ExternalTransactionID: "expired-external-" + identity,
		IdempotencyKey:        "provider-a:expired-" + identity, PayloadHash: hash[:],
		WalletID: walletID, PlayerID: playerID, RoundID: "it-round", GameID: "it-game",
		Kind: domain.TransactionRefund, Money: money,
		ReferenceExternalID: "never-arrives-" + identity, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := refund.MarkPendingReference(now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProcessWagerTransaction(context.Background(), refund, nil, "", integrationEventIDs(refund.ID()), nil, now); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `UPDATE wager_transactions
		SET next_reference_attempt_at = $1, reference_expires_at = $1 WHERE id = $2`, now.Add(-time.Second), refund.ID())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingReferences(context.Background(), "expiry-worker", now, 30*time.Second, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim expired = %+v, %v", claimed, err)
	}
	if err := store.RejectPendingReference(context.Background(), refund.ID(), "expiry-worker", "expired-event-"+identity, now); err != nil {
		t.Fatalf("RejectPendingReference() error = %v", err)
	}
	var status, failure string
	if err := pool.QueryRow(context.Background(), `SELECT status, failure_code FROM wager_transactions WHERE id = $1`, refund.ID()).Scan(&status, &failure); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.TransactionRejected) || failure != application.FailureReferenceNotFound {
		t.Fatalf("status/failure = %s/%s", status, failure)
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

// integrationReversal cria e vincula uma reversão válida para cenários concorrentes.
func integrationReversal(t *testing.T, id, externalID, walletID, playerID string, reference domain.WagerTransaction, now time.Time) domain.WagerTransaction {
	t.Helper()
	identity := strings.TrimPrefix(walletID, "it-wallet-")
	hash := sha256.Sum256([]byte(id + "-payload-" + identity))
	money, err := domain.ParseMoney(reference.Money().String(), reference.Money().Currency())
	if err != nil {
		t.Fatal(err)
	}
	kind := domain.TransactionRefund
	if strings.HasPrefix(id, "rollback") {
		kind = domain.TransactionRollback
	}
	transaction, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: id + "-" + identity, ProviderID: "provider-a",
		ExternalTransactionID: externalID + "-" + identity,
		IdempotencyKey:        "provider-a:" + externalID + "-" + identity, PayloadHash: hash[:],
		WalletID: walletID, PlayerID: playerID, RoundID: "it-round", GameID: "it-game",
		Kind: kind, Money: money, ReferenceExternalID: reference.ExternalTransactionID(), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.ResolveReference(reference, now); err != nil {
		t.Fatal(err)
	}
	return transaction
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
