// Package httpapi expõe os casos de uso por HTTP sem duplicar regras financeiras.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/auth"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

const maximumRequestBody = 64 * 1024

// Authenticator valida um token externo e devolve a identidade autorizada.
type Authenticator interface {
	Authenticate(context.Context, string) (auth.Principal, error)
}

// WagerSubmitter representa o caso de uso compartilhado com a futura entrada SQS.
type WagerSubmitter interface {
	Submit(context.Context, string, string, application.SubmitWagerCommand) (domain.WagerProcessingResult, error)
}

// Readiness verifica se a dependência necessária para aceitar trabalho está disponível.
type Readiness interface {
	Ping(context.Context) error
}

// Handler agrupa as dependências dos endpoints financeiros.
type Handler struct {
	authenticator Authenticator
	wagers        WagerSubmitter
	readiness     Readiness
}

// NewHandler cria as rotas e aplica autenticação antes dos endpoints protegidos.
func NewHandler(authenticator Authenticator, wagers WagerSubmitter, readiness Readiness) (http.Handler, error) {
	if authenticator == nil || wagers == nil || readiness == nil {
		return nil, errors.New("HTTP authenticator, wager service, and readiness are required")
	}
	handler := &Handler{authenticator: authenticator, wagers: wagers, readiness: readiness}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", handler.live)
	mux.HandleFunc("GET /health/ready", handler.ready)
	mux.Handle("POST /wagering/transactions", handler.requireProvider(http.HandlerFunc(handler.submitWager)))
	return mux, nil
}

// live informa somente que o processo HTTP está em execução.
func (handler *Handler) live(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, healthResponse{Status: "ok"})
}

// ready confirma o PostgreSQL com prazo curto antes de aceitar tráfego financeiro.
func (handler *Handler) ready(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := handler.readiness.Ping(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, healthResponse{Status: "ready"})
}

// healthResponse mantém os health checks pequenos e estáveis.
type healthResponse struct {
	Status string `json:"status"`
}

// submitWager valida o contrato HTTP e delega a regra ao caso de uso.
func (handler *Handler) submitWager(writer http.ResponseWriter, request *http.Request) {
	if !hasJSONContentType(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
		return
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if strings.TrimSpace(idempotencyKey) == "" || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		writeError(writer, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is required and must not contain surrounding whitespace")
		return
	}

	var body submitWagerRequest
	if err := decodeJSON(writer, request, &body); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_JSON", "request body must contain one valid JSON object")
		return
	}
	principal, ok := providerPrincipal(request.Context())
	if !ok {
		writeError(writer, http.StatusInternalServerError, "MISSING_PRINCIPAL", "authenticated identity is unavailable")
		return
	}
	if body.ProviderID != principal.ProviderID {
		writeError(writer, http.StatusForbidden, "PROVIDER_MISMATCH", "authenticated provider cannot submit transactions for another provider")
		return
	}

	result, err := handler.wagers.Submit(request.Context(), principal.ProviderID, idempotencyKey, body.command())
	if err != nil {
		handler.writeSubmitError(writer, err)
		return
	}
	writeWagerResult(writer, result)
}

// requireProvider exige Bearer token e injeta somente a identidade validada no contexto.
func (handler *Handler) requireProvider(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token, ok := bearerToken(request.Header.Get("Authorization"))
		if !ok {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeError(writer, http.StatusUnauthorized, "INVALID_TOKEN", "a valid Bearer token is required")
			return
		}
		principal, err := handler.authenticator.Authenticate(request.Context(), token)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrInvalidToken):
				writer.Header().Set("WWW-Authenticate", "Bearer")
				writeError(writer, http.StatusUnauthorized, "INVALID_TOKEN", "the access token is inactive or invalid")
			case errors.Is(err, auth.ErrUnauthorizedProvider):
				writeError(writer, http.StatusForbidden, "UNAUTHORIZED_PROVIDER", "the token client is not an authorized provider")
			default:
				writeError(writer, http.StatusServiceUnavailable, "IDENTITY_PROVIDER_UNAVAILABLE", "token validation is temporarily unavailable")
			}
			return
		}
		if principal.Role != auth.RoleProvider {
			writeError(writer, http.StatusForbidden, "PROVIDER_ROLE_REQUIRED", "a provider service identity is required")
			return
		}
		next.ServeHTTP(writer, request.WithContext(withProviderPrincipal(request.Context(), principal)))
	})
}

