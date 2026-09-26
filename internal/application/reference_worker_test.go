package application

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestReferenceWorkerReschedulesMissingReference fixa o backoff durável quando
// a operação original ainda não chegou.
func TestReferenceWorkerReschedulesMissingReference(t *testing.T) {
	now := fixedClock()
	repository := &referenceRepositorySpy{items: []PendingReference{{TransactionID: "pending-1", Attempts: 3, ExpiresAt: now.Add(time.Hour)}}}
	worker, err := NewReferenceWorker(repository, &sequenceIDs{values: []string{"ledger", "event", "balance"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "worker-1", time.Second, 30*time.Second, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	worker.clock = func() time.Time { return now }
	if err := worker.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.rescheduledID != "pending-1" || !repository.next.Equal(now.Add(4*time.Second)) {
		t.Fatalf("reschedule = %q at %s", repository.rescheduledID, repository.next)
	}
}

// TestReferenceWorkerRejectsAtAttemptLimit garante resultado terminal com
// failureCode estável quando a política de retry se esgota.
func TestReferenceWorkerRejectsAtAttemptLimit(t *testing.T) {
	now := fixedClock()
	repository := &referenceRepositorySpy{items: []PendingReference{{TransactionID: "pending-2", Attempts: 10, ExpiresAt: now.Add(time.Hour)}}}
	worker, err := NewReferenceWorker(repository, &sequenceIDs{values: []string{"reject-event"}},
		slog.New(slog.NewTextHandler(io.Discard, nil)), "worker-1", time.Second, 30*time.Second, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	worker.clock = func() time.Time { return now }
	if err := worker.ProcessBatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.rejectedID != "pending-2" || repository.rejectedEvent != "reject-event" {
		t.Fatalf("rejection = transaction %q, event %q", repository.rejectedID, repository.rejectedEvent)
	}
}

type referenceRepositorySpy struct {
	items                     []PendingReference
	resolved                  bool
	rescheduledID, rejectedID string
	rejectedEvent             string
	next                      time.Time
}

func (repository *referenceRepositorySpy) ClaimPendingReferences(context.Context, string, time.Time, time.Duration, int) ([]PendingReference, error) {
	return repository.items, nil
}

func (repository *referenceRepositorySpy) ResolvePendingReference(context.Context, string, string, string, WagerEventIDs, time.Time) (bool, error) {
	return repository.resolved, nil
}

func (repository *referenceRepositorySpy) ReschedulePendingReference(_ context.Context, transactionID, _ string, next time.Time) error {
	repository.rescheduledID, repository.next = transactionID, next
	return nil
}

func (repository *referenceRepositorySpy) RejectPendingReference(_ context.Context, transactionID, _ string, eventID string, _ time.Time) error {
	repository.rejectedID, repository.rejectedEvent = transactionID, eventID
	return nil
}
