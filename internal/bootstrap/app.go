// Package bootstrap compõe infraestrutura e adaptadores com Uber Fx.
package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/auth"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/httpapi"
	postgresstore "github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
)

// NewApp declara a composição completa e deixa o lifecycle sob responsabilidade do Fx.
func NewApp() *fx.App {
	return fx.New(
		fx.Provide(
			LoadConfig,
			newLogger,
			newPostgresPool,
			postgresstore.NewStore,
			newIDGenerator,
			newIntrospector,
			newWagerService,
			newWalletService,
			newSQSClient,
			newOutboxPublisher,
			newOutboxWorker,
			newSQSConsumer,
			newReferenceWorker,
			newHTTPHandler,
		),
		fx.Invoke(registerHTTPServer, registerOutboxWorker, registerSQSConsumer, registerReferenceWorker),
	)
}

// newLogger cria logs estruturados sem incluir segredos de configuração.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

// newPostgresPool cria o pool sem fazer I/O e usa o lifecycle para validar e fechar.
func newPostgresPool(lifecycle fx.Lifecycle, config Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, err
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

// newIDGenerator expõe a implementação por sua porta da camada de aplicação.
func newIDGenerator() application.IDGenerator {
	return UUIDGenerator{}
}

// newIntrospector configura a allowlist entre cliente OIDC e provedor interno.
func newIntrospector(config Config) (*auth.Introspector, error) {
	return auth.NewIntrospector(
		config.IntrospectionURL,
		config.IntrospectionClient,
		config.IntrospectionSecret,
		map[string]auth.ClientIdentity{
			config.ProviderClientID: {Role: auth.RoleProvider, ProviderID: config.ProviderID},
			config.InternalClientID: {Role: auth.RoleInternal},
		},
		nil,
	)
}

// newWagerService liga o mesmo Store às portas de processamento e referência.
func newWagerService(store *postgresstore.Store, ids application.IDGenerator) (*application.WagerService, error) {
	return application.NewWagerService(store, store, ids, nil)
}

// newWalletService compõe a abertura e leitura de carteiras no mesmo Store.
func newWalletService(store *postgresstore.Store, ids application.IDGenerator) (*application.WalletService, error) {
	return application.NewWalletService(store, ids, nil)
}

// newHTTPHandler conecta os adaptadores concretos ao contrato HTTP.
func newHTTPHandler(introspector *auth.Introspector, wagers *application.WagerService, wallets *application.WalletService, store *postgresstore.Store) (http.Handler, error) {
	return httpapi.NewHandler(introspector, wagers, wallets, store, store)
}
