// Package httpapi expõe os casos de uso por HTTP sem duplicar regras financeiras.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
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

// WalletManager expõe somente os casos de uso necessários às rotas internas.
type WalletManager interface {
	Open(context.Context, application.OpenWalletCommand) (domain.Wallet, error)
	Get(context.Context, string) (domain.Wallet, bool, error)
}

// Readiness verifica se a dependência necessária para aceitar trabalho está disponível.
type Readiness interface {
	Ping(context.Context) error
}

// Handler agrupa as dependências dos endpoints financeiros.
type Handler struct {
	authenticator Authenticator
	wagers        WagerSubmitter
	wallets       WalletManager
	readiness     Readiness
	queries       application.FinancialQueries
}

// NewHandler cria as rotas e aplica autenticação antes dos endpoints protegidos.
func NewHandler(authenticator Authenticator, wagers WagerSubmitter, wallets WalletManager, readiness Readiness, queries application.FinancialQueries) (http.Handler, error) {
	if authenticator == nil || wagers == nil || wallets == nil || readiness == nil || queries == nil {
		return nil, errors.New("HTTP authenticator, wager service, wallet service, and readiness are required")
	}
	handler := &Handler{authenticator: authenticator, wagers: wagers, wallets: wallets, readiness: readiness, queries: queries}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", handler.live)
	mux.HandleFunc("GET /health/ready", handler.ready)
	mux.Handle("POST /wagering/transactions", handler.requireProvider(http.HandlerFunc(handler.submitWager)))
	mux.Handle("POST /wallets", handler.requireInternal(http.HandlerFunc(handler.openWallet)))
	mux.Handle("GET /wallets/{walletId}", handler.requireInternal(http.HandlerFunc(handler.getWallet)))
	mux.Handle("GET /wallets/{walletId}/ledger", handler.requireInternal(http.HandlerFunc(handler.listLedger)))
	mux.Handle("POST /wallets/{walletId}/reconciliation", handler.requireInternal(http.HandlerFunc(handler.reconcileWallet)))
	mux.Handle("GET /wagering/transactions/{transactionId}", handler.requireProvider(http.HandlerFunc(handler.getTransaction)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", handler.requireProvider(http.HandlerFunc(handler.getProviderTransaction)))
	return mux, nil
}

func (handler *Handler) listLedger(writer http.ResponseWriter, request *http.Request) {
	limit := 50
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(writer, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	page, exists, err := handler.queries.ListWalletLedger(request.Context(), request.PathValue("walletId"), request.URL.Query().Get("cursor"), limit)
	if err != nil {
		if errors.Is(err, application.ErrInvalidLedgerQuery) {
			writeError(writer, http.StatusBadRequest, "INVALID_LEDGER_QUERY", "ledger cursor or parameters are invalid")
		} else {
			writeError(writer, http.StatusServiceUnavailable, "LEDGER_UNAVAILABLE", "wallet ledger could not be read")
		}
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "WALLET_NOT_FOUND", "the wallet was not found")
		return
	}
	response := ledgerPageResponse{NextCursor: page.NextCursor}
	for _, item := range page.Items {
		response.Items = append(response.Items, ledgerItemResponse{ID: item.ID, TransactionID: item.TransactionID,
			Direction: string(item.Direction), Money: moneyFromDomain(item.Money),
			BalanceBefore: moneyFromDomain(item.BalanceBefore), BalanceAfter: moneyFromDomain(item.BalanceAfter), CreatedAt: item.CreatedAt})
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) reconcileWallet(writer http.ResponseWriter, request *http.Request) {
	result, exists, err := handler.queries.ReconcileWallet(request.Context(), request.PathValue("walletId"))
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "RECONCILIATION_UNAVAILABLE", "wallet reconciliation could not be completed")
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "WALLET_NOT_FOUND", "the wallet was not found")
		return
	}
	writeJSON(writer, http.StatusOK, reconciliationResponse{WalletID: result.WalletID,
		StoredBalance: moneyFromDomain(result.StoredBalance), CalculatedBalance: moneyFromDomain(result.CalculatedBalance),
		Difference: moneyFromDomain(result.Difference), Consistent: result.Consistent, CheckedEntries: result.CheckedEntries})
}

