package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestOutboxWorkerMarksSuccessfulPublication cobre o caminho de confirmação.
func TestOutboxWorkerMarksSuccessfulPublication(t *testing.T) {
	t.Parallel()
	repository := &outboxRepositorySpy{events: []PendingOutboxEvent{{ID: "event-1", Type: "WagerTransactionProcessed", Attempts: 1}}}
	publisher := &eventPublisherStub{}
	worker := newTestOutboxWorker(t, repository, publisher)
	if err := worker.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if publisher.published != "event-1" || repository.marked != "event-1" || repository.rescheduled != "" {
		t.Fatalf("published/marked/rescheduled = %q/%q/%q", publisher.published, repository.marked, repository.rescheduled)
	}
}

// TestOutboxWorkerReschedulesFailureWithBackoff preserva o evento quando o SQS falha.
func TestOutboxWorkerReschedulesFailureWithBackoff(t *testing.T) {
	t.Parallel()
	repository := &outboxRepositorySpy{events: []PendingOutboxEvent{{ID: "event-2", Type: "WalletBalanceChanged", Attempts: 3}}}
	publisher := &eventPublisherStub{err: errors.New("SQS unavailable")}
	worker := newTestOutboxWorker(t, repository, publisher)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	worker.clock = func() time.Time { return now }
	if err := worker.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.rescheduled != "event-2" || !repository.nextAttempt.Equal(now.Add(4*time.Second)) || repository.marked != "" {
		t.Fatalf("reschedule = %q at %s, marked %q", repository.rescheduled, repository.nextAttempt, repository.marked)
	}
}

type outboxRepositorySpy struct {
	events              []PendingOutboxEvent
	marked, rescheduled string
	nextAttempt         time.Time
}

func (spy *outboxRepositorySpy) ClaimOutbox(context.Context, string, time.Time, time.Duration, int) ([]PendingOutboxEvent, error) {
	return spy.events, nil
}

func (spy *outboxRepositorySpy) MarkOutboxPublished(_ context.Context, eventID, _ string, _ time.Time) error {
	spy.marked = eventID
	return nil
}

func (spy *outboxRepositorySpy) RescheduleOutbox(_ context.Context, eventID, _ string, next time.Time, _ string) error {
	spy.rescheduled, spy.nextAttempt = eventID, next
	return nil
}

type eventPublisherStub struct {
	published string
	err       error
}

func (stub *eventPublisherStub) Publish(_ context.Context, event PendingOutboxEvent) error {
	stub.published = event.ID
	return stub.err
}

func newTestOutboxWorker(t *testing.T, repository OutboxRepository, publisher EventPublisher) *OutboxWorker {
	t.Helper()
	worker, err := NewOutboxWorker(repository, publisher, slog.New(slog.NewTextHandler(io.Discard, nil)), "worker-1", time.Second, 30*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}
