// Package auth valida tokens OAuth 2.0 emitidos por um provedor OIDC externo.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrInvalidToken indica credencial ausente, inativa ou malformada.
	ErrInvalidToken = errors.New("invalid access token")
	// ErrUnauthorizedProvider indica que o token não pertence a um provedor autorizado.
	ErrUnauthorizedProvider = errors.New("provider client is not authorized")
	// ErrIntrospectionUnavailable indica que não foi possível validar a credencial com o IdP.
	ErrIntrospectionUnavailable = errors.New("token introspection unavailable")
)

// ClientRole separa provedores externos de serviços internos autorizados.
type ClientRole string

const (
	// RoleProvider permite enviar operações somente para o próprio providerId.
	RoleProvider ClientRole = "PROVIDER"
	// RoleInternal permite executar operações administrativas, como abrir carteiras.
	RoleInternal ClientRole = "INTERNAL"
)

// ClientIdentity define a autorização esperada para um client_id do IdP.
type ClientIdentity struct {
	Role       ClientRole
	ProviderID string
}

// Principal representa a identidade do provedor obtida do token validado.
type Principal struct {
	Role       ClientRole
	ProviderID string
	ClientID   string
	Subject    string
}

// Introspector valida tokens no endpoint OAuth 2.0 de introspecção do IdP.
type Introspector struct {
	endpoint           string
	clientID           string
	clientSecret       string
	identityByClientID map[string]ClientIdentity
	httpClient         *http.Client
}

// NewIntrospector configura um cliente confidencial e uma allowlist explícita de provedores.
func NewIntrospector(endpoint, clientID, clientSecret string, identityByClientID map[string]ClientIdentity, httpClient *http.Client) (*Introspector, error) {
	parsedEndpoint, err := url.ParseRequestURI(endpoint)
	if err != nil || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") || parsedEndpoint.Host == "" {
		return nil, errors.New("introspection endpoint must be an absolute HTTP URL")
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("introspection client credentials are required")
	}
	if len(identityByClientID) == 0 {
		return nil, errors.New("at least one client identity must be authorized")
	}
	identities := make(map[string]ClientIdentity, len(identityByClientID))
	for key, identity := range identityByClientID {
		if strings.TrimSpace(key) == "" || (identity.Role != RoleProvider && identity.Role != RoleInternal) {
			return nil, errors.New("client id and role must be valid")
		}
		if identity.Role == RoleProvider && strings.TrimSpace(identity.ProviderID) == "" {
			return nil, errors.New("provider identity requires provider id")
		}
		if identity.Role == RoleInternal && strings.TrimSpace(identity.ProviderID) != "" {
			return nil, errors.New("internal identity must not impersonate a provider")
		}
		identities[key] = identity
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Introspector{
		endpoint: endpoint, clientID: clientID, clientSecret: clientSecret,
		identityByClientID: identities, httpClient: httpClient,
	}, nil
}

// Authenticate verifica o token no IdP e deriva o provedor do client_id autorizado.
func (introspector *Introspector) Authenticate(ctx context.Context, accessToken string) (Principal, error) {
	if introspector == nil || introspector.httpClient == nil {
		return Principal{}, errors.New("token introspector is not initialized")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || strings.ContainsAny(accessToken, " \t\r\n") {
		return Principal{}, ErrInvalidToken
	}

	form := url.Values{"token": {accessToken}, "token_type_hint": {"access_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, introspector.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Principal{}, fmt.Errorf("build token introspection request: %w", err)
	}
	request.SetBasicAuth(introspector.clientID, introspector.clientSecret)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	response, err := introspector.httpClient.Do(request)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrIntrospectionUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Principal{}, fmt.Errorf("%w: IdP returned HTTP %d", ErrIntrospectionUnavailable, response.StatusCode)
	}

	var claims introspectionClaims
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: invalid introspection response", ErrIntrospectionUnavailable)
	}
	if !claims.Active {
		return Principal{}, ErrInvalidToken
	}
	identity, authorized := introspector.identityByClientID[claims.ClientID]
	if !authorized {
		return Principal{}, ErrUnauthorizedProvider
	}
	return Principal{Role: identity.Role, ProviderID: identity.ProviderID, ClientID: claims.ClientID, Subject: claims.Subject}, nil
}

// introspectionClaims lê apenas os campos necessários para autenticar e autorizar um provedor.
type introspectionClaims struct {
	Active   bool   `json:"active"`
	ClientID string `json:"client_id"`
	Subject  string `json:"sub"`
}
