package domain

import (
	"errors"
	"testing"
	"time"
)

func TestWalletDebitAndCredit(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	initial, _ := ParseMoney("100.00", "BRL")
	wallet, err := RehydrateWallet("wallet-1", "player-1", initial, 1, now, now)
	if err != nil {
		t.Fatal(err)
	}

	bet, _ := ParseMoney("80.00", "BRL")
	if err := wallet.Debit(bet, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Balance().String(); got != "20.00" {
		t.Fatalf("balance = %s, want 20.00", got)
	}
	if wallet.Version() != 2 {
		t.Fatalf("version = %d, want 2", wallet.Version())
	}

	if err := wallet.Debit(bet, now.Add(2*time.Second)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("second debit error = %v, want insufficient funds", err)
	}
	if wallet.Version() != 2 {
		t.Fatalf("rejected debit changed version to %d", wallet.Version())
	}

	win, _ := ParseMoney("10.00", "BRL")
	if err := wallet.Credit(win, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := wallet.Balance().String(); got != "30.00" {
		t.Fatalf("balance = %s, want 30.00", got)
	}
}

func TestWalletRejectsCurrencyMismatch(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	wallet, _ := NewWallet("wallet-1", "player-1", "BRL", now)
	usd, _ := ParseMoney("1.00", "USD")
	if err := wallet.Credit(usd, now); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Credit() error = %v, want currency mismatch", err)
	}
}

func TestLedgerEntryValidatesBalanceEquation(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	before, _ := ParseMoney("100.00", "BRL")
	movement, _ := ParseMoney("80.00", "BRL")
	after, _ := ParseMoney("20.00", "BRL")

	if _, err := NewWalletLedgerEntry("entry-1", "wallet-1", "tx-1", LedgerDebit, movement, before, after, now); err != nil {
		t.Fatalf("valid ledger entry rejected: %v", err)
	}

	wrongAfter, _ := ParseMoney("30.00", "BRL")
	if _, err := NewWalletLedgerEntry("entry-2", "wallet-1", "tx-2", LedgerDebit, movement, before, wrongAfter, now); !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Fatalf("inconsistent ledger error = %v", err)
	}
}
