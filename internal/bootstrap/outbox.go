package bootstrap

import (
	"context"
	"errors"
	"log/slog"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/observability"
	postgresstore "github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/postgres"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/sqsoutbox"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"
)

// newSQSClient configura credenciais fictícias somente para o LocalStack local.
func newSQSClient(config Config) (*sqs.Client, error) {
	awsConfiguration, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(config.AWSRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsConfiguration, func(options *sqs.Options) {
		options.BaseEndpoint = aws.String(config.SQSEndpoint)
	}), nil
}

func newOutboxPublisher(client *sqs.Client, config Config) (*sqsoutbox.Publisher, error) {
	return sqsoutbox.NewPublisher(client, config.SQSOutputQueueURL)
}

func newOutboxWorker(store *postgresstore.Store, publisher *sqsoutbox.Publisher, logger *slog.Logger, metrics *observability.Metrics, ids application.IDGenerator, config Config) (*application.OutboxWorker, error) {
	workerID, err := ids.NewID()
	if err != nil {
		return nil, err
	}
	return application.NewOutboxWorker(store, publisher, logger, workerID,
		config.OutboxPollInterval, config.OutboxLockDuration, config.OutboxBatchSize, metrics)
}

// registerOutboxWorker inicia e encerra o loop junto com o lifecycle do Fx.
func registerOutboxWorker(lifecycle fx.Lifecycle, worker *application.OutboxWorker, logger *slog.Logger) {
	var cancel context.CancelFunc
	done := make(chan error, 1)
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			go func() { done <- worker.Run(ctx) }()
			logger.Info("outbox worker started")
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
