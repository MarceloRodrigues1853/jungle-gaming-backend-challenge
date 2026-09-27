package e2e

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/auth"
)

// TestRealKeycloakAcceptsActiveAndRejectsInvalidOrExpiredTokens usa o endpoint
// real de emissão e introspecção, sem substituir o IdP por doubles de teste.
func TestRealKeycloakAcceptsActiveAndRejectsInvalidOrExpiredTokens(t *testing.T) {
	if os.Getenv("JUNGLE_IDP_TEST") != "1" {
		t.Skip("set JUNGLE_IDP_TEST=1 with the Keycloak Compose service running")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	introspector, err := auth.NewIntrospector(
		"http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token/introspect",
		"jungle-api", "jungle-api-introspection-secret",
		map[string]auth.ClientIdentity{
			"provider-expiring-test": {Role: auth.RoleProvider, ProviderID: "provider-a"},
		}, client,
	)
	if err != nil {
		t.Fatal(err)
	}

	token := clientCredentialsToken(t, client, "provider-expiring-test", "provider-expiring-test-local-secret")
	principal, err := introspector.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate newly issued token: %v", err)
	}
	if principal.Role != auth.RoleProvider || principal.ProviderID != "provider-a" || principal.ClientID != "provider-expiring-test" {
		t.Fatalf("active token principal = %+v", principal)
	}
	if _, err := introspector.Authenticate(context.Background(), "not-a-real-access-token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("invalid token error = %v, want ErrInvalidToken", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, err = introspector.Authenticate(context.Background(), token)
		if errors.Is(err, auth.ErrInvalidToken) {
			return
		}
		if err != nil {
			t.Fatalf("introspect short-lived token: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("short-lived access token remained active after its configured lifespan")
}
