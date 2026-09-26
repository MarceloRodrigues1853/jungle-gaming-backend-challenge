package application

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestBuildWagerOutboxEventsCreatesTypedSnapshots valida os dois eventos de uma movimentação.
func TestBuildWagerOutboxEventsCreatesTypedSnapshots(t *testing.T) {
	t.Parallel()
	now := fixedClock()
	money, _ := domain.ParseMoney("25.00", "BRL")
	before, _ := domain.ParseMoney("100.00", "BRL")
	after, _ := domain.ParseMoney("75.00", "BRL")
	hash := bytes.Repeat([]byte{1}, 32)
	transaction, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "tx-1", ProviderID: "provider-a", ExternalTransactionID: "external-1",
		IdempotencyKey: "provider-a:external-1", PayloadHash: hash,
		WalletID: "wallet-1", PlayerID: "player-1", RoundID: "round-1", GameID: "game-1",
		Kind: domain.TransactionBet, Money: money, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.MarkProcessed(after, now); err != nil {
		t.Fatal(err)
	}
	ledger, _ := domain.NewWalletLedgerEntry("ledger-1", "wallet-1", "tx-1", domain.LedgerDebit, money, before, after, now)
	result := domain.WagerProcessingResult{
		TransactionID: "tx-1", Status: domain.TransactionProcessed, Balance: after,
		HasBalance: true, WalletVersion: 2, LedgerEntry: &ledger,
	}
	events, err := BuildWagerOutboxEvents(transaction, result, WagerEventIDs{Transaction: "event-1", WalletBalance: "event-2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "WagerTransactionProcessed" || events[1].Type != "WalletBalanceChanged" {
		t.Fatalf("events = %#v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	data := payload["data"].(map[string]any)
	if payload["version"] != float64(1) || data["direction"] != "DEBIT" || data["walletVersion"] != float64(2) {
		t.Fatalf("wallet event payload = %s", events[1].Payload)
	}
}

// TestBuildRejectedEventDoesNotClaimBalanceChange evita evento financeiro em rejeições.
func TestBuildRejectedEventDoesNotClaimBalanceChange(t *testing.T) {
	t.Parallel()
	command := validSubmitCommand()
	money, _ := domain.ParseMoney(command.Money.Amount, command.Money.Currency)
	hash, _ := HashWagerPayload("provider-a", command, money)
	transaction, _ := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: "tx-rejected", ProviderID: "provider-a", ExternalTransactionID: command.ExternalTransactionID,
		IdempotencyKey: "key", PayloadHash: hash[:], WalletID: command.WalletID, PlayerID: command.PlayerID,
		RoundID: command.RoundID, GameID: command.GameID, Kind: domain.TransactionBet, Money: money, Now: fixedClock(),
	})
	if err := transaction.MarkRejected(domain.FailureInsufficientFunds, fixedClock()); err != nil {
		t.Fatal(err)
	}
	events, err := BuildWagerOutboxEvents(transaction, domain.WagerProcessingResult{
		TransactionID: transaction.ID(), Status: domain.TransactionRejected, FailureCode: domain.FailureInsufficientFunds,
	}, WagerEventIDs{Transaction: "event-rejected", WalletBalance: "unused"}, fixedClock())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "WagerTransactionRejected" {
		t.Fatalf("rejected events = %#v", events)
	}
}
