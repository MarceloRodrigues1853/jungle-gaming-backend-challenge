package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsExposeRequiredSignals garante que o endpoint contenha todas as
// categorias operacionais exigidas sem labels de alta cardinalidade.
func TestMetricsExposeRequiredSignals(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveWager("PROCESSED", true, 250*time.Millisecond)
	metrics.ObserveSQSRetry(5, true)
	metrics.ObserveConcurrencyConflict()
	metrics.ObserveOutboxRetry()
	metrics.ObserveOutboxLag(2 * time.Second)
	metrics.ObserveReconciliationDivergence()

	recorder := httptest.NewRecorder()
	metrics.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, expected := range []string{
		`jungle_wager_results_total{status="PROCESSED"} 1`,
		"jungle_idempotent_replays_total 1", "jungle_sqs_retries_total 1",
		"jungle_sqs_dlq_candidates_total 1", "jungle_concurrency_conflicts_total 1",
		"jungle_outbox_retries_total 1", "jungle_outbox_oldest_lag_seconds 2",
		"jungle_wager_processing_duration_seconds_count 1",
		"jungle_reconciliation_divergences_total 1",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics body does not contain %q:\n%s", expected, body)
		}
	}
}
