// Package sqsoutbox publica snapshots duráveis da outbox no AWS SQS.
package sqsoutbox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Sender limita a dependência do adaptador à operação usada do SDK.
type Sender interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

// Publisher envia cada evento com deduplicação por eventId e grupo por agregado.
type Publisher struct {
	client   Sender
	queueURL string
}

// NewPublisher valida o cliente e o destino configurado.
func NewPublisher(client Sender, queueURL string) (*Publisher, error) {
	if client == nil || strings.TrimSpace(queueURL) == "" {
		return nil, errors.New("SQS client and output queue URL are required")
	}
	return &Publisher{client: client, queueURL: queueURL}, nil
}

// Publish preserva o eventId em reenvios e ordena eventos do mesmo agregado.
func (publisher *Publisher) Publish(ctx context.Context, event application.PendingOutboxEvent) error {
	_, err := publisher.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(publisher.queueURL),
		MessageBody:            aws.String(string(event.Payload)),
		MessageDeduplicationId: aws.String(event.ID),
		MessageGroupId:         aws.String(event.AggregateID),
	})
	if err != nil {
		return fmt.Errorf("send SQS event %s: %w", event.ID, err)
	}
	return nil
}
