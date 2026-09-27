package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func TestReadinessChecksPostgresAndEveryQueue(t *testing.T) {
	inspector := &queueInspectorStub{}
	readiness := &Readiness{postgres: pingerStub{}, sqs: inspector, queues: []string{"input", "output"}}
	if err := readiness.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inspector.calls != 2 {
		t.Fatalf("SQS calls = %d, want 2", inspector.calls)
	}
}

func TestReadinessStopsWhenSQSIsUnavailable(t *testing.T) {
	readiness := &Readiness{postgres: pingerStub{}, sqs: &queueInspectorStub{err: errors.New("SQS unavailable")}, queues: []string{"input"}}
	if err := readiness.Ping(context.Background()); err == nil {
		t.Fatal("Ping() error = nil")
	}
}

type pingerStub struct{ err error }

func (stub pingerStub) Ping(context.Context) error { return stub.err }

type queueInspectorStub struct {
	calls int
	err   error
}

func (stub *queueInspectorStub) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	stub.calls++
	return &sqs.GetQueueAttributesOutput{}, stub.err
}