func (handler *Handler) getTransaction(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request.Context())
	view, exists, err := handler.queries.GetProviderTransactionByID(request.Context(), principal.ProviderID, request.PathValue("transactionId"))
	handler.writeTransactionView(writer, view, exists, err)
}

func (handler *Handler) getProviderTransaction(writer http.ResponseWriter, request *http.Request) {
	principal, _ := requestPrincipal(request.Context())
	if request.PathValue("providerId") != principal.ProviderID {
		writeError(writer, http.StatusForbidden, "PROVIDER_MISMATCH", "authenticated provider cannot read another provider's transactions")
		return
	}
	view, exists, err := handler.queries.GetProviderTransactionByExternalID(request.Context(), principal.ProviderID, request.PathValue("externalTransactionId"))
	handler.writeTransactionView(writer, view, exists, err)
}

func (handler *Handler) writeTransactionView(writer http.ResponseWriter, view application.TransactionView, exists bool, err error) {
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TRANSACTION_UNAVAILABLE", "transaction could not be read")
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "TRANSACTION_NOT_FOUND", "the transaction was not found")
		return
	}
	response := transactionViewResponse{TransactionID: view.ID, ExternalTransactionID: view.ExternalTransactionID,
		WalletID: view.WalletID, PlayerID: view.PlayerID, RoundID: view.RoundID, GameID: view.GameID,
		Kind: string(view.Kind), Money: moneyFromDomain(view.Money), Status: string(view.Status),
		FailureCode: view.FailureCode, ReferenceExternalTransactionID: view.ReferenceExternalID,
		ReferenceTransactionID: view.ReferenceID, CreatedAt: view.CreatedAt, UpdatedAt: view.UpdatedAt}
	if view.HasResultBalance {
		balance := moneyFromDomain(view.ResultBalance)
		response.ResultBalance = &balance
	}
	writeJSON(writer, http.StatusOK, response)
}

type ledgerPageResponse struct {
	Items      []ledgerItemResponse `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}
type ledgerItemResponse struct {
	ID            string        `json:"id"`
	TransactionID string        `json:"transactionId"`
	Direction     string        `json:"direction"`
	Money         moneyResponse `json:"money"`
	BalanceBefore moneyResponse `json:"balanceBefore"`
	BalanceAfter  moneyResponse `json:"balanceAfter"`
	CreatedAt     time.Time     `json:"createdAt"`
}
type reconciliationResponse struct {
	WalletID          string        `json:"walletId"`
	StoredBalance     moneyResponse `json:"storedBalance"`
	CalculatedBalance moneyResponse `json:"calculatedBalance"`
	Difference        moneyResponse `json:"difference"`
	Consistent        bool          `json:"consistent"`
	CheckedEntries    int64         `json:"checkedEntries"`
}
type transactionViewResponse struct {
	TransactionID                  string         `json:"transactionId"`
	ExternalTransactionID          string         `json:"externalTransactionId"`
	WalletID                       string         `json:"walletId"`
	PlayerID                       string         `json:"playerId"`
	RoundID                        string         `json:"roundId"`
	GameID                         string         `json:"gameId"`
	Kind                           string         `json:"kind"`
	Money                          moneyResponse  `json:"money"`
	Status                         string         `json:"status"`
	FailureCode                    string         `json:"failureCode,omitempty"`
	ReferenceExternalTransactionID string         `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string         `json:"referenceTransactionId,omitempty"`
	ResultBalance                  *moneyResponse `json:"resultBalance,omitempty"`
	CreatedAt                      time.Time      `json:"createdAt"`
	UpdatedAt                      time.Time      `json:"updatedAt"`
}

