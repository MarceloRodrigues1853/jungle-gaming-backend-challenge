package bootstrap

import (
	"errors"
	"os"
	"strings"
)

// Config reúne somente valores necessários para compor e iniciar a API.
type Config struct {
	HTTPAddress         string
	DatabaseURL         string
	IntrospectionURL    string
	IntrospectionClient string
	IntrospectionSecret string
	ProviderClientID    string
	ProviderID          string
	InternalClientID    string
}

// LoadConfig lê o ambiente com padrões restritos ao desenvolvimento local.
func LoadConfig() (Config, error) {
	config := Config{
		HTTPAddress:         environmentOrDefault("HTTP_ADDRESS", "127.0.0.1:8090"),
		DatabaseURL:         environmentOrDefault("DATABASE_URL", "postgres://jungle_app:local_dev_only@127.0.0.1:5432/jungle_gaming?sslmode=disable"),
		IntrospectionURL:    environmentOrDefault("OIDC_INTROSPECTION_URL", "http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token/introspect"),
		IntrospectionClient: environmentOrDefault("OIDC_INTROSPECTION_CLIENT_ID", "jungle-api"),
		IntrospectionSecret: environmentOrDefault("OIDC_INTROSPECTION_CLIENT_SECRET", "jungle-api-introspection-secret"),
		ProviderClientID:    environmentOrDefault("PROVIDER_CLIENT_ID", "provider-a"),
		ProviderID:          environmentOrDefault("PROVIDER_ID", "provider-a"),
		InternalClientID:    environmentOrDefault("INTERNAL_CLIENT_ID", "wallet-internal"),
	}
	if strings.TrimSpace(config.HTTPAddress) == "" || strings.TrimSpace(config.DatabaseURL) == "" ||
		strings.TrimSpace(config.IntrospectionURL) == "" || strings.TrimSpace(config.IntrospectionClient) == "" ||
		strings.TrimSpace(config.IntrospectionSecret) == "" || strings.TrimSpace(config.ProviderClientID) == "" ||
		strings.TrimSpace(config.ProviderID) == "" || strings.TrimSpace(config.InternalClientID) == "" {
		return Config{}, errors.New("application configuration contains empty values")
	}
	return config, nil
}

// environmentOrDefault preserva configuração explícita e fornece conveniência local.
func environmentOrDefault(name, fallback string) string {
	if value, exists := os.LookupEnv(name); exists {
		return value
	}
	return fallback
}
