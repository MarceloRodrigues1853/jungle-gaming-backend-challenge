package bootstrap

import (
	"context"
	"errors"
	"fmt"

	postgresstore "github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/postgres"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type queueInspector interface {
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type dependencyPinger interface{ Ping(context.Context) error }

// Readiness confirma as dependências mínimas usadas para aceitar novas entradas.
type Readiness struct {
	postgres dependencyPinger
	sqs      queueInspector
	queues   []string
}

func newReadiness(store *postgresstore.Store, client *sqs.Client, config Config) (*Readiness, error) {
	if store == nil || client == nil {
		return nil, errors.New("PostgreSQL store and SQS client are required for readiness")
	}
	return &Readiness{postgres: store, sqs: client,
		queues: []string{config.SQSInputQueueURL, config.SQSOutputQueueURL}}, nil
}

// Ping falha quando o banco ou qualquer fila obrigatória não responde.
func (readiness *Readiness) Ping(ctx context.Context) error {
	if readiness == nil || readiness.postgres == nil || readiness.sqs == nil {
		return errors.New("readiness is not initialized")
	}
	if err := readiness.postgres.Ping(ctx); err != nil {
		return fmt.Errorf("postgres readiness: %w", err)
	}
	for _, queueURL := range readiness.queues {
		if _, err := readiness.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		}); err != nil {
			return fmt.Errorf("SQS readiness for %s: %w", queueURL, err)
		}
	}
	return nil
}
