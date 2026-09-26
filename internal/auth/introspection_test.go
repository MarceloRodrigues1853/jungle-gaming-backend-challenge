package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthenticateUsesAuthorizedClientIdentity comprova que o provedor vem do client_id validado pelo IdP.
func TestAuthenticateUsesAuthorizedClientIdentity(t *testing.T) {
	t.Parallel()

	server := newIntrospectionServer(t, http.StatusOK, `{"active":true,"client_id":"kc-client-a","sub":"service-account-a"}`)
	introspector := newTestIntrospector(t, server, map[string]ClientIdentity{"kc-client-a": {Role: RoleProvider, ProviderID: "provider-a"}})

	principal, err := introspector.Authenticate(context.Background(), "signed-access-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.Role != RoleProvider || principal.ProviderID != "provider-a" || principal.ClientID != "kc-client-a" || principal.Subject != "service-account-a" {
		t.Fatalf("principal = %#v", principal)
	}
}

// TestAuthenticateDistinguishesInternalClient garante que serviço interno não vire provedor.
func TestAuthenticateDistinguishesInternalClient(t *testing.T) {
	t.Parallel()

	server := newIntrospectionServer(t, http.StatusOK, `{"active":true,"client_id":"wallet-internal","sub":"service-account-wallet"}`)
	introspector := newTestIntrospector(t, server, map[string]ClientIdentity{"wallet-internal": {Role: RoleInternal}})
	principal, err := introspector.Authenticate(context.Background(), "signed-access-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if principal.Role != RoleInternal || principal.ProviderID != "" {
		t.Fatalf("internal principal = %#v", principal)
	}
}

// TestAuthenticateRejectsInactiveAndUnmappedTokens impede token expirado ou client não autorizado.
func TestAuthenticateRejectsInactiveAndUnmappedTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		allowed map[string]ClientIdentity
		wantErr error
	}{
		{
			name:    "inactive token",
			body:    `{"active":false,"client_id":"kc-client-a"}`,
			allowed: map[string]ClientIdentity{"kc-client-a": {Role: RoleProvider, ProviderID: "provider-a"}},
			wantErr: ErrInvalidToken,
		},
		{
			name:    "unmapped token client",
			body:    `{"active":true,"client_id":"unknown-client"}`,
			allowed: map[string]ClientIdentity{"kc-client-a": {Role: RoleProvider, ProviderID: "provider-a"}},
			wantErr: ErrUnauthorizedProvider,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newIntrospectionServer(t, http.StatusOK, test.body)
			introspector := newTestIntrospector(t, server, test.allowed)
			if _, err := introspector.Authenticate(context.Background(), "token"); !errors.Is(err, test.wantErr) {
				t.Fatalf("Authenticate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestAuthenticateClassifiesIdPFailures evita tratar indisponibilidade do IdP como token inválido.
func TestAuthenticateClassifiesIdPFailures(t *testing.T) {
	t.Parallel()

	server := newIntrospectionServer(t, http.StatusServiceUnavailable, `{}`)
	introspector := newTestIntrospector(t, server, map[string]ClientIdentity{"kc-client-a": {Role: RoleProvider, ProviderID: "provider-a"}})
	if _, err := introspector.Authenticate(context.Background(), "token"); !errors.Is(err, ErrIntrospectionUnavailable) {
		t.Fatalf("Authenticate() error = %v, want introspection unavailable", err)
	}
}

// TestAuthenticateRejectsMalformedBearerValue garante que um cabeçalho ambíguo não chegue ao endpoint de introspecção.
func TestAuthenticateRejectsMalformedBearerValue(t *testing.T) {
	t.Parallel()

	introspector, err := NewIntrospector("http://localhost/introspect", "api", "secret", map[string]ClientIdentity{"kc-client-a": {Role: RoleProvider, ProviderID: "provider-a"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := introspector.Authenticate(context.Background(), "token with spaces"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate() error = %v, want invalid token", err)
	}
}

// newIntrospectionServer verifica o formato OAuth 2.0 e responde com claims controladas pelo teste.
func newIntrospectionServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("request method = %s, want POST", request.Method)
		}
		clientID, clientSecret, ok := request.BasicAuth()
		if !ok || clientID != "api-client" || clientSecret != "api-secret" {
			t.Errorf("basic authentication = %q/%q/%v", clientID, clientSecret, ok)
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if request.Form.Get("token") == "" || request.Form.Get("token_type_hint") != "access_token" {
			t.Errorf("introspection form = %v", request.Form)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// newTestIntrospector cria o cliente do teste com allowlist e credenciais artificiais.
func newTestIntrospector(t *testing.T, server *httptest.Server, identities map[string]ClientIdentity) *Introspector {
	t.Helper()
	introspector, err := NewIntrospector(server.URL, "api-client", "api-secret", identities, server.Client())
	if err != nil {
		t.Fatalf("NewIntrospector() error = %v", err)
	}
	return introspector
}

// TestNewIntrospectorRequiresValidConfiguration rejeita URL e allowlists sem valor utilizável.
func TestNewIntrospectorRequiresValidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		endpoint   string
		clientID   string
		secret     string
		identities map[string]ClientIdentity
	}{
		{name: "relative endpoint", endpoint: "/introspect", clientID: "api", secret: "secret", identities: map[string]ClientIdentity{"kc": {Role: RoleProvider, ProviderID: "provider"}}},
		{name: "missing credentials", endpoint: "http://localhost/introspect", identities: map[string]ClientIdentity{"kc": {Role: RoleProvider, ProviderID: "provider"}}},
		{name: "empty allowlist", endpoint: "http://localhost/introspect", clientID: "api", secret: "secret"},
		{name: "empty provider mapping", endpoint: "http://localhost/introspect", clientID: "api", secret: "secret", identities: map[string]ClientIdentity{"kc": {Role: RoleProvider}}},
		{name: "internal impersonates provider", endpoint: "http://localhost/introspect", clientID: "api", secret: "secret", identities: map[string]ClientIdentity{"kc": {Role: RoleInternal, ProviderID: "provider"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewIntrospector(test.endpoint, test.clientID, test.secret, test.identities, nil)
			if err == nil || strings.TrimSpace(fmt.Sprint(err)) == "" {
				t.Fatal("NewIntrospector() should reject invalid configuration")
			}
		})
	}
}
