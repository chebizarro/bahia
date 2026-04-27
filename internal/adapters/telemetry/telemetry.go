// Package telemetry provides OpenTelemetry integration for tracing and metrics.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Config holds telemetry configuration.
type Config struct {
	Enabled       bool
	ServiceName   string
	ServiceVersion string
	OTLPEndpoint  string // e.g. "localhost:4317" for gRPC, "localhost:4318" for HTTP
	OTLPProtocol  string // "grpc" or "http", defaults to "grpc"
	Environment   string // e.g. "production", "staging"
}

// Provider manages telemetry lifecycle.
type Provider struct {
	config  Config
	logger  *zap.Logger
	metrics *Metrics
	mu      sync.Mutex
}

// Metrics collects application-level counters and gauges.
type Metrics struct {
	mu sync.RWMutex

	// HTTP metrics
	HTTPRequestsTotal      map[string]int64   // key: method:path:status
	HTTPRequestDurations   []float64          // in seconds
	HTTPRequestDurationSum float64            // sum for average calculation

	// Deployment metrics
	DeploymentsTotal   map[string]int64 // key: service:env:status
	DriftDetectedTotal int64

	// Reconciliation metrics
	ReconcileDurations     []float64
	ReconcileStatesChecked int64
	ReconcileTotal         int64

	// Nostr metrics
	NostrEventsPublished map[string]int64 // key: kind
	NostrEventsReceived  map[string]int64

	// Nostr protocol metrics (NIP-01 frames)
	NostrEOSELatencies    []float64        // EOSE latency in seconds
	NostrPublishOK        map[string]int64 // key: relay_url - successful publishes
	NostrPublishFailed    map[string]int64 // key: relay_url:reason - failed publishes
	NostrPublishLatencies []float64        // publish latency in seconds
	NostrReconnects       map[string]int64 // key: relay_url - reconnection count
	NostrBackoffDurations []float64        // backoff durations in seconds

	// Relay health metrics
	NostrRelayHealthy   map[string]bool    // key: relay_url - is healthy
	NostrRelayDegraded  map[string]bool    // key: relay_url - is degraded
	NostrRelaySuccessRate map[string]float64 // key: relay_url - success rate (0-1)

	// Worker metrics
	WorkersActive    int64
	WorkersTotal     int64
	LoomJobsInflight int64
	LoomJobsTotal    map[string]int64 // key: status (completed, failed, cancelled)

	// Cashu payment metrics
	CashuPaymentsTotal  map[string]int64 // key: status (sent, redeemed, failed)
	CashuPaymentsSats   int64            // total sats paid
	CashuWalletBalance  map[string]int64 // key: mint_url
}

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		HTTPRequestsTotal:    make(map[string]int64),
		DeploymentsTotal:     make(map[string]int64),
		NostrEventsPublished: make(map[string]int64),
		NostrEventsReceived:  make(map[string]int64),
		NostrPublishOK:        make(map[string]int64),
		NostrPublishFailed:    make(map[string]int64),
		NostrReconnects:       make(map[string]int64),
		NostrRelayHealthy:     make(map[string]bool),
		NostrRelayDegraded:    make(map[string]bool),
		NostrRelaySuccessRate: make(map[string]float64),
		LoomJobsTotal:         make(map[string]int64),
		CashuPaymentsTotal:   make(map[string]int64),
		CashuWalletBalance:   make(map[string]int64),
	}
}

// Setup initializes telemetry with the given configuration.
// Returns a Provider that can be used to record metrics and shut down cleanly.
func Setup(cfg Config, logger *zap.Logger) *Provider {
	p := &Provider{
		config:  cfg,
		logger:  logger,
		metrics: NewMetrics(),
	}

	if !cfg.Enabled {
		logger.Info("telemetry disabled")
		return p
	}

	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = "bahia"
	}

	// In production, this would initialize OTLP exporters:
	// - go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
	// - go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc
	// For now, we use manual metrics collection that exports via /metrics

	logger.Info("telemetry initialized",
		zap.String("service", serviceName),
		zap.String("version", cfg.ServiceVersion),
		zap.String("environment", cfg.Environment),
		zap.String("otlp_endpoint", cfg.OTLPEndpoint),
	)

	return p
}

