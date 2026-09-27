package bootstrap

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/fx"
)

// TestAppModulesComposeCompleteGraph valida o grafo sem abrir conexões ou portas.
func TestAppModulesComposeCompleteGraph(t *testing.T) {
	if err := fx.ValidateApp(appOptions()...); err != nil {
		t.Fatalf("validate Fx application graph: %v", err)
	}
}

// TestAppStartsAndStopsWithRealDependencies comprova os hooks do pool, servidor
// e workers contra PostgreSQL, Keycloak e LocalStack reais quando solicitado.
func TestAppStartsAndStopsWithRealDependencies(t *testing.T) {
	if os.Getenv("JUNGLE_BOOTSTRAP_TEST") != "1" {
		t.Skip("set JUNGLE_BOOTSTRAP_TEST=1 with the Compose dependencies running")
	}
	t.Setenv("HTTP_ADDRESS", "127.0.0.1:0")
	t.Setenv("DATABASE_URL", "postgres://jungle_app:local_dev_only@127.0.0.1:5432/jungle_gaming?sslmode=disable")
	t.Setenv("OIDC_INTROSPECTION_URL", "http://127.0.0.1:8080/realms/jungle-dev/protocol/openid-connect/token/introspect")
	t.Setenv("SQS_ENDPOINT", "http://127.0.0.1:4566")
	t.Setenv("SQS_INPUT_QUEUE_URL", "http://127.0.0.1:4566/000000000000/wager-transactions.fifo")
	t.Setenv("SQS_OUTPUT_QUEUE_URL", "http://127.0.0.1:4566/000000000000/jungle-events.fifo")

	app := NewApp()
	startContext, cancelStart := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStart()
	if err := app.Start(startContext); err != nil {
		t.Fatalf("start Fx application: %v", err)
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStop()
	if err := app.Stop(stopContext); err != nil {
		t.Fatalf("stop Fx application: %v", err)
	}
}