func moneyFromDomain(money domain.Money) moneyResponse {
	return moneyResponse{Amount: money.String(), Currency: money.Currency()}
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
	principal, ok := requestPrincipal(request.Context())
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
		next.ServeHTTP(writer, request.WithContext(withPrincipal(request.Context(), principal)))
	})
}

// requireInternal permite operações de carteira somente à identidade de serviço interno.
func (handler *Handler) requireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		token, ok := bearerToken(request.Header.Get("Authorization"))
		if !ok {
			writer.Header().Set("WWW-Authenticate", "Bearer")
			writeError(writer, http.StatusUnauthorized, "INVALID_TOKEN", "a valid Bearer token is required")
			return
		}
		principal, err := handler.authenticator.Authenticate(request.Context(), token)
		if err != nil {
			handler.writeAuthenticationError(writer, err)
			return
		}
		if principal.Role != auth.RoleInternal {
			writeError(writer, http.StatusForbidden, "INTERNAL_ROLE_REQUIRED", "an internal service identity is required")
			return
		}
		next.ServeHTTP(writer, request.WithContext(withPrincipal(request.Context(), principal)))
	})
}

// writeAuthenticationError mantém os mesmos códigos nos dois tipos de rota protegida.
func (handler *Handler) writeAuthenticationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidToken):
		writer.Header().Set("WWW-Authenticate", "Bearer")
		writeError(writer, http.StatusUnauthorized, "INVALID_TOKEN", "the access token is inactive or invalid")
	case errors.Is(err, auth.ErrUnauthorizedProvider):
		writeError(writer, http.StatusForbidden, "UNAUTHORIZED_CLIENT", "the token client is not authorized")
	default:
		writeError(writer, http.StatusServiceUnavailable, "IDENTITY_PROVIDER_UNAVAILABLE", "token validation is temporarily unavailable")
	}
}

// openWallet valida a entrada e delega a atomicidade ao caso de uso.
func (handler *Handler) openWallet(writer http.ResponseWriter, request *http.Request) {
	if !hasJSONContentType(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json")
		return
	}
	var command application.OpenWalletCommand
	if err := decodeJSON(writer, request, &command); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_JSON", "request body must contain one valid JSON object")
		return
	}
	wallet, err := handler.wallets.Open(request.Context(), command)
	if err != nil {
		handler.writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, walletResponseFromDomain(wallet))
}

// getWallet consulta uma carteira sem permitir acesso de provedores externos.
func (handler *Handler) getWallet(writer http.ResponseWriter, request *http.Request) {
	wallet, exists, err := handler.wallets.Get(request.Context(), request.PathValue("walletId"))
	if err != nil {
		handler.writeWalletError(writer, err)
		return
	}
	if !exists {
		writeError(writer, http.StatusNotFound, "WALLET_NOT_FOUND", "the wallet was not found")
		return
	}
	writeJSON(writer, http.StatusOK, walletResponseFromDomain(wallet))
}

// writeWalletError traduz as falhas conhecidas sem revelar detalhes internos.
func (handler *Handler) writeWalletError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidWalletCommand):
		writeError(writer, http.StatusBadRequest, "INVALID_WALLET", "wallet data is invalid")
	case errors.Is(err, application.ErrWalletConflict):
		writeError(writer, http.StatusConflict, "WALLET_ALREADY_EXISTS", "player and currency already have a wallet")
	default:
		writeError(writer, http.StatusServiceUnavailable, "WALLET_UNAVAILABLE", "the wallet operation could not be completed now")
	}
}

// walletResponse mantém dinheiro como string no contrato externo.
type walletResponse struct {
	ID       string        `json:"id"`
	PlayerID string        `json:"playerId"`
	Balance  moneyResponse `json:"balance"`
	Version  int64         `json:"version"`
}

func walletResponseFromDomain(wallet domain.Wallet) walletResponse {
	return walletResponse{ID: wallet.ID(), PlayerID: wallet.PlayerID(),
		Balance: moneyResponse{Amount: wallet.Balance().String(), Currency: wallet.Balance().Currency()}, Version: wallet.Version()}
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