// GetMetrics returns the metrics collector.
func (p *Provider) GetMetrics() *Metrics {
	return p.metrics
}

// Shutdown cleanly shuts down telemetry exporters.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.logger.Info("telemetry shutdown complete")
	return nil
}

// --- HTTP Metrics ---

// RecordHTTPRequest records an HTTP request metric.
func (m *Metrics) RecordHTTPRequest(method, path string, status int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s:%d", method, path, status)
	m.HTTPRequestsTotal[key]++
	
	durSec := duration.Seconds()
	m.HTTPRequestDurations = append(m.HTTPRequestDurations, durSec)
	m.HTTPRequestDurationSum += durSec
	
	// Keep only last 1000 samples for memory efficiency
	if len(m.HTTPRequestDurations) > 1000 {
		m.HTTPRequestDurations = m.HTTPRequestDurations[len(m.HTTPRequestDurations)-1000:]
	}
}

// --- Deployment Metrics ---

// RecordDeployment records a deployment metric.
func (m *Metrics) RecordDeployment(service, environment, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s:%s", service, environment, status)
	m.DeploymentsTotal[key]++
}

// RecordDriftDetected increments the drift detection counter.
func (m *Metrics) RecordDriftDetected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DriftDetectedTotal++
}

// --- Reconciliation Metrics ---

// RecordReconcile records a reconciliation cycle.
func (m *Metrics) RecordReconcile(duration time.Duration, statesChecked int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ReconcileDurations = append(m.ReconcileDurations, duration.Seconds())
	m.ReconcileStatesChecked = int64(statesChecked)
	m.ReconcileTotal++
	
	// Keep only last 100 samples
	if len(m.ReconcileDurations) > 100 {
		m.ReconcileDurations = m.ReconcileDurations[len(m.ReconcileDurations)-100:]
	}
}

// --- Nostr Metrics ---

// RecordNostrPublished increments the published event counter for a kind.
func (m *Metrics) RecordNostrPublished(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrEventsPublished[kind]++
}

// RecordNostrReceived increments the received event counter for a kind.
func (m *Metrics) RecordNostrReceived(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrEventsReceived[kind]++
}

// RecordNostrEOSE records the latency from subscription start to EOSE receipt.
func (m *Metrics) RecordNostrEOSE(latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrEOSELatencies = append(m.NostrEOSELatencies, latency.Seconds())
	// Keep only last 1000 samples for memory efficiency
	if len(m.NostrEOSELatencies) > 1000 {
		m.NostrEOSELatencies = m.NostrEOSELatencies[len(m.NostrEOSELatencies)-1000:]
	}
}

// RecordNostrPublishOK records a successful publish to a relay.
func (m *Metrics) RecordNostrPublishOK(relayURL string, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrPublishOK[relayURL]++
	m.NostrPublishLatencies = append(m.NostrPublishLatencies, latency.Seconds())
	// Keep only last 1000 samples
	if len(m.NostrPublishLatencies) > 1000 {
		m.NostrPublishLatencies = m.NostrPublishLatencies[len(m.NostrPublishLatencies)-1000:]
	}
}

// RecordNostrPublishFailed records a failed publish to a relay with reason.
// Reasons: auth-required, rate-limited, blocked, duplicate, error
func (m *Metrics) RecordNostrPublishFailed(relayURL, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s:%s", relayURL, reason)
	m.NostrPublishFailed[key]++
}

// RecordNostrReconnect records a reconnection attempt to a relay.
func (m *Metrics) RecordNostrReconnect(relayURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrReconnects[relayURL]++
}

// RecordNostrBackoff records a backoff duration before reconnection.
func (m *Metrics) RecordNostrBackoff(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrBackoffDurations = append(m.NostrBackoffDurations, duration.Seconds())
	// Keep only last 500 samples
	if len(m.NostrBackoffDurations) > 500 {
		m.NostrBackoffDurations = m.NostrBackoffDurations[len(m.NostrBackoffDurations)-500:]
	}
}

