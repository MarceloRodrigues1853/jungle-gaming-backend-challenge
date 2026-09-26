package application

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestHashWagerPayloadHasStableContract fixa o resultado para detectar mudanças acidentais no contrato.
func TestHashWagerPayloadHasStableContract(t *testing.T) {
	t.Parallel()

	command := validSubmitCommand()
	money, err := domain.ParseMoney(command.Money.Amount, command.Money.Currency)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashWagerPayload("provider-a", command, money)
	if err != nil {
		t.Fatal(err)
	}
	const expected = "1d689ea6a8af5cc761f1297123e1b1be1839a60011063e3ba01ea186324e60c8"
	if hex.EncodeToString(hash[:]) != expected {
		t.Fatalf("canonical hash = %x, want %s", hash, expected)
	}
}

// TestSubmitBuildsTheSamePayloadForHTTPAndSQS comprova que o transporte não participa do hash.
func TestSubmitBuildsTheSamePayloadForHTTPAndSQS(t *testing.T) {
	t.Parallel()

	command := validSubmitCommand()
	firstProcessor := &processorSpy{}
	first := newTestWagerService(t, firstProcessor, &sequenceIDs{values: []string{"tx-http", "ledger-http"}})
	if _, err := first.Submit(context.Background(), "provider-a", "key-from-http", command); err != nil {
		t.Fatalf("HTTP-like Submit() error = %v", err)
	}

	secondProcessor := &processorSpy{}
	second := newTestWagerService(t, secondProcessor, &sequenceIDs{values: []string{"tx-sqs", "ledger-sqs"}})
	if _, err := second.Submit(context.Background(), "provider-a", "key-from-sqs", command); err != nil {
		t.Fatalf("SQS-like Submit() error = %v", err)
	}

	firstHash, _ := firstProcessor.transaction.PayloadHash()
	secondHash, _ := secondProcessor.transaction.PayloadHash()
	if !bytes.Equal(firstHash, secondHash) {
		t.Fatalf("payload hashes differ: %x != %x", firstHash, secondHash)
	}
	if firstProcessor.transaction.IdempotencyKey() == secondProcessor.transaction.IdempotencyKey() {
		t.Fatal("test must use distinct transport idempotency keys")
	}
}

// TestSubmitUsesAuthenticatedProviderInHash garante isolamento entre provedores.
func TestSubmitUsesAuthenticatedProviderInHash(t *testing.T) {
	t.Parallel()

	command := validSubmitCommand()
	money, err := domain.ParseMoney(command.Money.Amount, command.Money.Currency)
	if err != nil {
		t.Fatal(err)
	}
	first, err := HashWagerPayload("provider-a", command, money)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashWagerPayload("provider-b", command, money)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first[:], second[:]) {
		t.Fatal("different authenticated providers must produce different payload hashes")
	}
}

// TestSubmitRejectsInvalidInputBeforePersistence evita gravar valores ambíguos.
func TestSubmitRejectsInvalidInputBeforePersistence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*SubmitWagerCommand)
	}{
		{name: "surrounding whitespace", prepare: func(command *SubmitWagerCommand) { command.PlayerID = " player-1" }},
		{name: "float-like excess scale", prepare: func(command *SubmitWagerCommand) { command.Money.Amount = "25.001" }},
		{name: "opening is not external", prepare: func(command *SubmitWagerCommand) { command.Kind = "OPENING" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			processor := &processorSpy{}
			service := newTestWagerService(t, processor, &sequenceIDs{values: []string{"tx", "ledger"}})
			command := validSubmitCommand()
			test.prepare(&command)
			if _, err := service.Submit(context.Background(), "provider-a", "provider-a:external-1", command); !errors.Is(err, ErrInvalidWagerCommand) {
				t.Fatalf("Submit() error = %v, want invalid command", err)
			}
			if processor.called {
				t.Fatal("invalid command reached persistence")
			}
		})
	}
}

