package domain

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// transactionInput cria uma operação externa válida como base para cenários de teste.
// Cada teste altera somente os campos relevantes à regra que está verificando.
func transactionInput(kind TransactionKind, amount string) ExternalTransactionInput {
	money, err := ParseMoney(amount, "BRL")
	if err != nil {
		panic(err)
	}
	return ExternalTransactionInput{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "external-1",
		IdempotencyKey:        "provider-a:external-1",
		PayloadHash:           bytes.Repeat([]byte{0xA5}, 32),
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 money,
		Now:                   time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
	}
}

// TestNewExternalTransactionAmountRules verifica as regras de valor de cada tipo externo.
// A tabela cobre operações positivas, LOSS zerado e tipos que não podem vir de um provedor.
func TestNewExternalTransactionAmountRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    TransactionKind
		amount  string
		wantErr bool
	}{
		{name: "bet positive", kind: TransactionBet, amount: "1.00"},
		{name: "win positive", kind: TransactionWin, amount: "1.00"},
		{name: "refund positive", kind: TransactionRefund, amount: "1.00"},
		{name: "rollback positive", kind: TransactionRollback, amount: "1.00"},
		{name: "loss zero", kind: TransactionLoss, amount: "0.00"},
		{name: "loss positive", kind: TransactionLoss, amount: "1.00", wantErr: true},
		{name: "bet zero", kind: TransactionBet, amount: "0.00", wantErr: true},
		{name: "opening rejected as external", kind: TransactionOpening, amount: "1.00", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := transactionInput(tc.kind, tc.amount)
			if tc.kind == TransactionRefund || tc.kind == TransactionRollback {
				input.ReferenceExternalID = "external-original"
			}
			_, err := NewExternalTransaction(input)
			if tc.wantErr && !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("NewExternalTransaction() error = %v, want invalid transaction", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewExternalTransaction() error = %v", err)
			}
		})
	}
}

// TestNewExternalTransactionRequiresReversalReference garante que apenas reversões
// exijam referência externa e que uma referência ausente seja rejeitada.
func TestNewExternalTransactionRequiresReversalReference(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionRefund, "10.00")
	if _, err := NewExternalTransaction(input); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("refund without reference error = %v, want invalid transaction", err)
	}

	input = transactionInput(TransactionBet, "10.00")
	input.ReferenceExternalID = "unexpected-reference"
	if _, err := NewExternalTransaction(input); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("bet with reference error = %v, want invalid transaction", err)
	}
}

// TestNewExternalTransactionRequiresCompleteMetadata rejeita entradas sem dados
// necessários para rastreabilidade, idempotência e associação à rodada.
func TestNewExternalTransactionRequiresCompleteMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ExternalTransactionInput)
	}{
		{name: "missing provider", mutate: func(in *ExternalTransactionInput) { in.ProviderID = " " }},
		{name: "missing idempotency key", mutate: func(in *ExternalTransactionInput) { in.IdempotencyKey = "" }},
		{name: "invalid hash size", mutate: func(in *ExternalTransactionInput) { in.PayloadHash = []byte{1} }},
		{name: "missing round", mutate: func(in *ExternalTransactionInput) { in.RoundID = "" }},
		{name: "missing time", mutate: func(in *ExternalTransactionInput) { in.Now = time.Time{} }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := transactionInput(TransactionBet, "10.00")
			tc.mutate(&input)
			if _, err := NewExternalTransaction(input); !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("NewExternalTransaction() error = %v, want invalid transaction", err)
			}
		})
	}
}

// TestNewOpeningTransactionIsInternalAndPositive verifica que OPENING é uma operação
// interna válida e que saldo inicial zero não gera uma transação de abertura.
func TestNewOpeningTransactionIsInternalAndPositive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	amount, _ := ParseMoney("25.00", "BRL")
	tx, err := NewOpeningTransaction(OpeningTransactionInput{
		ID: "opening-1", WalletID: "wallet-1", PlayerID: "player-1", Money: amount, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Kind() != TransactionOpening || tx.Status() != TransactionPending {
		t.Fatalf("new opening = kind %s, status %s", tx.Kind(), tx.Status())
	}

	zero, _ := Zero("BRL")
	if _, err := NewOpeningTransaction(OpeningTransactionInput{
		ID: "opening-zero", WalletID: "wallet-1", PlayerID: "player-1", Money: zero, Now: now,
	}); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("zero opening error = %v, want invalid transaction", err)
	}
}