// SetNostrRelayHealth updates the health status for a relay.
// healthy: true if relay is healthy, degraded: true if degraded but not unhealthy.
func (m *Metrics) SetNostrRelayHealth(relayURL string, healthy, degraded bool, successRate float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrRelayHealthy[relayURL] = healthy
	m.NostrRelayDegraded[relayURL] = degraded
	m.NostrRelaySuccessRate[relayURL] = successRate
}

// --- Worker Metrics ---

// SetWorkersActive sets the active worker gauge.
func (m *Metrics) SetWorkersActive(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkersActive = n
}

// SetWorkersTotal sets the total known workers.
func (m *Metrics) SetWorkersTotal(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WorkersTotal = n
}

// SetLoomJobsInflight sets the in-flight jobs gauge.
func (m *Metrics) SetLoomJobsInflight(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LoomJobsInflight = n
}

// RecordLoomJob records a Loom job completion.
func (m *Metrics) RecordLoomJob(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LoomJobsTotal[status]++
}

// --- Cashu Payment Metrics ---

// RecordCashuPayment records a Cashu payment.
func (m *Metrics) RecordCashuPayment(status string, amountSats int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CashuPaymentsTotal[status]++
	if status == "sent" || status == "redeemed" {
		m.CashuPaymentsSats += amountSats
	}
}

// SetCashuWalletBalance sets the wallet balance for a mint.
func (m *Metrics) SetCashuWalletBalance(mintURL string, balance int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CashuWalletBalance[mintURL] = balance
}