// TestSubmitResolvesReferenceWithinAuthenticatedProvider verifica o escopo da busca.
func TestSubmitResolvesReferenceWithinAuthenticatedProvider(t *testing.T) {
	t.Parallel()

	command := validSubmitCommand()
	command.Kind = "REFUND"
	command.ReferenceExternalID = "bet-original"
	reference := processedReference(t, command)
	finder := &referenceSpy{transaction: reference}
	processor := &processorSpy{}
	service, err := NewWagerService(processor, finder, &sequenceIDs{values: []string{"refund-id", "refund-ledger"}}, fixedClock)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Submit(context.Background(), "provider-a", "provider-a:refund-1", command); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if finder.providerID != "provider-a" || finder.externalID != "bet-original" {
		t.Fatalf("reference lookup = %q/%q", finder.providerID, finder.externalID)
	}
	if processor.reference == nil || processor.reference.ID() != reference.ID() {
		t.Fatal("resolved reference was not forwarded to processor")
	}
}

// processorSpy captura a chamada que futuramente será compartilhada por HTTP e SQS.
type processorSpy struct {
	called      bool
	transaction domain.WagerTransaction
	reference   *domain.WagerTransaction
	ledgerID    string
}

func (spy *processorSpy) ProcessWagerTransaction(_ context.Context, transaction domain.WagerTransaction, reference *domain.WagerTransaction, ledgerID string, _ time.Time) (domain.WagerProcessingResult, error) {
	spy.called, spy.transaction, spy.reference, spy.ledgerID = true, transaction, reference, ledgerID
	return domain.WagerProcessingResult{TransactionID: transaction.ID()}, nil
}

// referenceSpy registra o provedor usado para evitar busca cruzada entre clientes.
type referenceSpy struct {
	providerID, externalID string
	transaction            domain.WagerTransaction
}

func (spy *referenceSpy) FindProviderTransaction(_ context.Context, providerID, externalID string) (domain.WagerTransaction, error) {
	spy.providerID, spy.externalID = providerID, externalID
	return spy.transaction, nil
}

// sequenceIDs torna os identificadores previsíveis nos testes do caso de uso.
type sequenceIDs struct {
	values []string
	next   int
}

func (generator *sequenceIDs) NewID() string {
	value := generator.values[generator.next]
	generator.next++
	return value
}

// newTestWagerService cria o serviço com relógio determinístico.
func newTestWagerService(t *testing.T, processor TransactionProcessor, ids IDGenerator) *WagerService {
	t.Helper()
	service, err := NewWagerService(processor, nil, ids, fixedClock)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// validSubmitCommand devolve uma aposta válida para variar somente o campo testado.
func validSubmitCommand() SubmitWagerCommand {
	return SubmitWagerCommand{
		ExternalTransactionID: "external-1", PlayerID: "player-1", WalletID: "wallet-1",
		RoundID: "round-1", GameID: "game-1", Kind: "BET",
		Money: MoneyInput{Amount: "25.00", Currency: "BRL"},
	}
}

// processedReference monta a aposta terminal usada pelo cenário de reembolso.
func processedReference(t *testing.T, command SubmitWagerCommand) domain.WagerTransaction {
	t.Helper()
	money, _ := domain.ParseMoney(command.Money.Amount, command.Money.Currency)
	balance, _ := domain.ParseMoney("75.00", "BRL")
	reference, err := domain.RehydrateWagerTransaction(domain.WagerTransactionSnapshot{
		ID: "bet-id", ProviderID: "provider-a", ExternalTransactionID: "bet-original",
		IdempotencyKey: "provider-a:bet-original", PayloadHash: bytes.Repeat([]byte{1}, 32),
		WalletID: command.WalletID, PlayerID: command.PlayerID, RoundID: command.RoundID,
		GameID: command.GameID, Kind: domain.TransactionBet, Money: money,
		Status: domain.TransactionProcessed, ResultBalance: balance, HasResultBalance: true,
		CreatedAt: fixedClock(), UpdatedAt: fixedClock(), ProcessedAt: fixedClock(),
	})
	if err != nil {
		t.Fatalf("RehydrateWagerTransaction() error = %v", err)
	}
	return reference
}

// fixedClock evita que horário e fuso tornem o teste instável.
func fixedClock() time.Time {
	return time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC)
}
