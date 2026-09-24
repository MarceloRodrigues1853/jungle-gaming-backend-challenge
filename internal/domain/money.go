package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrInvalidMoney     = errors.New("invalid money")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrMoneyOverflow    = errors.New("money overflow")
	moneyPattern        = regexp.MustCompile(`^(0|[1-9][0-9]*)\.([0-9]{2})$`)
	currencyPattern     = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Money stores exact monetary values in minor units. It never uses floating point.
type Money struct {
	minor    int64
	currency string
}

func ParseMoney(amount, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency must be an ISO 4217 code", ErrInvalidMoney)
	}

	matches := moneyPattern.FindStringSubmatch(amount)
	if matches == nil {
		return Money{}, fmt.Errorf("%w: amount must use exactly two decimal places", ErrInvalidMoney)
	}

	whole, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || whole > math.MaxInt64/100 {
		return Money{}, ErrMoneyOverflow
	}

	fraction, _ := strconv.ParseInt(matches[2], 10, 64)
	minor := whole * 100
	if minor > math.MaxInt64-fraction {
		return Money{}, ErrMoneyOverflow
	}

	return Money{minor: minor + fraction, currency: currency}, nil
}

func Zero(currency string) (Money, error) {
	return ParseMoney("0.00", currency)
}

func (m Money) MinorUnits() int64 { return m.minor }

func (m Money) Currency() string { return m.currency }

func (m Money) String() string {
	whole := m.minor / 100
	fraction := m.minor % 100
	if fraction < 0 {
		fraction = -fraction
	}
	if m.minor < 0 && whole == 0 {
		return fmt.Sprintf("-0.%02d", fraction)
	}
	return fmt.Sprintf("%d.%02d", whole, fraction)
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

func (m Money) Subtract(other Money) (Money, error) {
	negated, err := other.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negated)
}

func (m Money) Negate() (Money, error) {
	if m.minor == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.sameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) IsNegative() bool { return m.minor < 0 }

func (m Money) IsZero() bool { return m.minor == 0 }

func (m Money) sameCurrency(other Money) error {
	if strings.TrimSpace(m.currency) == "" || m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}
