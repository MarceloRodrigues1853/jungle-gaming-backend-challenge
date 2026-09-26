// Package application coordena os casos de uso sem depender de HTTP ou mensageria.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

var (
	// ErrInvalidWagerCommand indica uma entrada externa incompleta ou malformada.
	ErrInvalidWagerCommand = errors.New("invalid wager command")
	// ErrReferenceNotFound indica que a operação referenciada ainda não está disponível.
	ErrReferenceNotFound = errors.New("referenced transaction not found")
)

// MoneyInput representa o contrato textual de dinheiro compartilhado por HTTP e SQS.
type MoneyInput struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// SubmitWagerCommand contém apenas os campos de negócio recebidos externamente.
// ProviderID e IdempotencyKey não pertencem ao corpo: vêm da identidade e do transporte.
type SubmitWagerCommand struct {
	ExternalTransactionID string     `json:"externalTransactionId"`
	PlayerID              string     `json:"playerId"`
	WalletID              string     `json:"walletId"`
	RoundID               string     `json:"roundId"`
	GameID                string     `json:"gameId"`
	Kind                  string     `json:"kind"`
	Money                 MoneyInput `json:"money"`
	ReferenceExternalID   string     `json:"referenceExternalTransactionId,omitempty"`
}

// TransactionProcessor persiste e processa a operação financeira atomicamente.
type TransactionProcessor interface {
	ProcessWagerTransaction(context.Context, domain.WagerTransaction, *domain.WagerTransaction, string, time.Time) (domain.WagerProcessingResult, error)
}

// ReferenceFinder localiza uma operação já persistida no mesmo provedor.
type ReferenceFinder interface {
	FindProviderTransaction(context.Context, string, string) (domain.WagerTransaction, error)
}

// IDGenerator produz identificadores internos sem acoplar o caso de uso a uma biblioteca.
type IDGenerator interface {
	NewID() string
}

// WagerService prepara a entrada comum e delega a atomicidade ao processador persistente.
type WagerService struct {
	processor  TransactionProcessor
	references ReferenceFinder
	ids        IDGenerator
	clock      func() time.Time
}

// NewWagerService valida e cria o caso de uso compartilhado pelos adaptadores de entrada.
func NewWagerService(processor TransactionProcessor, references ReferenceFinder, ids IDGenerator, clock func() time.Time) (*WagerService, error) {
	if processor == nil || ids == nil {
		return nil, errors.New("wager processor and id generator are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &WagerService{processor: processor, references: references, ids: ids, clock: clock}, nil
}

// Submit valida, canonicaliza e processa uma operação em nome do provedor autenticado.
func (service *WagerService) Submit(ctx context.Context, providerID, idempotencyKey string, command SubmitWagerCommand) (domain.WagerProcessingResult, error) {
	if service == nil || service.processor == nil || service.ids == nil || service.clock == nil {
		return domain.WagerProcessingResult{}, errors.New("wager service is not initialized")
	}
	if err := validateExactIdentifier("providerId", providerID); err != nil {
		return domain.WagerProcessingResult{}, err
	}
	if err := validateExactIdentifier("idempotencyKey", idempotencyKey); err != nil {
		return domain.WagerProcessingResult{}, err
	}
	if err := validateCommand(command); err != nil {
		return domain.WagerProcessingResult{}, err
	}

	money, err := domain.ParseMoney(command.Money.Amount, command.Money.Currency)
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("%w: %v", ErrInvalidWagerCommand, err)
	}
	payloadHash, err := HashWagerPayload(providerID, command, money)
	if err != nil {
		return domain.WagerProcessingResult{}, err
	}

	now := service.clock().UTC()
	transaction, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
		ID: service.ids.NewID(), ProviderID: providerID,
		ExternalTransactionID: command.ExternalTransactionID, IdempotencyKey: idempotencyKey,
		PayloadHash: payloadHash[:], WalletID: command.WalletID, PlayerID: command.PlayerID,
		RoundID: command.RoundID, GameID: command.GameID, Kind: domain.TransactionKind(command.Kind),
		Money: money, ReferenceExternalID: command.ReferenceExternalID, Now: now,
	})
	if err != nil {
		return domain.WagerProcessingResult{}, fmt.Errorf("%w: %v", ErrInvalidWagerCommand, err)
	}

	var reference *domain.WagerTransaction
	if command.ReferenceExternalID != "" {
		if service.references == nil {
			return domain.WagerProcessingResult{}, errors.New("reference finder is required for referenced operations")
		}
		found, err := service.references.FindProviderTransaction(ctx, providerID, command.ReferenceExternalID)
		if err != nil {
			return domain.WagerProcessingResult{}, err
		}
		reference = &found
	}

	ledgerEntryID := ""
	if transaction.Kind() != domain.TransactionLoss {
		ledgerEntryID = service.ids.NewID()
	}
	return service.processor.ProcessWagerTransaction(ctx, transaction, reference, ledgerEntryID, now)
}

// HashWagerPayload gera SHA-256 sobre JSON canônico com todos os campos de negócio.
// A chave de idempotência e metadados de transporte são intencionalmente excluídos.
func HashWagerPayload(providerID string, command SubmitWagerCommand, money domain.Money) ([sha256.Size]byte, error) {
	payload := canonicalWagerPayload{
		ProviderID: providerID, ExternalTransactionID: command.ExternalTransactionID,
		PlayerID: command.PlayerID, WalletID: command.WalletID, RoundID: command.RoundID,
		GameID: command.GameID, Kind: command.Kind, Amount: money.String(), Currency: money.Currency(),
		ReferenceExternalID: command.ReferenceExternalID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("encode canonical wager payload: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

// canonicalWagerPayload usa struct para manter nomes e ordem de campos estáveis no JSON.
type canonicalWagerPayload struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Amount                string `json:"amount"`
	Currency              string `json:"currency"`
	ReferenceExternalID   string `json:"referenceExternalTransactionId"`
}

// validateCommand rejeita normalizações silenciosas que poderiam alterar o hash entre transportes.
func validateCommand(command SubmitWagerCommand) error {
	fields := []struct{ name, value string }{
		{"externalTransactionId", command.ExternalTransactionID}, {"playerId", command.PlayerID},
		{"walletId", command.WalletID}, {"roundId", command.RoundID}, {"gameId", command.GameID},
		{"kind", command.Kind}, {"money.amount", command.Money.Amount}, {"money.currency", command.Money.Currency},
	}
	for _, field := range fields {
		if err := validateExactIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	if command.ReferenceExternalID != "" && strings.TrimSpace(command.ReferenceExternalID) != command.ReferenceExternalID {
		return fmt.Errorf("%w: referenceExternalTransactionId must not contain surrounding whitespace", ErrInvalidWagerCommand)
	}
	return nil
}

// validateExactIdentifier exige valor não vazio sem modificar a entrada recebida.
func validateExactIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is empty or contains surrounding whitespace", ErrInvalidWagerCommand, name)
	}
	return nil
}
