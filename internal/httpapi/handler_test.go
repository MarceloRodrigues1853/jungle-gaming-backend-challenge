package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/auth"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
)

// TestSubmitWagerUsesAuthenticatedProvider valida o caminho HTTP completo até o caso de uso.
func TestSubmitWagerUsesAuthenticatedProvider(t *testing.T) {
	t.Parallel()

	balance, _ := domain.ParseMoney("75.00", "BRL")
	submitter := &submitterSpy{result: domain.WagerProcessingResult{
		TransactionID: "transaction-internal", Status: domain.TransactionProcessed,
		Balance: balance, HasBalance: true,
	}}
	handler := testHandler(t, &authenticatorStub{principal: auth.Principal{Role: auth.RoleProvider, ProviderID: "provider-a"}}, submitter)
	response := performWagerRequest(handler, "Bearer valid-token", "provider-a:external-1", validRequestBody("provider-a"))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !submitter.called || submitter.providerID != "provider-a" || submitter.idempotencyKey != "provider-a:external-1" {
		t.Fatalf("submit call = %v/%q/%q", submitter.called, submitter.providerID, submitter.idempotencyKey)
	}
	if submitter.command.ExternalTransactionID != "external-1" || !strings.Contains(response.Body.String(), `"amount":"75.00"`) {
		t.Fatalf("command/response = %#v / %s", submitter.command, response.Body.String())
	}
}

// TestSubmitWagerRejectsProviderMismatch impede falsificação do providerId no JSON.
func TestSubmitWagerRejectsProviderMismatch(t *testing.T) {
	t.Parallel()

	submitter := &submitterSpy{}
	handler := testHandler(t, &authenticatorStub{principal: auth.Principal{Role: auth.RoleProvider, ProviderID: "provider-a"}}, submitter)
	response := performWagerRequest(handler, "Bearer valid-token", "key-1", validRequestBody("provider-b"))
	if response.Code != http.StatusForbidden || submitter.called {
		t.Fatalf("status/called = %d/%v, body = %s", response.Code, submitter.called, response.Body.String())
	}
	assertErrorCode(t, response, "PROVIDER_MISMATCH")
}

// TestAuthenticationErrorsHaveDistinctStatuses diferencia credencial, autorização e IdP indisponível.
func TestAuthenticationErrorsHaveDistinctStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, authorization, code string
		err                       error
		status                    int
	}{
		{name: "missing bearer", code: "INVALID_TOKEN", status: http.StatusUnauthorized},
		{name: "inactive token", authorization: "Bearer inactive", err: auth.ErrInvalidToken, code: "INVALID_TOKEN", status: http.StatusUnauthorized},
		{name: "unmapped client", authorization: "Bearer unknown", err: auth.ErrUnauthorizedProvider, code: "UNAUTHORIZED_PROVIDER", status: http.StatusForbidden},
		{name: "identity provider down", authorization: "Bearer token", err: auth.ErrIntrospectionUnavailable, code: "IDENTITY_PROVIDER_UNAVAILABLE", status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := testHandler(t, &authenticatorStub{err: test.err}, &submitterSpy{})
			response := performWagerRequest(handler, test.authorization, "key-1", validRequestBody("provider-a"))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			assertErrorCode(t, response, test.code)
		})
	}
}

// TestSubmitWagerMapsReplayAndKnownErrors fixa o contrato público dos principais resultados.
func TestSubmitWagerMapsReplayAndKnownErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, code string
		result     domain.WagerProcessingResult
		err        error
		status     int
	}{
		{name: "idempotent replay", result: domain.WagerProcessingResult{TransactionID: "tx", Status: domain.TransactionProcessed, IdempotentReplay: true}, status: http.StatusOK},
		{name: "idempotency conflict", err: application.ErrIdempotencyConflict, code: "IDEMPOTENCY_CONFLICT", status: http.StatusConflict},
		{name: "reference missing", err: application.ErrReferenceNotFound, code: "REFERENCE_NOT_FOUND", status: http.StatusConflict},
		{name: "wallet missing", err: application.ErrWalletNotFound, code: "WALLET_NOT_FOUND", status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := testHandler(t, &authenticatorStub{principal: auth.Principal{Role: auth.RoleProvider, ProviderID: "provider-a"}}, &submitterSpy{result: test.result, err: test.err})
			response := performWagerRequest(handler, "Bearer token", "key-1", validRequestBody("provider-a"))
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if test.code != "" {
				assertErrorCode(t, response, test.code)
			}
		})
	}
}

// TestSubmitWagerRejectsAmbiguousJSON impede campos desconhecidos e múltiplos objetos.
func TestSubmitWagerRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()

	handler := testHandler(t, &authenticatorStub{principal: auth.Principal{Role: auth.RoleProvider, ProviderID: "provider-a"}}, &submitterSpy{})
	bodies := []string{
		strings.TrimSuffix(validRequestBody("provider-a"), "}") + `,"unexpected":true}`,
		validRequestBody("provider-a") + `{}`,
	}
	for _, body := range bodies {
		response := performWagerRequest(handler, "Bearer token", "key-1", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		assertErrorCode(t, response, "INVALID_JSON")
	}
}

