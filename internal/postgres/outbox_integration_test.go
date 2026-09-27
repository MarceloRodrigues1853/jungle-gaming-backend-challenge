package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresOutboxSurvivesCommitBeforePublication representa a queda logo depois
// do commit: uma nova instância ainda encontra e reserva o evento persistido.
func TestPostgresOutboxSurvivesCommitBeforePublication(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	eventID := insertPendingOutboxEvent(t, pool, "commit-before-publish")

	// Nenhum worker foi executado entre o INSERT confirmado e esta reserva. Isso
	// simula o processo que caiu depois do commit financeiro e antes de publicar.
	claimed, err := store.ClaimOutbox(context.Background(), "worker-after-restart", now, time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	event, ok := findOutboxEvent(claimed, eventID)
	if !ok || event.Attempts != 1 {
		t.Fatalf("committed event %q was not recovered after restart: %#v", eventID, claimed)
	}
	if err := store.MarkOutboxPublished(context.Background(), eventID, "worker-after-restart", now); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresOutboxRecoversPublicationWithoutConfirmation representa a queda no
// segundo intervalo crítico: o envio ocorreu, mas published_at não foi confirmado.
func TestPostgresOutboxRecoversPublicationWithoutConfirmation(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	eventID := insertPendingOutboxEvent(t, pool, "publish-before-confirm")

	first, err := store.ClaimOutbox(context.Background(), "worker-a", now, time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	firstEvent, ok := findOutboxEvent(first, eventID)
	if !ok || firstEvent.Attempts != 1 {
		t.Fatalf("first claim did not contain %q: %#v", eventID, first)
	}

	// O envio bem-sucedido é registrado pelo teste, mas a confirmação no banco é
	// deliberadamente omitida para simular a interrupção do worker nesse instante.
	publishedEventIDs := []string{firstEvent.ID}
	if err := store.MarkOutboxPublished(context.Background(), eventID, "worker-b", now); err == nil {
		t.Fatal("a worker that does not own the reservation marked the event as published")
	}
	recovered, err := store.ClaimOutbox(context.Background(), "worker-b", now.Add(2*time.Second), time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	recoveredEvent, ok := findOutboxEvent(recovered, eventID)
	if !ok || recoveredEvent.Attempts != 2 {
		t.Fatalf("expired reservation %q was not recovered: %#v", eventID, recovered)
	}
	publishedEventIDs = append(publishedEventIDs, recoveredEvent.ID)
	if publishedEventIDs[0] != publishedEventIDs[1] {
		t.Fatalf("republish changed eventId: %q then %q", publishedEventIDs[0], publishedEventIDs[1])
	}
	if err := store.MarkOutboxPublished(context.Background(), eventID, "worker-b", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var publishedAt *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT published_at FROM outbox_events WHERE event_id = $1`, eventID).Scan(&publishedAt); err != nil || publishedAt == nil {
		t.Fatalf("published_at = %v, error %v", publishedAt, err)
	}
}

// TestPostgresOutboxAllowsOnlyOneConcurrentOwner executa duas reservas reais ao
// mesmo tempo e comprova que FOR UPDATE SKIP LOCKED entrega o evento uma única vez.
func TestPostgresOutboxAllowsOnlyOneConcurrentOwner(t *testing.T) {
	store, pool := integrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	eventID := insertPendingOutboxEvent(t, pool, "two-publishers")
	start := make(chan struct{})
	type claimResult struct {
		worker string
		events []application.PendingOutboxEvent
		err    error
	}
	results := make(chan claimResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)

	for _, workerID := range []string{"publisher-a", "publisher-b"} {
		workerID := workerID
		go func() {
			ready.Done()
			<-start
			events, err := store.ClaimOutbox(context.Background(), workerID, now, 30*time.Second, 1000)
			results <- claimResult{worker: workerID, events: events, err: err}
		}()
	}
	ready.Wait()
	close(start)

	owners := make([]string, 0, 1)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("%s claim failed: %v", result.worker, result.err)
		}
		if _, ok := findOutboxEvent(result.events, eventID); ok {
			owners = append(owners, result.worker)
		}
	}
	if len(owners) != 1 {
		t.Fatalf("event %q owners = %v, want exactly one", eventID, owners)
	}
	if err := store.MarkOutboxPublished(context.Background(), eventID, owners[0], now); err != nil {
		t.Fatal(err)
	}
}

// insertPendingOutboxEvent cria uma linha isolada e já confirmada no PostgreSQL.
func insertPendingOutboxEvent(t *testing.T, pool *pgxpool.Pool, scenario string) string {
	t.Helper()
	eventID := fmt.Sprintf("it-outbox-%s-%d", scenario, time.Now().UnixNano())
	earliest := time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := pool.Exec(context.Background(), `INSERT INTO outbox_events
		(event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
		VALUES ($1, $2, 'TestEvent', jsonb_build_object('eventId', $1::text), $3, $3)`,
		eventID, "wallet-"+scenario, earliest)
	if err != nil {
		t.Fatalf("insert pending outbox event: %v", err)
	}
	return eventID
}

// findOutboxEvent localiza somente o evento do cenário e ignora outras linhas que
// possam existir no banco compartilhado de integração.
func findOutboxEvent(events []application.PendingOutboxEvent, eventID string) (application.PendingOutboxEvent, bool) {
	for _, event := range events {
		if event.ID == eventID {
			return event, true
		}
	}
	return application.PendingOutboxEvent{}, false
}