// writeSubmitError traduz erros conhecidos sem expor detalhes internos do servidor.
func (handler *Handler) writeSubmitError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidWagerCommand):
		writeError(writer, http.StatusBadRequest, "INVALID_WAGER", "wager transaction data is invalid")
	case errors.Is(err, application.ErrIdempotencyConflict):
		writeError(writer, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "the external identity was already used with different content")
	case errors.Is(err, application.ErrReferenceNotFound):
		writeError(writer, http.StatusConflict, "REFERENCE_NOT_FOUND", "the referenced transaction is not available")
	case errors.Is(err, application.ErrWalletNotFound):
		writeError(writer, http.StatusNotFound, "WALLET_NOT_FOUND", "the wallet was not found")
	default:
		writeError(writer, http.StatusServiceUnavailable, "PROCESSING_UNAVAILABLE", "the transaction could not be processed now")
	}
}

// submitWagerRequest mantém providerId no contrato para conferir a identidade autenticada.
type submitWagerRequest struct {
	ProviderID                     string                 `json:"providerId"`
	ExternalTransactionID          string                 `json:"externalTransactionId"`
	PlayerID                       string                 `json:"playerId"`
	WalletID                       string                 `json:"walletId"`
	RoundID                        string                 `json:"roundId"`
	GameID                         string                 `json:"gameId"`
	Kind                           string                 `json:"kind"`
	Money                          application.MoneyInput `json:"money"`
	ReferenceExternalTransactionID string                 `json:"referenceExternalTransactionId,omitempty"`
}

// command remove do corpo a identidade, que já foi conferida contra o token.
func (body submitWagerRequest) command() application.SubmitWagerCommand {
	return application.SubmitWagerCommand{
		ExternalTransactionID: body.ExternalTransactionID, PlayerID: body.PlayerID,
		WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID,
		Kind: body.Kind, Money: body.Money,
		ReferenceExternalID: body.ReferenceExternalTransactionID,
	}
}

// wagerResponse mantém dinheiro como string no contrato externo.
type wagerResponse struct {
	TransactionID    string         `json:"transactionId"`
	Status           string         `json:"status"`
	Balance          *moneyResponse `json:"balance,omitempty"`
	FailureCode      string         `json:"failureCode,omitempty"`
	IdempotentReplay bool           `json:"idempotentReplay"`
}

// moneyResponse serializa o valor exato sem converter para ponto flutuante.
type moneyResponse struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// writeWagerResult escolhe o status HTTP conforme o resultado financeiro persistido.
func writeWagerResult(writer http.ResponseWriter, result domain.WagerProcessingResult) {
	response := wagerResponse{
		TransactionID: result.TransactionID, Status: string(result.Status),
		FailureCode: result.FailureCode, IdempotentReplay: result.IdempotentReplay,
	}
	if result.HasBalance {
		response.Balance = &moneyResponse{Amount: result.Balance.String(), Currency: result.Balance.Currency()}
	}
	status := http.StatusCreated
	switch {
	case result.IdempotentReplay:
		status = http.StatusOK
	case result.Status == domain.TransactionRejected:
		status = http.StatusUnprocessableEntity
	case result.Status == domain.TransactionPending || result.Status == domain.TransactionPendingReference:
		status = http.StatusAccepted
	case result.Status == domain.TransactionFailed:
		status = http.StatusInternalServerError
	}
	writeJSON(writer, status, response)
}

// decodeJSON limita o corpo, rejeita campos desconhecidos e exige um único objeto.
func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request contains more than one JSON value")
	}
	return nil
}

// hasJSONContentType aceita parâmetros válidos, como charset, mas não outros formatos.
func hasJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

// bearerToken aceita exatamente o esquema Bearer seguido de uma credencial sem espaços.
func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.ContainsAny(parts[1], "\r\n\t") {
		return "", false
	}
	return parts[1], true
}

// errorResponse padroniza falhas públicas sem devolver erros internos.
type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeError produz um envelope JSON estável para clientes e testes.
func writeError(writer http.ResponseWriter, status int, code, message string) {
	var response errorResponse
	response.Error.Code, response.Error.Message = code, message
	writeJSON(writer, status, response)
}

// writeJSON centraliza cabeçalho, status e codificação das respostas.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
