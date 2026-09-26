package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

var (
	// ErrInvalidWalletCommand indica dados inválidos para abertura de carteira.
	ErrInvalidWalletCommand = errors.New("invalid wallet command")
	// ErrWalletConflict indica que jogador e moeda já possuem uma carteira.
	ErrWalletConflict = errors.New("wallet already exists")
)

// OpenWalletCommand representa a entrada interna de criação de carteira.
type OpenWalletCommand struct {
	PlayerID       string     `json:"playerId"`
	InitialBalance MoneyInput `json:"initialBalance"`
}

// OutboxEvent contém um evento durável que será publicado depois do commit.
type OutboxEvent struct {
	ID, AggregateID, Type string
	Payload               []byte
	OccurredAt            time.Time
}

// WalletRepository mantém criação e leitura desacopladas do PostgreSQL.
type WalletRepository interface {
	CreateWallet(context.Context, domain.Wallet, *domain.WagerTransaction, *domain.WalletLedgerEntry, []OutboxEvent) error
	GetWallet(context.Context, string) (domain.Wallet, bool, error)
}

// WalletService coordena a abertura atômica e a leitura de carteiras.
type WalletService struct {
	repository WalletRepository
	ids        IDGenerator
	clock      func() time.Time
}

// NewWalletService valida as dependências do caso de uso.
func NewWalletService(repository WalletRepository, ids IDGenerator, clock func() time.Time) (*WalletService, error) {
	if repository == nil || ids == nil {
		return nil, errors.New("wallet repository and id generator are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &WalletService{repository: repository, ids: ids, clock: clock}, nil
}

// Open cria a carteira; saldo positivo também gera OPENING, ledger e outbox.
func (service *WalletService) Open(ctx context.Context, command OpenWalletCommand) (domain.Wallet, error) {
	if service == nil || service.repository == nil || service.ids == nil || service.clock == nil {
		return domain.Wallet{}, errors.New("wallet service is not initialized")
	}
	if err := validateExactIdentifier("playerId", command.PlayerID); err != nil {
		return domain.Wallet{}, fmt.Errorf("%w: %v", ErrInvalidWalletCommand, err)
	}
	balance, err := domain.ParseMoney(command.InitialBalance.Amount, command.InitialBalance.Currency)
	if err != nil || balance.IsNegative() {
		return domain.Wallet{}, fmt.Errorf("%w: invalid initial balance", ErrInvalidWalletCommand)
	}
	now := service.clock().UTC()
	walletID, err := service.ids.NewID()
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("generate wallet id: %w", err)
	}
	wallet, err := domain.NewWalletWithBalance(walletID, command.PlayerID, balance, now)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("%w: %v", ErrInvalidWalletCommand, err)
	}

	var opening *domain.WagerTransaction
	var ledger *domain.WalletLedgerEntry
	var events []OutboxEvent
	if !balance.IsZero() {
		opening, ledger, events, err = service.openingArtifacts(wallet, now)
		if err != nil {
			return domain.Wallet{}, err
		}
	}
	if err := service.repository.CreateWallet(ctx, wallet, opening, ledger, events); err != nil {
		return domain.Wallet{}, err
	}
	return wallet, nil
}

// Get devolve a carteira por ID sem expor detalhes de persistência.
func (service *WalletService) Get(ctx context.Context, walletID string) (domain.Wallet, bool, error) {
	if strings.TrimSpace(walletID) == "" || strings.TrimSpace(walletID) != walletID {
		return domain.Wallet{}, false, ErrInvalidWalletCommand
	}
	return service.repository.GetWallet(ctx, walletID)
}

// openingArtifacts monta os registros financeiros exigidos para saldo positivo.
func (service *WalletService) openingArtifacts(wallet domain.Wallet, now time.Time) (*domain.WagerTransaction, *domain.WalletLedgerEntry, []OutboxEvent, error) {
	transactionID, err := service.ids.NewID()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate opening transaction id: %w", err)
	}
	transaction, err := domain.NewOpeningTransaction(domain.OpeningTransactionInput{
		ID: transactionID, WalletID: wallet.ID(), PlayerID: wallet.PlayerID(), Money: wallet.Balance(), Now: now,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create opening transaction: %w", err)
	}
	if err := transaction.MarkProcessed(wallet.Balance(), now); err != nil {
		return nil, nil, nil, fmt.Errorf("complete opening transaction: %w", err)
	}
	ledgerID, err := service.ids.NewID()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate opening ledger id: %w", err)
	}
	zero, _ := domain.Zero(wallet.Balance().Currency())
	entry, err := domain.NewWalletLedgerEntry(ledgerID, wallet.ID(), transaction.ID(), domain.LedgerCredit, wallet.Balance(), zero, wallet.Balance(), now)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create opening ledger: %w", err)
	}
	events, err := service.openingEvents(wallet, transaction, now)
	if err != nil {
		return nil, nil, nil, err
	}
	return &transaction, &entry, events, nil
}

// openingEvents usa JSON determinístico e IDs estáveis gerados antes da transação SQL.
func (service *WalletService) openingEvents(wallet domain.Wallet, transaction domain.WagerTransaction, now time.Time) ([]OutboxEvent, error) {
	types := []string{"WagerTransactionProcessed", "WalletBalanceChanged"}
	events := make([]OutboxEvent, 0, len(types))
	for _, eventType := range types {
		eventID, err := service.ids.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate outbox event id: %w", err)
		}
		payload, err := json.Marshal(openingEventPayload{
			EventID: eventID, Type: eventType, WalletID: wallet.ID(), PlayerID: wallet.PlayerID(),
			TransactionID: transaction.ID(), Amount: wallet.Balance().String(),
			Currency: wallet.Balance().Currency(), Version: wallet.Version(), OccurredAt: now,
		})
		if err != nil {
			return nil, fmt.Errorf("encode opening event: %w", err)
		}
		events = append(events, OutboxEvent{ID: eventID, AggregateID: wallet.ID(), Type: eventType, Payload: payload, OccurredAt: now})
	}
	return events, nil
}

// openingEventPayload mantém o contrato do evento explícito e revisável.
type openingEventPayload struct {
	EventID       string    `json:"eventId"`
	Type          string    `json:"type"`
	WalletID      string    `json:"walletId"`
	PlayerID      string    `json:"playerId"`
	TransactionID string    `json:"transactionId"`
	Amount        string    `json:"amount"`
	Currency      string    `json:"currency"`
	Version       int64     `json:"version"`
	OccurredAt    time.Time `json:"occurredAt"`
}