// --- Prometheus Export ---

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible metrics.
func (p *Provider) MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := p.metrics
		m.mu.RLock()
		defer m.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		// HTTP metrics
		fmt.Fprintln(w, "# HELP bahia_http_requests_total Total HTTP requests by method, path, and status code")
		fmt.Fprintln(w, "# TYPE bahia_http_requests_total counter")
		for key, count := range m.HTTPRequestsTotal {
			fmt.Fprintf(w, "bahia_http_requests_total{key=%q} %d\n", key, count)
		}

		if len(m.HTTPRequestDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_http_request_duration_seconds HTTP request duration in seconds")
			fmt.Fprintln(w, "# TYPE bahia_http_request_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.HTTPRequestDurations)
			total := int64(len(m.HTTPRequestDurations))
			fmt.Fprintf(w, "bahia_http_request_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_http_request_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_http_request_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_http_request_duration_seconds_sum %.6f\n", m.HTTPRequestDurationSum)
			fmt.Fprintf(w, "bahia_http_request_duration_seconds_count %d\n", total)
		}

		// Deployment metrics
		fmt.Fprintln(w, "# HELP bahia_deployments_total Total deployments by service, environment, and status")
		fmt.Fprintln(w, "# TYPE bahia_deployments_total counter")
		for key, count := range m.DeploymentsTotal {
			fmt.Fprintf(w, "bahia_deployments_total{key=%q} %d\n", key, count)
		}

		fmt.Fprintln(w, "# HELP bahia_drift_detected_total Total drift detections")
		fmt.Fprintln(w, "# TYPE bahia_drift_detected_total counter")
		fmt.Fprintf(w, "bahia_drift_detected_total %d\n", m.DriftDetectedTotal)

		// Reconciliation metrics
		fmt.Fprintln(w, "# HELP bahia_reconcile_total Total reconciliation cycles")
		fmt.Fprintln(w, "# TYPE bahia_reconcile_total counter")
		fmt.Fprintf(w, "bahia_reconcile_total %d\n", m.ReconcileTotal)

		fmt.Fprintln(w, "# HELP bahia_reconcile_states_checked Number of states checked in last reconcile")
		fmt.Fprintln(w, "# TYPE bahia_reconcile_states_checked gauge")
		fmt.Fprintf(w, "bahia_reconcile_states_checked %d\n", m.ReconcileStatesChecked)

		if len(m.ReconcileDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_reconcile_duration_seconds Reconciliation cycle duration in seconds")
			fmt.Fprintln(w, "# TYPE bahia_reconcile_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.ReconcileDurations)
			fmt.Fprintf(w, "bahia_reconcile_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_reconcile_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_reconcile_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
		}

		// Nostr metrics
		fmt.Fprintln(w, "# HELP bahia_nostr_events_published_total Nostr events published by kind")
		fmt.Fprintln(w, "# TYPE bahia_nostr_events_published_total counter")
		for kind, count := range m.NostrEventsPublished {
			fmt.Fprintf(w, "bahia_nostr_events_published_total{kind=%q} %d\n", kind, count)
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_events_received_total Nostr events received by kind")
		fmt.Fprintln(w, "# TYPE bahia_nostr_events_received_total counter")
		for kind, count := range m.NostrEventsReceived {
			fmt.Fprintf(w, "bahia_nostr_events_received_total{kind=%q} %d\n", kind, count)
		}

		// Nostr protocol metrics (EOSE, OK, reconnects)
		if len(m.NostrEOSELatencies) > 0 {
			fmt.Fprintln(w, "# HELP bahia_nostr_eose_latency_seconds Time from subscription start to EOSE receipt")
			fmt.Fprintln(w, "# TYPE bahia_nostr_eose_latency_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrEOSELatencies)
			fmt.Fprintf(w, "bahia_nostr_eose_latency_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_nostr_eose_latency_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_nostr_eose_latency_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_nostr_eose_latency_seconds_count %d\n", len(m.NostrEOSELatencies))
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_publish_ok_total Successful publishes by relay")
		fmt.Fprintln(w, "# TYPE bahia_nostr_publish_ok_total counter")
		for relay, count := range m.NostrPublishOK {
			fmt.Fprintf(w, "bahia_nostr_publish_ok_total{relay=%q} %d\n", relay, count)
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_publish_failed_total Failed publishes by relay and reason")
		fmt.Fprintln(w, "# TYPE bahia_nostr_publish_failed_total counter")
		for key, count := range m.NostrPublishFailed {
			fmt.Fprintf(w, "bahia_nostr_publish_failed_total{key=%q} %d\n", key, count)
		}

		if len(m.NostrPublishLatencies) > 0 {
			fmt.Fprintln(w, "# HELP bahia_nostr_publish_latency_seconds Publish latency in seconds")
			fmt.Fprintln(w, "# TYPE bahia_nostr_publish_latency_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrPublishLatencies)
			fmt.Fprintf(w, "bahia_nostr_publish_latency_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_nostr_publish_latency_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_nostr_publish_latency_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_nostr_publish_latency_seconds_count %d\n", len(m.NostrPublishLatencies))
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_reconnects_total Relay reconnection attempts")
		fmt.Fprintln(w, "# TYPE bahia_nostr_reconnects_total counter")
		for relay, count := range m.NostrReconnects {
			fmt.Fprintf(w, "bahia_nostr_reconnects_total{relay=%q} %d\n", relay, count)
		}

		if len(m.NostrBackoffDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_nostr_backoff_seconds Backoff duration before reconnection")
			fmt.Fprintln(w, "# TYPE bahia_nostr_backoff_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrBackoffDurations)
			fmt.Fprintf(w, "bahia_nostr_backoff_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_nostr_backoff_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_nostr_backoff_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_nostr_backoff_seconds_count %d\n", len(m.NostrBackoffDurations))
		}

		// Relay health metrics
		fmt.Fprintln(w, "# HELP bahia_nostr_relay_healthy Whether relay is healthy (1=yes, 0=no)")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relay_healthy gauge")
		for relay, healthy := range m.NostrRelayHealthy {
			val := 0
			if healthy {
				val = 1
			}
			fmt.Fprintf(w, "bahia_nostr_relay_healthy{relay=%q} %d\n", relay, val)
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_relay_degraded Whether relay is degraded (1=yes, 0=no)")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relay_degraded gauge")
		for relay, degraded := range m.NostrRelayDegraded {
			val := 0
			if degraded {
				val = 1
			}
			fmt.Fprintf(w, "bahia_nostr_relay_degraded{relay=%q} %d\n", relay, val)
		}

		fmt.Fprintln(w, "# HELP bahia_nostr_relay_success_rate Relay publish success rate (0.0-1.0)")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relay_success_rate gauge")
		for relay, rate := range m.NostrRelaySuccessRate {
			fmt.Fprintf(w, "bahia_nostr_relay_success_rate{relay=%q} %.4f\n", relay, rate)
		}

		// Aggregate relay health counts
		healthyCount := 0
		degradedCount := 0
		unhealthyCount := 0
		for relay := range m.NostrRelayHealthy {
			if m.NostrRelayHealthy[relay] {
				healthyCount++
			} else if m.NostrRelayDegraded[relay] {
				degradedCount++
			} else {
				unhealthyCount++
			}
		}
		fmt.Fprintln(w, "# HELP bahia_nostr_relays_healthy_total Count of healthy relays")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relays_healthy_total gauge")
		fmt.Fprintf(w, "bahia_nostr_relays_healthy_total %d\n", healthyCount)

		fmt.Fprintln(w, "# HELP bahia_nostr_relays_degraded_total Count of degraded relays")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relays_degraded_total gauge")
		fmt.Fprintf(w, "bahia_nostr_relays_degraded_total %d\n", degradedCount)

		fmt.Fprintln(w, "# HELP bahia_nostr_relays_unhealthy_total Count of unhealthy relays")
		fmt.Fprintln(w, "# TYPE bahia_nostr_relays_unhealthy_total gauge")
		fmt.Fprintf(w, "bahia_nostr_relays_unhealthy_total %d\n", unhealthyCount)

		// Worker metrics
		fmt.Fprintln(w, "# HELP bahia_workers_active Currently active (online) workers")
		fmt.Fprintln(w, "# TYPE bahia_workers_active gauge")
		fmt.Fprintf(w, "bahia_workers_active %d\n", m.WorkersActive)

		fmt.Fprintln(w, "# HELP bahia_workers_total Total known workers")
		fmt.Fprintln(w, "# TYPE bahia_workers_total gauge")
		fmt.Fprintf(w, "bahia_workers_total %d\n", m.WorkersTotal)

		fmt.Fprintln(w, "# HELP bahia_loom_jobs_inflight Currently in-flight Loom jobs")
		fmt.Fprintln(w, "# TYPE bahia_loom_jobs_inflight gauge")
		fmt.Fprintf(w, "bahia_loom_jobs_inflight %d\n", m.LoomJobsInflight)

		fmt.Fprintln(w, "# HELP bahia_loom_jobs_total Total Loom jobs by status")
		fmt.Fprintln(w, "# TYPE bahia_loom_jobs_total counter")
		for status, count := range m.LoomJobsTotal {
			fmt.Fprintf(w, "bahia_loom_jobs_total{status=%q} %d\n", status, count)
		}

		// Cashu payment metrics
		fmt.Fprintln(w, "# HELP bahia_cashu_payments_total Total Cashu payments by status")
		fmt.Fprintln(w, "# TYPE bahia_cashu_payments_total counter")
		for status, count := range m.CashuPaymentsTotal {
			fmt.Fprintf(w, "bahia_cashu_payments_total{status=%q} %d\n", status, count)
		}

		fmt.Fprintln(w, "# HELP bahia_cashu_payments_sats_total Total sats paid via Cashu")
		fmt.Fprintln(w, "# TYPE bahia_cashu_payments_sats_total counter")
		fmt.Fprintf(w, "bahia_cashu_payments_sats_total %d\n", m.CashuPaymentsSats)

		fmt.Fprintln(w, "# HELP bahia_cashu_wallet_balance_sats Current wallet balance in sats by mint")
		fmt.Fprintln(w, "# TYPE bahia_cashu_wallet_balance_sats gauge")
		for mint, balance := range m.CashuWalletBalance {
			fmt.Fprintf(w, "bahia_cashu_wallet_balance_sats{mint=%q} %d\n", mint, balance)
		}
	}
}

// calculatePercentiles returns p50, p90, and p99 for a slice of float64 values.
func calculatePercentiles(values []float64) (p50, p90, p99 float64) {
	if len(values) == 0 {
		return 0, 0, 0
	}

	// Copy and sort
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	n := len(sorted)
	p50 = sorted[n*50/100]
	p90 = sorted[n*90/100]
	
	p99Idx := n * 99 / 100
	if p99Idx >= n {
		p99Idx = n - 1
	}
	p99 = sorted[p99Idx]

	return p50, p90, p99
}
