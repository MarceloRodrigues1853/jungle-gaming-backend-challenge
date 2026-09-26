// Package sqsconsumer adapta mensagens SQS ao mesmo caso de uso usado pelo HTTP.
package sqsconsumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Client limita o adaptador às operações SQS necessárias para receber,
// confirmar ou adiar uma mensagem.
type Client interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

// Submitter expõe a entrada que inclui a inbox na mesma transação financeira.
type Submitter interface {
	SubmitFromInbox(context.Context, string, string, application.SubmitWagerCommand, application.InboxDelivery) (domain.WagerProcessingResult, error)
}

// Consumer busca mensagens em lotes pequenos e só as remove após o commit.
type Consumer struct {
	client            Client
	submitter         Submitter
	logger            *slog.Logger
	queueURL          string
	consumerName      string
	waitTimeSeconds   int32
	visibilityTimeout int32
	maxMessages       int32
	clock             func() time.Time
}

// New valida a configuração operacional do consumidor.
func New(client Client, submitter Submitter, logger *slog.Logger, queueURL, consumerName string, waitTimeSeconds, visibilityTimeout, maxMessages int32) (*Consumer, error) {
	if client == nil || submitter == nil || logger == nil || strings.TrimSpace(queueURL) == "" || strings.TrimSpace(consumerName) == "" {
		return nil, errors.New("SQS client, submitter, logger, queue URL, and consumer name are required")
	}
	if waitTimeSeconds < 0 || waitTimeSeconds > 20 || visibilityTimeout <= 0 || maxMessages <= 0 || maxMessages > 10 {
		return nil, errors.New("invalid SQS polling configuration")
	}
	return &Consumer{client: client, submitter: submitter, logger: logger,
		queueURL: queueURL, consumerName: consumerName, waitTimeSeconds: waitTimeSeconds,
		visibilityTimeout: visibilityTimeout, maxMessages: maxMessages, clock: time.Now}, nil
}

// Run usa long polling e interrompe novas buscas assim que o contexto é cancelado.
func (consumer *Consumer) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		output, err := consumer.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(consumer.queueURL), MaxNumberOfMessages: consumer.maxMessages,
			WaitTimeSeconds: consumer.waitTimeSeconds, VisibilityTimeout: consumer.visibilityTimeout,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			consumer.logger.Error("SQS receive failed", "consumer", consumer.consumerName, "error", err)
			if !waitForRetry(ctx, time.Second) {
				return ctx.Err()
			}
			continue
		}
		for _, message := range output.Messages {
			if err := consumer.process(ctx, message); err != nil && ctx.Err() == nil {
				consumer.logger.Warn("SQS message left for retry", "consumer", consumer.consumerName,
					"sqsMessageId", aws.ToString(message.MessageId), "attempt", receiveCount(message), "error", err)
			}
		}
	}
}

func (consumer *Consumer) process(ctx context.Context, message types.Message) error {
	body := aws.ToString(message.Body)
	envelope, err := decodeEnvelope(body)
	if err != nil {
		consumer.deferMessage(ctx, message)
		return err
	}
	hash := sha256.Sum256([]byte(body))
	delivery := application.InboxDelivery{ConsumerName: consumer.consumerName,
		MessageID: envelope.MessageID, PayloadHash: hash, ReceivedAt: consumer.clock().UTC()}
	_, err = consumer.submitter.SubmitFromInbox(ctx, envelope.Data.ProviderID,
		envelope.Data.IdempotencyKey, envelope.Data.command(), delivery)
	if err != nil {
		consumer.deferMessage(ctx, message)
		return fmt.Errorf("process wager message %s: %w", envelope.MessageID, err)
	}
	if strings.TrimSpace(aws.ToString(message.ReceiptHandle)) == "" {
		return errors.New("SQS receipt handle is missing")
	}
	if _, err := consumer.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(consumer.queueURL), ReceiptHandle: message.ReceiptHandle,
	}); err != nil {
		return fmt.Errorf("delete committed SQS message %s: %w", envelope.MessageID, err)
	}
	consumer.logger.Info("SQS wager message completed", "consumer", consumer.consumerName,
		"messageId", envelope.MessageID, "providerId", envelope.Data.ProviderID,
		"externalTransactionId", envelope.Data.ExternalTransactionID)
	return nil
}

// deferMessage aplica backoff pela visibilidade; após o limite configurado no
// redrive, o próprio SQS move a mensagem para a DLQ.
func (consumer *Consumer) deferMessage(ctx context.Context, message types.Message) {
	receipt := aws.ToString(message.ReceiptHandle)
	if receipt == "" {
		return
	}
	seconds := consumer.visibilityTimeout * int32(receiveCount(message))
	if seconds > 300 {
		seconds = 300
	}
	_, _ = consumer.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(consumer.queueURL), ReceiptHandle: aws.String(receipt), VisibilityTimeout: seconds,
	})
}

func receiveCount(message types.Message) int {
	value, err := strconv.Atoi(message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil || value < 1 {
		return 1
	}
	return value
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type envelope struct {
	MessageID  string       `json:"messageId"`
	Type       string       `json:"type"`
	OccurredAt string       `json:"occurredAt"`
	Data       envelopeData `json:"data"`
}

type envelopeData struct {
	ProviderID                     string                 `json:"providerId"`
	ExternalTransactionID          string                 `json:"externalTransactionId"`
	IdempotencyKey                 string                 `json:"idempotencyKey"`
	PlayerID                       string                 `json:"playerId"`
	WalletID                       string                 `json:"walletId"`
	RoundID                        string                 `json:"roundId"`
	GameID                         string                 `json:"gameId"`
	Kind                           string                 `json:"kind"`
	Money                          application.MoneyInput `json:"money"`
	ReferenceExternalTransactionID string                 `json:"referenceExternalTransactionId,omitempty"`
}

func (data envelopeData) command() application.SubmitWagerCommand {
	return application.SubmitWagerCommand{ExternalTransactionID: data.ExternalTransactionID,
		PlayerID: data.PlayerID, WalletID: data.WalletID, RoundID: data.RoundID,
		GameID: data.GameID, Kind: data.Kind, Money: data.Money,
		ReferenceExternalID: data.ReferenceExternalTransactionID}
}

func decodeEnvelope(body string) (envelope, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(body))
	decoder.DisallowUnknownFields()
	var value envelope
	if err := decoder.Decode(&value); err != nil {
		return envelope{}, fmt.Errorf("decode SQS envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return envelope{}, errors.New("SQS envelope must contain one JSON object")
	}
	if strings.TrimSpace(value.MessageID) == "" || value.Type != "WagerTransactionRequested" {
		return envelope{}, errors.New("SQS envelope has invalid messageId or type")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.OccurredAt); err != nil {
		return envelope{}, fmt.Errorf("SQS envelope has invalid occurredAt: %w", err)
	}
	return value, nil
}
