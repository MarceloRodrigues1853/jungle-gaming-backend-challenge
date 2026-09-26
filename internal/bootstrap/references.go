package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	postgresstore "github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/postgres"
	"go.uber.org/fx"
)

func newReferenceWorker(store *postgresstore.Store, ids application.IDGenerator, logger *slog.Logger, config Config) (*application.ReferenceWorker, error) {
	workerID, err := ids.NewID()
	if err != nil {
		return nil, err
	}
	return application.NewReferenceWorker(store, ids, logger, workerID,
		config.ReferencePollInterval, config.ReferenceLockDuration,
		config.ReferenceBatchSize, config.ReferenceMaxAttempts)
}

func registerReferenceWorker(lifecycle fx.Lifecycle, worker *application.ReferenceWorker, logger *slog.Logger) {
	var cancel context.CancelFunc
	done := make(chan error, 1)
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			go func() { done <- worker.Run(ctx) }()
			logger.Info("pending reference worker started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel == nil {
				return nil
			}
			cancel()
			select {
			case err := <-done:
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}
