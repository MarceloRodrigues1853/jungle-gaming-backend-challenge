// Package domain contém os tipos e as regras centrais do domínio financeiro.
// Ele não depende de banco de dados, transporte HTTP ou mensageria.
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
	// ErrInvalidMoney indica que o valor ou a moeda não seguem o formato aceito.
	ErrInvalidMoney = errors.New("invalid money")
	// ErrCurrencyMismatch indica uma operação entre valores de moedas diferentes.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrMoneyOverflow indica que uma operação excederia os limites de int64.
	ErrMoneyOverflow = errors.New("money overflow")
	// O formato exige duas casas decimais e não aceita sinal, zeros à esquerda ou expoente.
	moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.([0-9]{2})$`)
	// O domínio valida a forma ISO 4217; a lista suportada é decisão da aplicação.
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Money representa um valor monetário exato em unidades mínimas (centavos).
// Seus campos privados impedem alterações diretas e evitam ponto flutuante.
type Money struct {
	minor    int64
	currency string
}

// ParseMoney valida o formato decimal fixo e converte o valor para unidades mínimas.
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

// Zero cria o valor zero para uma moeda válida.
func Zero(currency string) (Money, error) {
	return ParseMoney("0.00", currency)
}

// MoneyFromMinorUnits reidrata um valor exato vindo de uma fonte persistente.
func MoneyFromMinorUnits(minor int64, currency string) (Money, error) {
	if !currencyPattern.MatchString(currency) {
		return Money{}, fmt.Errorf("%w: currency must be an ISO 4217 code", ErrInvalidMoney)
	}
	return Money{minor: minor, currency: currency}, nil
}

// MinorUnits retorna o valor inteiro armazenado, sem conversão para ponto flutuante.
func (m Money) MinorUnits() int64 { return m.minor }

// Currency retorna o código da moeda associado ao valor.
func (m Money) Currency() string { return m.currency }

// String formata o valor com duas casas decimais, preservando sinal interno negativo.
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

// Add soma valores da mesma moeda e rejeita resultados fora do intervalo de int64.
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

// Subtract subtrai outro valor da mesma moeda e propaga erros de moeda ou overflow.
func (m Money) Subtract(other Money) (Money, error) {
	negated, err := other.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negated)
}

// Negate inverte o sinal; o menor int64 é rejeitado porque seu oposto não cabe em int64.
func (m Money) Negate() (Money, error) {
	if m.minor == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Compare compara valores da mesma moeda: -1, 0 ou 1 conforme m seja menor, igual ou maior.
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

// IsNegative informa se o valor é negativo; isso pode ocorrer em cálculos internos.
func (m Money) IsNegative() bool { return m.minor < 0 }

// IsZero informa se o valor não representa movimentação monetária.
func (m Money) IsZero() bool { return m.minor == 0 }

// sameCurrency centraliza a validação exigida por operações monetárias.
func (m Money) sameCurrency(other Money) error {
	if strings.TrimSpace(m.currency) == "" || m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}