// TestWagerTransactionTransitionsAndTerminalState percorre uma reversão que aguarda
// sua referência, é resolvida e processada; depois confirma que o estado terminal não muda.
func TestWagerTransactionTransitionsAndTerminalState(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionRefund, "10.00")
	input.ReferenceExternalID = "external-original"
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}

	pendingAt := input.Now.Add(time.Second)
	// A referência ainda não chegou, então a transação precisa permanecer recuperável.
	if err := tx.MarkPendingReference(pendingAt); err != nil {
		t.Fatalf("MarkPendingReference() error = %v", err)
	}
	if tx.Status() != TransactionPendingReference {
		t.Fatalf("status = %s, want %s", tx.Status(), TransactionPendingReference)
	}

	resultBalance, _ := ParseMoney("110.00", "BRL")
	if err := tx.MarkProcessed(resultBalance, pendingAt.Add(time.Second)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("processing unresolved reference error = %v, want invalid transaction", err)
	}
	resolvedAt := pendingAt.Add(time.Second)
	// Ao localizar a operação original, guardar seu ID interno antes de processar a reversão.
	if err := tx.ResolveReference("tx-original", resolvedAt); err != nil {
		t.Fatalf("ResolveReference() error = %v", err)
	}
	if tx.Status() != TransactionPending || tx.ReferenceID() != "tx-original" {
		t.Fatalf("resolved state = %s/%q", tx.Status(), tx.ReferenceID())
	}
	processedAt := resolvedAt.Add(time.Second)
	if err := tx.MarkProcessed(resultBalance, processedAt); err != nil {
		t.Fatalf("MarkProcessed() error = %v", err)
	}
	stored, ok := tx.ResultBalance()
	if !ok || stored.String() != "110.00" {
		t.Fatalf("ResultBalance() = %s, %v; want 110.00, true", stored.String(), ok)
	}
	if !tx.ProcessedAt().Equal(processedAt) {
		t.Fatalf("ProcessedAt() = %s, want %s", tx.ProcessedAt(), processedAt)
	}
	if err := tx.MarkRejected("some-code", processedAt.Add(time.Second)); !errors.Is(err, ErrTerminalTransaction) {
		t.Fatalf("transition after processing error = %v, want terminal transaction", err)
	}
	invalidProcessed := tx.Snapshot()
	invalidProcessed.ReferenceID = ""
	if _, err := RehydrateWagerTransaction(invalidProcessed); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("processed reversal without resolved reference error = %v, want invalid transaction", err)
	}
}

// TestWagerTransactionFailureNeedsCodeAndDoesNotCarryResult verifica que rejeições e
// falhas precisam de código estável e não armazenam saldo de resultado bem-sucedido.
func TestWagerTransactionFailureNeedsCodeAndDoesNotCarryResult(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionBet, "10.00")
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkRejected(" ", input.Now.Add(time.Second)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("empty failure code error = %v, want invalid transaction", err)
	}
	if err := tx.MarkFailed("DATABASE_UNAVAILABLE", input.Now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if tx.Status() != TransactionFailed || tx.FailureCode() != "DATABASE_UNAVAILABLE" {
		t.Fatalf("failure state = %s/%q", tx.Status(), tx.FailureCode())
	}
	if _, ok := tx.ResultBalance(); ok {
		t.Fatal("failed transaction must not expose a result balance")
	}
}

// TestMarkProcessedRequiresCompatibleBalanceAndMonotonicTime impede registrar um
// resultado em moeda diferente ou com timestamp anterior ao da última transição.
func TestMarkProcessedRequiresCompatibleBalanceAndMonotonicTime(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionBet, "10.00")
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	usd, _ := ParseMoney("10.00", "USD")
	if err := tx.MarkProcessed(usd, input.Now.Add(time.Second)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("currency mismatch error = %v, want invalid transaction", err)
	}
	if err := tx.MarkProcessed(input.Money, input.Now.Add(-time.Second)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("backwards time error = %v, want invalid transaction", err)
	}
}

// TestTransactionPayloadHashIsCopied protege o hash contra mutação das fatias do
// chamador, tanto na criação quanto quando o hash é consultado.
func TestTransactionPayloadHashIsCopied(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionBet, "10.00")
	want := append([]byte(nil), input.PayloadHash...)
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	input.PayloadHash[0] = 0
	got, ok := tx.PayloadHash()
	if !ok || !bytes.Equal(got, want) {
		t.Fatal("transaction hash changed when the input slice changed")
	}
	got[0] = 0
	again, _ := tx.PayloadHash()
	if !bytes.Equal(again, want) {
		t.Fatal("transaction hash changed when the returned slice changed")
	}
}

// TestRehydrateWagerTransactionValidatesSnapshotWithoutReplaying confirma que um
// snapshot processado pode ser restaurado sem reaplicar a operação ou aceitar estado inválido.
func TestRehydrateWagerTransactionValidatesSnapshotWithoutReplaying(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionLoss, "0.00")
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	balance, _ := ParseMoney("100.00", "BRL")
	if err := tx.MarkProcessed(balance, input.Now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	rehydrated, err := RehydrateWagerTransaction(tx.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated.Status() != TransactionProcessed {
		t.Fatalf("rehydrated status = %s, want processed", rehydrated.Status())
	}
	if err := rehydrated.MarkProcessed(balance, input.Now.Add(2*time.Second)); !errors.Is(err, ErrTerminalTransaction) {
		t.Fatalf("reprocessing hydrated transaction error = %v, want terminal transaction", err)
	}

	invalid := tx.Snapshot()
	invalid.FailureCode = "STALE_FAILURE"
	if _, err := RehydrateWagerTransaction(invalid); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("invalid persisted snapshot error = %v, want invalid transaction", err)
	}
}

// TestResolvedReferenceSurvivesRehydration garante que o ID interno encontrado para
// uma reversão pendente sobrevive à persistência e à reidratação.
func TestResolvedReferenceSurvivesRehydration(t *testing.T) {
	t.Parallel()

	input := transactionInput(TransactionRollback, "10.00")
	input.ReferenceExternalID = "external-original"
	tx, err := NewExternalTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	now := input.Now.Add(time.Second)
	if err := tx.MarkPendingReference(now); err != nil {
		t.Fatal(err)
	}
	if err := tx.ResolveReference("internal-original", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	rehydrated, err := RehydrateWagerTransaction(tx.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated.ReferenceID() != "internal-original" || rehydrated.Status() != TransactionPending {
		t.Fatalf("rehydrated reference state = %s/%q", rehydrated.Status(), rehydrated.ReferenceID())
	}
}
