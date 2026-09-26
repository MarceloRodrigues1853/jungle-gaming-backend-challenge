package httpapi

import (
	"context"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/auth"
)

// principalContextKey usa um tipo privado para evitar colisões com outros pacotes.
type principalContextKey struct{}

// withProviderPrincipal associa a identidade validada somente ao contexto da requisição.
func withProviderPrincipal(ctx context.Context, principal auth.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// providerPrincipal lê a identidade previamente inserida pelo middleware.
func providerPrincipal(ctx context.Context) (auth.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(auth.Principal)
	return principal, ok
}
