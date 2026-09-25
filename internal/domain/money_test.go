package domain

import (
	"errors"
	"math"
	"testing"
)

// TestParseMoney cobre entradas aceitas e formatos externos que devem ser rejeitados.
func TestParseMoney(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   string
		currency string
		want     int64
		wantErr  error
	}{
		{name: "valid", amount: "25.00", currency: "BRL", want: 2500},
		{name: "zero", amount: "0.00", currency: "BRL", want: 0},
		{name: "leading zero", amount: "025.00", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "negative external value", amount: "-1.00", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "scientific notation", amount: "1e2", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "excess scale", amount: "1.001", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "invalid currency", amount: "1.00", currency: "brl", wantErr: ErrInvalidMoney},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMoney(tt.amount, tt.currency)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseMoney() error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got.MinorUnits() != tt.want {
				t.Fatalf("ParseMoney() minor units = %d, want %d", got.MinorUnits(), tt.want)
			}
		})
	}
}

// TestMoneyOperations verifica aritmética exata e incompatibilidade entre moedas.
func TestMoneyOperations(t *testing.T) {
	t.Parallel()

	a, _ := ParseMoney("25.00", "BRL")
	b, _ := ParseMoney("10.50", "BRL")

	sum, err := a.Add(b)
	if err != nil || sum.String() != "35.50" {
		t.Fatalf("Add() = %s, %v", sum.String(), err)
	}

	difference, err := a.Subtract(b)
	if err != nil || difference.String() != "14.50" {
		t.Fatalf("Subtract() = %s, %v", difference.String(), err)
	}

	usd, _ := ParseMoney("1.00", "USD")
	if _, err := a.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add() error = %v, want currency mismatch", err)
	}
}

// TestMoneyOverflow garante que a soma não ultrapasse a capacidade de int64.
func TestMoneyOverflow(t *testing.T) {
	t.Parallel()

	max := Money{minor: math.MaxInt64, currency: "BRL"}
	one := Money{minor: 1, currency: "BRL"}
	if _, err := max.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("Add() error = %v, want overflow", err)
	}
}
