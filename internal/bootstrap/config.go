package bootstrap

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config reúne somente valores necessários para compor e iniciar a API.
type Config struct {
	HTTPAddress           string
	DatabaseURL           string
	IntrospectionURL      string
	IntrospectionClient   string
	IntrospectionSecret   string
	ProviderClientID      string
	ProviderID            string
	InternalClientID      string
	AWSRegion             string
	SQSEndpoint           string
	SQSOutputQueueURL     string
	SQSInputQueueURL      string
	SQSConsumerName       string
	SQSWaitTimeSeconds    int
	SQSVisibilitySeconds  int
	SQSMaxMessages        int
	OutboxPollInterval    time.Duration
	OutboxLockDuration    time.Duration
	OutboxBatchSize       int
	ReferencePollInterval time.Duration
	ReferenceLockDuration time.Duration
	ReferenceBatchSize    int
	ReferenceMaxAttempts  int
}

// LoadConfig lê o ambiente com padrões restritos ao desenvolvimento local.
func LoadConfig() (Config, error) {
	config := Config{
		HTTPAddress:           environmentOrDefault("HTTP_ADDRESS", "127.0.0.1:8090"),
		DatabaseURL:           environmentOrDefault("DATABASE_URL", "postgres://jungle_app:local_dev_only@127.0.0.1:5432/jungle_gaming?sslmode=disable"),
		IntrospectionURL:      environmentOrDefault("OIDC_INTROSPECTION_URL", "http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token/introspect"),
		IntrospectionClient:   environmentOrDefault("OIDC_INTROSPECTION_CLIENT_ID", "jungle-api"),
		IntrospectionSecret:   environmentOrDefault("OIDC_INTROSPECTION_CLIENT_SECRET", "jungle-api-introspection-secret"),
		ProviderClientID:      environmentOrDefault("PROVIDER_CLIENT_ID", "provider-a"),
		ProviderID:            environmentOrDefault("PROVIDER_ID", "provider-a"),
		InternalClientID:      environmentOrDefault("INTERNAL_CLIENT_ID", "wallet-internal"),
		AWSRegion:             environmentOrDefault("AWS_REGION", "us-east-1"),
		SQSEndpoint:           environmentOrDefault("SQS_ENDPOINT", "http://127.0.0.1:4566"),
		SQSOutputQueueURL:     environmentOrDefault("SQS_OUTPUT_QUEUE_URL", "http://127.0.0.1:4566/000000000000/jungle-events.fifo"),
		SQSInputQueueURL:      environmentOrDefault("SQS_INPUT_QUEUE_URL", "http://127.0.0.1:4566/000000000000/wager-transactions.fifo"),
		SQSConsumerName:       environmentOrDefault("SQS_CONSUMER_NAME", "wager-transactions-v1"),
		SQSWaitTimeSeconds:    integerOrDefault("SQS_WAIT_TIME_SECONDS", 10),
		SQSVisibilitySeconds:  integerOrDefault("SQS_VISIBILITY_TIMEOUT_SECONDS", 30),
		SQSMaxMessages:        integerOrDefault("SQS_MAX_MESSAGES", 10),
		OutboxPollInterval:    durationOrDefault("OUTBOX_POLL_INTERVAL", time.Second),
		OutboxLockDuration:    durationOrDefault("OUTBOX_LOCK_DURATION", 30*time.Second),
		OutboxBatchSize:       integerOrDefault("OUTBOX_BATCH_SIZE", 20),
		ReferencePollInterval: durationOrDefault("REFERENCE_POLL_INTERVAL", time.Second),
		ReferenceLockDuration: durationOrDefault("REFERENCE_LOCK_DURATION", 30*time.Second),
		ReferenceBatchSize:    integerOrDefault("REFERENCE_BATCH_SIZE", 20),
		ReferenceMaxAttempts:  integerOrDefault("REFERENCE_MAX_ATTEMPTS", 10),
	}
	if strings.TrimSpace(config.HTTPAddress) == "" || strings.TrimSpace(config.DatabaseURL) == "" ||
		strings.TrimSpace(config.IntrospectionURL) == "" || strings.TrimSpace(config.IntrospectionClient) == "" ||
		strings.TrimSpace(config.IntrospectionSecret) == "" || strings.TrimSpace(config.ProviderClientID) == "" ||
		strings.TrimSpace(config.ProviderID) == "" || strings.TrimSpace(config.InternalClientID) == "" ||
		strings.TrimSpace(config.AWSRegion) == "" || strings.TrimSpace(config.SQSEndpoint) == "" ||
		strings.TrimSpace(config.SQSOutputQueueURL) == "" || strings.TrimSpace(config.SQSInputQueueURL) == "" ||
		strings.TrimSpace(config.SQSConsumerName) == "" || config.SQSWaitTimeSeconds < 0 || config.SQSWaitTimeSeconds > 20 ||
		config.SQSVisibilitySeconds <= 0 || config.SQSMaxMessages <= 0 || config.SQSMaxMessages > 10 || config.OutboxPollInterval <= 0 ||
		config.OutboxLockDuration <= 0 || config.OutboxBatchSize <= 0 ||
		config.ReferencePollInterval <= 0 || config.ReferenceLockDuration <= 0 ||
		config.ReferenceBatchSize <= 0 || config.ReferenceMaxAttempts <= 0 {
		return Config{}, errors.New("application configuration contains empty values")
	}
	return config, nil
}

func durationOrDefault(name string, fallback time.Duration) time.Duration {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return duration
}

func integerOrDefault(name string, fallback int) int {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

// environmentOrDefault preserva configuração explícita e fornece conveniência local.
func environmentOrDefault(name, fallback string) string {
	if value, exists := os.LookupEnv(name); exists {
		return value
	}
	return fallback
}
