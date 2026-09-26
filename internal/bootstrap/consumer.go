package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/sqsconsumer"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
)

func newSQSConsumer(client *sqs.Client, wagers *application.WagerService, logger *slog.Logger, config Config) (*sqsconsumer.Consumer, error) {
	return sqsconsumer.New(client, wagers, logger, config.SQSInputQueueURL, config.SQSConsumerName,
		int32(config.SQSWaitTimeSeconds), int32(config.SQSVisibilitySeconds), int32(config.SQSMaxMessages))
}

// registerSQSConsumer acompanha o lifecycle do Fx: cancela o long polling e
// aguarda a mensagem em andamento antes de fechar PostgreSQL e demais recursos.
func registerSQSConsumer(lifecycle fx.Lifecycle, consumer *sqsconsumer.Consumer, logger *slog.Logger) {
	var cancel context.CancelFunc
	done := make(chan error, 1)
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			go func() { done <- consumer.Run(ctx) }()
			logger.Info("SQS wager consumer started")
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
