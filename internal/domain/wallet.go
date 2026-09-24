package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidWallet     = errors.New("invalid wallet")
	ErrInsufficientFunds = errors.New("insufficient funds")
)

type Wallet struct {
	id        string
	playerID  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func NewWallet(id, playerID, currency string, now time.Time) (Wallet, error) {
	zero, err := Zero(currency)
	if err != nil {
		return Wallet{}, err
	}
	return RehydrateWallet(id, playerID, zero, 1, now, now)
}

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

func (w Wallet) ID() string { return w.id }

func (w Wallet) PlayerID() string { return w.playerID }

func (w Wallet) Balance() Money { return w.balance }

func (w Wallet) Version() int64 { return w.version }

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

func (w *Wallet) applyBalance(balance Money, now time.Time) error {
	if now.Before(w.updatedAt) {
		return fmt.Errorf("%w: update time moved backwards", ErrInvalidWallet)
	}
	w.balance = balance
	w.version++
	w.updatedAt = now
	return nil
}
