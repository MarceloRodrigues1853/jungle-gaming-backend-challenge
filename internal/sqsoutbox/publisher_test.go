package sqsoutbox

import (
	"context"
	"testing"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// TestPublisherUsesStableFIFOIdentifiers valida ordenação por agregado e deduplicação por evento.
func TestPublisherUsesStableFIFOIdentifiers(t *testing.T) {
	t.Parallel()
	sender := &senderSpy{}
	publisher, err := NewPublisher(sender, "http://local/queue.fifo")
	if err != nil {
		t.Fatal(err)
	}
	err = publisher.Publish(context.Background(), application.PendingOutboxEvent{
		ID: "event-1", AggregateID: "wallet-1", Type: "WalletBalanceChanged", Payload: []byte(`{"eventId":"event-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if *sender.input.MessageDeduplicationId != "event-1" || *sender.input.MessageGroupId != "wallet-1" || *sender.input.MessageBody != `{"eventId":"event-1"}` {
		t.Fatalf("SendMessage input = %#v", sender.input)
	}
}

type senderSpy struct{ input *sqs.SendMessageInput }

func (spy *senderSpy) SendMessage(_ context.Context, input *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	spy.input = input
	return &sqs.SendMessageOutput{}, nil
}
