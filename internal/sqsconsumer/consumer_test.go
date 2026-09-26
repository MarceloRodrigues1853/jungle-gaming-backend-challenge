package sqsconsumer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// TestConsumerCommitsBeforeDeletingMessage verifica a ordem observável e o
// mapeamento completo do envelope para o caso de uso compartilhado.
func TestConsumerCommitsBeforeDeletingMessage(t *testing.T) {
	client := &clientSpy{}
	submitter := &submitterSpy{onSubmit: func() { client.committed = true }}
	consumer, err := New(client, submitter, slog.New(slog.NewTextHandler(io.Discard, nil)),
		"queue-url", "wager-consumer", 0, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	consumer.clock = func() time.Time { return time.Date(2026, 9, 26, 20, 0, 0, 0, time.UTC) }
	message := validMessage()
	if err := consumer.process(context.Background(), message); err != nil {
		t.Fatalf("process() error = %v", err)
	}
	if !client.deleted || !client.committed {
		t.Fatalf("committed/deleted = %v/%v, want true/true", client.committed, client.deleted)
	}
	if submitter.providerID != "provider-a" || submitter.delivery.MessageID != "msg-123" || submitter.command.Kind != "BET" {
		t.Fatalf("submit mapping = provider %q, message %q, kind %q", submitter.providerID, submitter.delivery.MessageID, submitter.command.Kind)
	}
}

// TestConsumerLeavesInvalidMessageForRedrive garante que mensagens permanentes
// não sejam apagadas e possam atingir a DLQ após o limite do broker.
func TestConsumerLeavesInvalidMessageForRedrive(t *testing.T) {
	client := &clientSpy{}
	consumer, err := New(client, &submitterSpy{}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		"queue-url", "wager-consumer", 0, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Body = aws.String(`{"messageId":"msg-123","type":"WrongType"}`)
	if err := consumer.process(context.Background(), message); err == nil {
		t.Fatal("process() error = nil, want invalid envelope")
	}
	if client.deleted || !client.visibilityChanged {
		t.Fatalf("deleted/visibilityChanged = %v/%v, want false/true", client.deleted, client.visibilityChanged)
	}
}

func validMessage() types.Message {
	return types.Message{MessageId: aws.String("sqs-id"), ReceiptHandle: aws.String("receipt"),
		Attributes: map[string]string{"ApproximateReceiveCount": "1"}, Body: aws.String(`{
			"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-26T20:00:00Z",
			"data":{"providerId":"provider-a","externalTransactionId":"external-1",
			"idempotencyKey":"provider-a:external-1","playerId":"player-1","walletId":"wallet-1",
			"roundId":"round-1","gameId":"game-1","kind":"BET",
			"money":{"amount":"25.00","currency":"BRL"}}
		}`)}
}

type clientSpy struct {
	committed, deleted, visibilityChanged bool
}

func (client *clientSpy) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{}, nil
}

func (client *clientSpy) DeleteMessage(_ context.Context, _ *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	if !client.committed {
		panic("message deleted before financial commit")
	}
	client.deleted = true
	return &sqs.DeleteMessageOutput{}, nil
}

func (client *clientSpy) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	client.visibilityChanged = true
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

type submitterSpy struct {
	providerID string
	command    application.SubmitWagerCommand
	delivery   application.InboxDelivery
	onSubmit   func()
}

func (submitter *submitterSpy) SubmitFromInbox(_ context.Context, providerID, _ string, command application.SubmitWagerCommand, delivery application.InboxDelivery) (domain.WagerProcessingResult, error) {
	submitter.providerID, submitter.command, submitter.delivery = providerID, command, delivery
	if submitter.onSubmit != nil {
		submitter.onSubmit()
	}
	return domain.WagerProcessingResult{Status: domain.TransactionProcessed}, nil
}