// TestWalletRoutesRequireInternalIdentity impede que provedores administrem carteiras.
func TestWalletRoutesRequireInternalIdentity(t *testing.T) {
	t.Parallel()
	manager := &walletManagerStub{}
	handler, err := NewHandler(&authenticatorStub{principal: auth.Principal{Role: auth.RoleProvider, ProviderID: "provider-a"}}, &submitterSpy{}, manager, readinessStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/wallets", strings.NewReader(`{"playerId":"player-1","initialBalance":{"amount":"100.00","currency":"BRL"}}`))
	request.Header.Set("Authorization", "Bearer provider-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response, "INTERNAL_ROLE_REQUIRED")
}

// TestOpenAndGetWalletExposeExactMoney cobre os contratos internos de escrita e leitura.
func TestOpenAndGetWalletExposeExactMoney(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	balance, _ := domain.ParseMoney("100.00", "BRL")
	wallet, _ := domain.NewWalletWithBalance("wallet-1", "player-1", balance, now)
	manager := &walletManagerStub{wallet: wallet, exists: true}
	handler, err := NewHandler(&authenticatorStub{principal: auth.Principal{Role: auth.RoleInternal}}, &submitterSpy{}, manager, readinessStub{})
	if err != nil {
		t.Fatal(err)
	}

	open := httptest.NewRequest(http.MethodPost, "/wallets", strings.NewReader(`{"playerId":"player-1","initialBalance":{"amount":"100.00","currency":"BRL"}}`))
	open.Header.Set("Authorization", "Bearer internal-token")
	open.Header.Set("Content-Type", "application/json")
	openResponse := httptest.NewRecorder()
	handler.ServeHTTP(openResponse, open)
	if openResponse.Code != http.StatusCreated || !strings.Contains(openResponse.Body.String(), `"amount":"100.00"`) {
		t.Fatalf("open status/body = %d/%s", openResponse.Code, openResponse.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/wallets/wallet-1", nil)
	get.Header.Set("Authorization", "Bearer internal-token")
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"version":1`) {
		t.Fatalf("get status/body = %d/%s", getResponse.Code, getResponse.Body.String())
	}
}

// TestHealthChecksArePublicAndReadinessUsesPostgres separa vida do processo de prontidão.
func TestHealthChecksArePublicAndReadinessUsesPostgres(t *testing.T) {
	t.Parallel()

	liveHandler, err := NewHandler(&authenticatorStub{}, &submitterSpy{}, &walletManagerStub{}, readinessStub{})
	if err != nil {
		t.Fatal(err)
	}
	live := httptest.NewRecorder()
	liveHandler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", live.Code)
	}

	unreadyHandler, err := NewHandler(&authenticatorStub{}, &submitterSpy{}, &walletManagerStub{}, readinessStub{err: errors.New("postgres unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	ready := httptest.NewRecorder()
	unreadyHandler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, body = %s", ready.Code, ready.Body.String())
	}
}

// authenticatorStub controla a identidade devolvida ao middleware.
type authenticatorStub struct {
	principal auth.Principal
	err       error
}

func (stub *authenticatorStub) Authenticate(_ context.Context, _ string) (auth.Principal, error) {
	return stub.principal, stub.err
}

// submitterSpy registra os dados que atravessaram o adaptador HTTP.
type submitterSpy struct {
	called         bool
	providerID     string
	idempotencyKey string
	command        application.SubmitWagerCommand
	result         domain.WagerProcessingResult
	err            error
}

// walletManagerStub isola as rotas de carteira nos testes do adaptador HTTP.
type walletManagerStub struct {
	wallet domain.Wallet
	exists bool
	err    error
	opened bool
}

func (stub *walletManagerStub) Open(context.Context, application.OpenWalletCommand) (domain.Wallet, error) {
	stub.opened = true
	return stub.wallet, stub.err
}

func (stub *walletManagerStub) Get(context.Context, string) (domain.Wallet, bool, error) {
	return stub.wallet, stub.exists, stub.err
}

func (spy *submitterSpy) Submit(_ context.Context, providerID, idempotencyKey string, command application.SubmitWagerCommand) (domain.WagerProcessingResult, error) {
	spy.called, spy.providerID, spy.idempotencyKey, spy.command = true, providerID, idempotencyKey, command
	return spy.result, spy.err
}

// testHandler falha imediatamente se as dependências do roteador forem inválidas.
func testHandler(t *testing.T, authenticator Authenticator, submitter WagerSubmitter) http.Handler {
	t.Helper()
	handler, err := NewHandler(authenticator, submitter, &walletManagerStub{}, readinessStub{})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// readinessStub representa PostgreSQL disponível nos testes de contrato HTTP.
type readinessStub struct{ err error }

func (stub readinessStub) Ping(context.Context) error { return stub.err }

// performWagerRequest monta uma requisição equivalente à futura chamada do Postman.
func performWagerRequest(handler http.Handler, authorization, idempotencyKey, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

// validRequestBody mantém o exemplo legível para comparar com o contrato do desafio.
func validRequestBody(providerID string) string {
	return `{"providerId":"` + providerID + `","externalTransactionId":"external-1","playerId":"player-1","walletId":"wallet-1","roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`
}

// assertErrorCode verifica o código estável sem depender do texto explicativo.
func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	if !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("response body = %s, want code %s", response.Body.String(), code)
	}
}
