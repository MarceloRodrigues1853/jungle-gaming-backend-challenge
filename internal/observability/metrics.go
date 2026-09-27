// Package observability expõe métricas operacionais sem acoplar o domínio ao transporte.
package observability

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Metrics mantém apenas agregados numéricos; nenhum identificador de jogador ou
// payload financeiro é usado como label para evitar cardinalidade e exposição.
type Metrics struct {
	mu                        sync.RWMutex
	wagerResults              map[string]uint64
	idempotentReplays         uint64
	sqsRetries                uint64
	sqsDLQCandidates          uint64
	concurrencyConflicts      uint64
	outboxRetries             uint64
	outboxOldestLagSeconds    float64
	wagerProcessingCount      uint64
	wagerProcessingSecondsSum float64
	reconciliationDivergences uint64
}

// NewMetrics cria um registro independente por processo, apropriado para coleta
// e agregação posterior pelo Prometheus em ambientes com múltiplas instâncias.
func NewMetrics() *Metrics {
	return &Metrics{wagerResults: make(map[string]uint64)}
}

// ObserveWager registra o resultado persistido, replay e tempo do caso de uso.
func (metrics *Metrics) ObserveWager(status string, replay bool, elapsed time.Duration) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.wagerResults[status]++
	if replay {
		metrics.idempotentReplays++
	}
	metrics.wagerProcessingCount++
	metrics.wagerProcessingSecondsSum += elapsed.Seconds()
}

// ObserveSQSRetry registra reentregas e mensagens que atingiram o limite de
// recebimentos configurado para o redrive do broker.
func (metrics *Metrics) ObserveSQSRetry(_ int, dlqCandidate bool) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.sqsRetries++
	if dlqCandidate {
		metrics.sqsDLQCandidates++
	}
}

// ObserveConcurrencyConflict registra conflitos transitórios detectados pela persistência.
func (metrics *Metrics) ObserveConcurrencyConflict() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.concurrencyConflicts++
}

// ObserveOutboxRetry registra uma publicação que precisou ser reagendada.
func (metrics *Metrics) ObserveOutboxRetry() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.outboxRetries++
}

// ObserveOutboxLag mantém o atraso do evento mais antigo observado pelo publisher.
func (metrics *Metrics) ObserveOutboxLag(lag time.Duration) {
	if lag < 0 {
		lag = 0
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.outboxOldestLagSeconds = lag.Seconds()
}

// ObserveReconciliationDivergence registra uma inconsistência sem alterar o saldo.
func (metrics *Metrics) ObserveReconciliationDivergence() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.reconciliationDivergences++
}

// ServeHTTP publica o formato de texto do Prometheus.
func (metrics *Metrics) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	writeMetricHeader(writer, "jungle_wager_results_total", "counter", "Operacoes financeiras concluidas por status persistido.")
	statuses := make([]string, 0, len(metrics.wagerResults))
	for status := range metrics.wagerResults {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	for _, status := range statuses {
		_, _ = fmt.Fprintf(writer, "jungle_wager_results_total{status=%q} %d\n", status, metrics.wagerResults[status])
	}
	writeCounter(writer, "jungle_idempotent_replays_total", "Replays idempotentes devolvidos sem nova movimentacao.", metrics.idempotentReplays)
	writeCounter(writer, "jungle_sqs_retries_total", "Mensagens SQS mantidas para nova tentativa.", metrics.sqsRetries)
	writeCounter(writer, "jungle_sqs_dlq_candidates_total", "Mensagens que atingiram o limite de recebimentos para redrive.", metrics.sqsDLQCandidates)
	writeCounter(writer, "jungle_concurrency_conflicts_total", "Conflitos transitorios de concorrencia detectados.", metrics.concurrencyConflicts)
	writeCounter(writer, "jungle_outbox_retries_total", "Publicacoes da outbox reagendadas apos falha.", metrics.outboxRetries)
	writeGauge(writer, "jungle_outbox_oldest_lag_seconds", "Atraso do evento mais antigo observado pela outbox.", metrics.outboxOldestLagSeconds)
	writeCounter(writer, "jungle_wager_processing_duration_seconds_count", "Quantidade de operacoes observadas na latencia de processamento.", metrics.wagerProcessingCount)
	writeGauge(writer, "jungle_wager_processing_duration_seconds_sum", "Soma da latencia das operacoes financeiras em segundos.", metrics.wagerProcessingSecondsSum)
	writeCounter(writer, "jungle_reconciliation_divergences_total", "Divergencias encontradas pela reconciliacao.", metrics.reconciliationDivergences)
}

func writeCounter(writer http.ResponseWriter, name, help string, value uint64) {
	writeMetricHeader(writer, name, "counter", help)
	_, _ = fmt.Fprintf(writer, "%s %d\n", name, value)
}

func writeGauge(writer http.ResponseWriter, name, help string, value float64) {
	writeMetricHeader(writer, name, "gauge", help)
	_, _ = fmt.Fprintf(writer, "%s %g\n", name, value)
}

func writeMetricHeader(writer http.ResponseWriter, name, kind, help string) {
	_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}
