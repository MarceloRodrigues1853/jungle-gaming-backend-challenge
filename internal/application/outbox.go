package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

const integrationEventVersion = 1

// WagerEventIDs reserva identidades estáveis antes do início da transação SQL.
type WagerEventIDs struct {
	Transaction   string
	WalletBalance string
}

// BuildWagerOutboxEvents cria snapshots imutáveis dos eventos do resultado financeiro.
func BuildWagerOutboxEvents(transaction domain.WagerTransaction, result domain.WagerProcessingResult, ids WagerEventIDs, occurredAt time.Time) ([]OutboxEvent, error) {
	if strings.TrimSpace(ids.Transaction) == "" || occurredAt.IsZero() {
		return nil, errors.New("transaction event id and occurrence time are required")
	}
	eventType := ""
	switch result.Status {
	case domain.TransactionProcessed:
		eventType = "WagerTransactionProcessed"
	case domain.TransactionRejected:
		eventType = "WagerTransactionRejected"
	case domain.TransactionPendingReference:
		eventType = "WagerTransactionPendingReference"
	default:
		return nil, fmt.Errorf("unsupported outbox transaction status %q", result.Status)
	}

	transactionData := transactionEventData{
		TransactionID: transaction.ID(), ProviderID: transaction.ProviderID(),
		ExternalTransactionID: transaction.ExternalTransactionID(), WalletID: transaction.WalletID(),
		PlayerID: transaction.PlayerID(), Kind: string(transaction.Kind()), Status: string(result.Status),
		Money:       eventMoney{Amount: transaction.Money().String(), Currency: transaction.Money().Currency()},
		FailureCode: result.FailureCode,
	}
	correlationID := transaction.ExternalTransactionID()
	if correlationID == "" {
		correlationID = transaction.ID()
	}
	transactionEvent, err := marshalOutboxEvent(ids.Transaction, eventType, transaction.ID(), correlationID, occurredAt, transactionData)
	if err != nil {
		return nil, err
	}
	events := []OutboxEvent{transactionEvent}
	if result.LedgerEntry == nil {
		return events, nil
	}
	if strings.TrimSpace(ids.WalletBalance) == "" {
		return nil, errors.New("wallet balance event id is required for a financial movement")
	}
	entry := result.LedgerEntry
	balanceData := walletBalanceEventData{
		WalletID: entry.WalletID, TransactionID: entry.TransactionID, Direction: string(entry.Direction),
		Money:         eventMoney{Amount: entry.Money.String(), Currency: entry.Money.Currency()},
		BalanceBefore: eventMoney{Amount: entry.BalanceBefore.String(), Currency: entry.BalanceBefore.Currency()},
		BalanceAfter:  eventMoney{Amount: entry.BalanceAfter.String(), Currency: entry.BalanceAfter.Currency()},
		WalletVersion: result.WalletVersion,
	}
	balanceEvent, err := marshalOutboxEvent(ids.WalletBalance, "WalletBalanceChanged", transaction.WalletID(), transaction.ID(), occurredAt, balanceData)
	if err != nil {
		return nil, err
	}
	return append(events, balanceEvent), nil
}

// marshalOutboxEvent fixa o envelope comum e sua versão de contrato.
func marshalOutboxEvent(eventID, eventType, aggregateID, correlationID string, occurredAt time.Time, data any) (OutboxEvent, error) {
	envelope := integrationEventEnvelope{
		EventID: eventID, EventType: eventType, AggregateID: aggregateID,
		CorrelationID: correlationID, OccurredAt: occurredAt.UTC(), Version: integrationEventVersion, Data: data,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("encode %s event: %w", eventType, err)
	}
	return OutboxEvent{ID: eventID, AggregateID: aggregateID, Type: eventType, Payload: payload, OccurredAt: occurredAt.UTC()}, nil
}

type integrationEventEnvelope struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          any       `json:"data"`
}

type eventMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type transactionEventData struct {
	TransactionID         string     `json:"transactionId"`
	ProviderID            string     `json:"providerId,omitempty"`
	ExternalTransactionID string     `json:"externalTransactionId,omitempty"`
	WalletID              string     `json:"walletId"`
	PlayerID              string     `json:"playerId"`
	Kind                  string     `json:"kind"`
	Status                string     `json:"status"`
	Money                 eventMoney `json:"money"`
	FailureCode           string     `json:"failureCode,omitempty"`
}

type walletBalanceEventData struct {
	WalletID      string     `json:"walletId"`
	TransactionID string     `json:"transactionId"`
	Direction     string     `json:"direction"`
	Money         eventMoney `json:"money"`
	BalanceBefore eventMoney `json:"balanceBefore"`
	BalanceAfter  eventMoney `json:"balanceAfter"`
	WalletVersion int64      `json:"walletVersion"`
}
