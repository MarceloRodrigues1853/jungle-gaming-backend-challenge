package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidWallet indica estado ou transição inválida da carteira.
	ErrInvalidWallet = errors.New("invalid wallet")
	// ErrInsufficientFunds indica que um débito excede o saldo disponível.
	ErrInsufficientFunds = errors.New("insufficient funds")
)

// Wallet é a raiz do agregado financeiro: saldo e versão só mudam por suas operações.
// Os campos privados protegem o estado contra alterações fora das regras do domínio.
type Wallet struct {
	id        string
	playerID  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWallet cria uma carteira com saldo zero e versão inicial igual a 1.
func NewWallet(id, playerID, currency string, now time.Time) (Wallet, error) {
	zero, err := Zero(currency)
	if err != nil {
		return Wallet{}, err
	}
	return RehydrateWallet(id, playerID, zero, 1, now, now)
}

// NewWalletWithBalance cria uma carteira já com o saldo de abertura e versão 1.
// A abertura não usa Credit porque a primeira versão representa o estado inicial.
func NewWalletWithBalance(id, playerID string, balance Money, now time.Time) (Wallet, error) {
	return RehydrateWallet(id, playerID, balance, 1, now, now)
}

// RehydrateWallet reconstrói uma carteira persistida sem reaplicar movimentações.
// A validação impede que dados inválidos do armazenamento entrem no domínio.
func RehydrateWallet(id, playerID string, balance Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(playerID) == "" {
		return Wallet{}, fmt.Errorf("%w: id and player id are required", ErrInvalidWallet)
	}
	if balance.IsNegative() {
		return Wallet{}, fmt.Errorf("%w: negative balance", ErrInvalidWallet)
	}
	if version < 1 {
		return Wallet{}, fmt.Errorf("%w: version must be positive", ErrInvalidWallet)
	}
	if createdAt.IsZero() || updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return Wallet{}, fmt.Errorf("%w: invalid timestamps", ErrInvalidWallet)
	}
	return Wallet{id: id, playerID: playerID, balance: balance, version: version, createdAt: createdAt, updatedAt: updatedAt}, nil
}

// ID retorna o identificador estável da carteira.
func (w Wallet) ID() string { return w.id }

// PlayerID retorna o jogador proprietário da carteira.
func (w Wallet) PlayerID() string { return w.playerID }

// Balance retorna o saldo atual como valor imutável.
func (w Wallet) Balance() Money { return w.balance }

// Version retorna a versão usada para controle de concorrência na persistência.
func (w Wallet) Version() int64 { return w.version }

// UpdatedAt retorna o instante da última alteração persistida do agregado.
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

// CreatedAt retorna o instante imutável de criação da carteira.
func (w Wallet) CreatedAt() time.Time { return w.createdAt }

// Credit credita um valor positivo e avança saldo, versão e instante de atualização.
func (w *Wallet) Credit(amount Money, now time.Time) error {
	if amount.IsNegative() || amount.IsZero() {
		return fmt.Errorf("%w: credit must be positive", ErrInvalidMoney)
	}
	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return err
	}
	return w.applyBalance(newBalance, now)
}

// Debit debita um valor positivo somente quando a carteira mantém saldo não negativo.
func (w *Wallet) Debit(amount Money, now time.Time) error {
	if amount.IsNegative() || amount.IsZero() {
		return fmt.Errorf("%w: debit must be positive", ErrInvalidMoney)
	}
	comparison, err := w.balance.Compare(amount)
	if err != nil {
		return err
	}
	if comparison < 0 {
		return ErrInsufficientFunds
	}
	newBalance, err := w.balance.Subtract(amount)
	if err != nil {
		return err
	}
	return w.applyBalance(newBalance, now)
}

// applyBalance aplica uma mudança validada e mantém versão e timestamp coerentes.
func (w *Wallet) applyBalance(balance Money, now time.Time) error {
	if now.Before(w.updatedAt) {
		return fmt.Errorf("%w: update time moved backwards", ErrInvalidWallet)
	}
	w.balance = balance
	w.version++
	w.updatedAt = now
	return nil
}
