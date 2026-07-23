// Package telemetry provides OpenTelemetry integration for tracing and metrics.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Config holds telemetry configuration.
type Config struct {
	Enabled        bool
	ServiceName    string
	ServiceVersion string
	OTLPEndpoint   string // e.g. "localhost:4317" for gRPC, "localhost:4318" for HTTP
	OTLPProtocol   string // "grpc" or "http", defaults to "grpc"
	Environment    string // e.g. "production", "staging"
}

// Provider manages telemetry lifecycle.
type Provider struct {
	config         Config
	logger         *zap.Logger
	metrics        *Metrics
	tracerProvider *sdktrace.TracerProvider
	meterProvider  *sdkmetric.MeterProvider
	setupErr       error
	shutdownOnce   sync.Once
	shutdownErr    error
}

// Metrics collects application-level counters and gauges.
type Metrics struct {
	mu sync.RWMutex

	// HTTP metrics
	HTTPRequestsTotal      map[string]int64 // key: method:path:status
	HTTPRequestDurations   []float64        // in seconds
	HTTPRequestDurationSum float64          // sum for average calculation

	// Deployment metrics
	DeploymentsTotal   map[string]int64 // key: service:env:status
	DriftDetectedTotal int64

	// Fleet hygiene metrics (fp-jan / Swabbie)
	HygieneScansTotal            int64
	HygieneCandidatesTotal       map[string]int64 // key: class
	HygieneActionsTotal          map[string]int64 // key: method:status
	HygienePressureBreachesTotal int64

	// Fleet health metrics (fp-obs / Bahia WS6)
	FleetHealthEntities map[string]int64 // key: domain:status

	// Adoption/direct-runtime operational metrics
	AdoptionScansTotal          map[string]int64 // key: status
	AdoptionScanDurations       []float64        // in seconds
	AdoptionTargetsScannedTotal int64
	AdoptionCandidatesTotal     int64
	AdoptionRedactedKeysTotal   int64
	AdoptionImportsTotal        map[string]int64 // key: status
	AdoptionImportDurations     []float64        // in seconds
	AdoptionImportSuccessTotal  int64
	AdoptionImportFailureTotal  int64
	RuntimeActionsTotal         map[string]int64 // key: action:status
	RuntimeActionDurations      []float64        // in seconds

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
	NostrRelayHealthy     map[string]bool    // key: relay_url - is healthy
	NostrRelayDegraded    map[string]bool    // key: relay_url - is degraded
	NostrRelaySuccessRate map[string]float64 // key: relay_url - success rate (0-1)

	// Worker metrics
	WorkersActive    int64
	WorkersTotal     int64
	LoomJobsInflight int64
	LoomJobsTotal    map[string]int64 // key: status (completed, failed, cancelled)

	// Cashu payment metrics
	CashuPaymentsTotal map[string]int64 // key: status (sent, redeemed, failed)
	CashuPaymentsSats  int64            // total sats paid
	CashuWalletBalance map[string]int64 // key: mint_url
}

// NewMetrics creates a new metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		HTTPRequestsTotal:      make(map[string]int64),
		DeploymentsTotal:       make(map[string]int64),
		AdoptionScansTotal:     make(map[string]int64),
		AdoptionImportsTotal:   make(map[string]int64),
		RuntimeActionsTotal:    make(map[string]int64),
		NostrEventsPublished:   make(map[string]int64),
		NostrEventsReceived:    make(map[string]int64),
		NostrPublishOK:         make(map[string]int64),
		NostrPublishFailed:     make(map[string]int64),
		NostrReconnects:        make(map[string]int64),
		NostrRelayHealthy:      make(map[string]bool),
		NostrRelayDegraded:     make(map[string]bool),
		NostrRelaySuccessRate:  make(map[string]float64),
		LoomJobsTotal:          make(map[string]int64),
		CashuPaymentsTotal:     make(map[string]int64),
		CashuWalletBalance:     make(map[string]int64),
		HygieneCandidatesTotal: make(map[string]int64),
		HygieneActionsTotal:    make(map[string]int64),
		FleetHealthEntities:    make(map[string]int64),
	}
}

// Setup initializes telemetry with the given configuration.
// Returns a Provider that can be used to record metrics and shut down cleanly.
func Setup(cfg Config, logger *zap.Logger) *Provider {
	if logger == nil {
		logger = zap.NewNop()
	}
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

	exportMode := "prometheus"
	if strings.TrimSpace(cfg.OTLPEndpoint) != "" {
		tracerProvider, meterProvider, err := configureOTLP(context.Background(), cfg, serviceName)
		if err != nil {
			p.setupErr = fmt.Errorf("configuring OTLP telemetry: %w", err)
			logger.Error("telemetry OTLP initialization failed", zap.Error(p.setupErr))
		} else {
			p.tracerProvider = tracerProvider
			p.meterProvider = meterProvider
			otel.SetTracerProvider(tracerProvider)
			otel.SetMeterProvider(meterProvider)
			exportMode = "prometheus+otlp"
		}
	}

	logger.Info("telemetry initialized",
		zap.String("service", serviceName),
		zap.String("version", cfg.ServiceVersion),
		zap.String("environment", cfg.Environment),
		zap.String("export_mode", exportMode),
	)

	return p
}

// GetMetrics returns the metrics collector.
func (p *Provider) GetMetrics() *Metrics {
	return p.metrics
}

// TracerProvider returns the configured OTLP tracer provider, or nil when OTLP is disabled or failed to initialize.
func (p *Provider) TracerProvider() trace.TracerProvider {
	if p.tracerProvider == nil {
		return nil
	}
	return p.tracerProvider
}

// MeterProvider returns the configured OTLP meter provider, or nil when OTLP is disabled or failed to initialize.
func (p *Provider) MeterProvider() metric.MeterProvider {
	if p.meterProvider == nil {
		return nil
	}
	return p.meterProvider
}

// Err reports any exporter initialization failure retained by Setup.
func (p *Provider) Err() error {
	return p.setupErr
}

// Shutdown flushes and shuts down all configured telemetry exporters exactly once.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		var shutdownErrors []error
		if p.meterProvider != nil {
			if err := p.meterProvider.Shutdown(ctx); err != nil {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("shutting down OTLP metrics: %w", err))
			}
		}
		if p.tracerProvider != nil {
			if err := p.tracerProvider.Shutdown(ctx); err != nil {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("shutting down OTLP traces: %w", err))
			}
		}
		if p.setupErr != nil {
			shutdownErrors = append(shutdownErrors, p.setupErr)
		}
		p.shutdownErr = errors.Join(shutdownErrors...)
		p.logger.Info("telemetry shutdown complete", zap.Error(p.shutdownErr))
	})
	return p.shutdownErr
}

func configureOTLP(ctx context.Context, cfg Config, serviceName string) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider, error) {
	res := resource.NewSchemaless(
		attribute.String("service.name", serviceName),
		attribute.String("service.version", cfg.ServiceVersion),
		attribute.String("deployment.environment.name", cfg.Environment),
	)

	protocol := strings.ToLower(strings.TrimSpace(cfg.OTLPProtocol))
	if protocol == "" {
		protocol = "grpc"
	}

	var (
		traceExporter  sdktrace.SpanExporter
		metricExporter sdkmetric.Exporter
		err            error
	)
	switch protocol {
	case "grpc":
		endpoint, insecure := normalizeGRPCEndpoint(cfg.OTLPEndpoint)
		traceOptions := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		metricOptions := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint)}
		if insecure {
			traceOptions = append(traceOptions, otlptracegrpc.WithInsecure())
			metricOptions = append(metricOptions, otlpmetricgrpc.WithInsecure())
		}
		traceExporter, err = otlptracegrpc.New(ctx, traceOptions...)
		if err == nil {
			metricExporter, err = otlpmetricgrpc.New(ctx, metricOptions...)
		}
	case "http":
		traceOptions, metricOptions := httpExporterOptions(cfg.OTLPEndpoint)
		traceExporter, err = otlptracehttp.New(ctx, traceOptions...)
		if err == nil {
			metricExporter, err = otlpmetrichttp.New(ctx, metricOptions...)
		}
	default:
		return nil, nil, fmt.Errorf("unsupported OTLP protocol %q", cfg.OTLPProtocol)
	}
	if err != nil {
		if traceExporter != nil {
			_ = traceExporter.Shutdown(ctx)
		}
		return nil, nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter), sdktrace.WithResource(res))
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	return tracerProvider, meterProvider, nil
}

func normalizeGRPCEndpoint(endpoint string) (string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case strings.HasPrefix(endpoint, "http://"):
		return strings.TrimPrefix(endpoint, "http://"), true
	case strings.HasPrefix(endpoint, "https://"):
		return strings.TrimPrefix(endpoint, "https://"), false
	default:
		return endpoint, true
	}
}

func httpExporterOptions(endpoint string) ([]otlptracehttp.Option, []otlpmetrichttp.Option) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint + "/v1/traces")},
			[]otlpmetrichttp.Option{otlpmetrichttp.WithEndpointURL(endpoint + "/v1/metrics")}
	}
	return []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure()},
		[]otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(endpoint), otlpmetrichttp.WithInsecure()}
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

// RecordHygieneScan counts a hygiene dry-run scan issued to a worker.
func (m *Metrics) RecordHygieneScan() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HygieneScansTotal++
}

// RecordHygieneCandidates counts scan candidates by class.
func (m *Metrics) RecordHygieneCandidates(class string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HygieneCandidatesTotal[class] += int64(count)
}

// RecordHygieneAction counts a maintenance intent by method and status.
func (m *Metrics) RecordHygieneAction(method, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HygieneActionsTotal[method+":"+status]++
}

// RecordHygienePressureBreach counts a pressure-threshold breach.
func (m *Metrics) RecordHygienePressureBreach() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.HygienePressureBreachesTotal++
}

var fleetHealthDomains = []string{"worker", "service", "runtime"}
var fleetHealthStatuses = []string{"healthy", "degraded", "unhealthy", "unknown"}

func normalizeFleetHealthDomain(domain string) string {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "worker", "service", "runtime":
		return strings.ToLower(strings.TrimSpace(domain))
	default:
		return "unknown"
	}
}

func normalizeFleetHealthStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "healthy", "degraded", "unhealthy", "unknown":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "unknown"
	}
}

// SetFleetHealthEntities sets a bounded fleet-health gauge.
func (m *Metrics) SetFleetHealthEntities(domain, status string, count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if count < 0 {
		count = 0
	}
	domain = normalizeFleetHealthDomain(domain)
	if domain == "unknown" {
		return
	}
	status = normalizeFleetHealthStatus(status)
	m.FleetHealthEntities[domain+":"+status] = count
}

// RecordAdoptionScan records an adoption scan operation. It stores only
// aggregate counts and never stores environment values, labels, or host secrets.
func (m *Metrics) RecordAdoptionScan(targets, candidates, redactedKeys int, duration time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := "success"
	if !success {
		status = "failed"
	}
	m.AdoptionScansTotal[status]++
	m.AdoptionTargetsScannedTotal += int64(targets)
	m.AdoptionCandidatesTotal += int64(candidates)
	m.AdoptionRedactedKeysTotal += int64(redactedKeys)
	m.AdoptionScanDurations = appendBounded(m.AdoptionScanDurations, duration.Seconds(), 1000)
}

// RecordAdoptionImport records an adoption import batch. It stores aggregate
// result counts and redaction counts only, not discovered values.
func (m *Metrics) RecordAdoptionImport(candidates, successCount, failureCount, redactedKeys int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	status := "success"
	if failureCount > 0 {
		status = "partial_failure"
	}
	if successCount == 0 && failureCount > 0 {
		status = "failed"
	}
	m.AdoptionImportsTotal[status]++
	m.AdoptionCandidatesTotal += int64(candidates)
	m.AdoptionImportSuccessTotal += int64(successCount)
	m.AdoptionImportFailureTotal += int64(failureCount)
	m.AdoptionRedactedKeysTotal += int64(redactedKeys)
	m.AdoptionImportDurations = appendBounded(m.AdoptionImportDurations, duration.Seconds(), 1000)
}

// RecordRuntimeAction records direct runtime action latency and outcome.
func (m *Metrics) RecordRuntimeAction(action, status string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if action == "" {
		action = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	key := fmt.Sprintf("%s:%s", action, status)
	m.RuntimeActionsTotal[key]++
	m.RuntimeActionDurations = appendBounded(m.RuntimeActionDurations, duration.Seconds(), 1000)
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

		fmt.Fprintln(w, "# HELP bahia_hygiene_scans_total Total hygiene dry-run scans issued")
		fmt.Fprintln(w, "# TYPE bahia_hygiene_scans_total counter")
		fmt.Fprintf(w, "bahia_hygiene_scans_total %d\n", m.HygieneScansTotal)
		fmt.Fprintln(w, "# HELP bahia_hygiene_candidates_total Hygiene scan candidates by class")
		fmt.Fprintln(w, "# TYPE bahia_hygiene_candidates_total counter")
		for class, count := range m.HygieneCandidatesTotal {
			fmt.Fprintf(w, "bahia_hygiene_candidates_total{class=%q} %d\n", class, count)
		}
		fmt.Fprintln(w, "# HELP bahia_hygiene_actions_total Hygiene maintenance intents by method and status")
		fmt.Fprintln(w, "# TYPE bahia_hygiene_actions_total counter")
		for key, count := range m.HygieneActionsTotal {
			parts := strings.SplitN(key, ":", 2)
			status := ""
			if len(parts) == 2 {
				status = parts[1]
			}
			fmt.Fprintf(w, "bahia_hygiene_actions_total{method=%q,status=%q} %d\n", parts[0], status, count)
		}
		fmt.Fprintln(w, "# HELP bahia_hygiene_pressure_breaches_total Pressure threshold breaches (disk>85%% / inode)")
		fmt.Fprintln(w, "# TYPE bahia_hygiene_pressure_breaches_total counter")
		fmt.Fprintf(w, "bahia_hygiene_pressure_breaches_total %d\n", m.HygienePressureBreachesTotal)

		// Adoption/direct-runtime operational metrics
		fmt.Fprintln(w, "# HELP bahia_adoption_scans_total Adoption scan requests by status")
		fmt.Fprintln(w, "# TYPE bahia_adoption_scans_total counter")
		for status, count := range m.AdoptionScansTotal {
			fmt.Fprintf(w, "bahia_adoption_scans_total{status=%q} %d\n", status, count)
		}

		fmt.Fprintln(w, "# HELP bahia_adoption_targets_scanned_total Adoption runtime targets scanned")
		fmt.Fprintln(w, "# TYPE bahia_adoption_targets_scanned_total counter")
		fmt.Fprintf(w, "bahia_adoption_targets_scanned_total %d\n", m.AdoptionTargetsScannedTotal)

		fmt.Fprintln(w, "# HELP bahia_adoption_candidates_total Adoption candidates observed or processed")
		fmt.Fprintln(w, "# TYPE bahia_adoption_candidates_total counter")
		fmt.Fprintf(w, "bahia_adoption_candidates_total %d\n", m.AdoptionCandidatesTotal)

		fmt.Fprintln(w, "# HELP bahia_adoption_redacted_keys_total Sensitive adoption env/label keys redacted or extracted")
		fmt.Fprintln(w, "# TYPE bahia_adoption_redacted_keys_total counter")
		fmt.Fprintf(w, "bahia_adoption_redacted_keys_total %d\n", m.AdoptionRedactedKeysTotal)

		if len(m.AdoptionScanDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_adoption_scan_duration_seconds Adoption scan duration in seconds")
			fmt.Fprintln(w, "# TYPE bahia_adoption_scan_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.AdoptionScanDurations)
			fmt.Fprintf(w, "bahia_adoption_scan_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_adoption_scan_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_adoption_scan_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_adoption_scan_duration_seconds_count %d\n", len(m.AdoptionScanDurations))
		}

		fmt.Fprintln(w, "# HELP bahia_adoption_imports_total Adoption import batches by status")
		fmt.Fprintln(w, "# TYPE bahia_adoption_imports_total counter")
		for status, count := range m.AdoptionImportsTotal {
			fmt.Fprintf(w, "bahia_adoption_imports_total{status=%q} %d\n", status, count)
		}

		fmt.Fprintln(w, "# HELP bahia_adoption_import_success_total Adoption import candidates that succeeded")
		fmt.Fprintln(w, "# TYPE bahia_adoption_import_success_total counter")
		fmt.Fprintf(w, "bahia_adoption_import_success_total %d\n", m.AdoptionImportSuccessTotal)

		fmt.Fprintln(w, "# HELP bahia_adoption_import_failure_total Adoption import candidates that failed")
		fmt.Fprintln(w, "# TYPE bahia_adoption_import_failure_total counter")
		fmt.Fprintf(w, "bahia_adoption_import_failure_total %d\n", m.AdoptionImportFailureTotal)

		if len(m.AdoptionImportDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_adoption_import_duration_seconds Adoption import batch duration in seconds")
			fmt.Fprintln(w, "# TYPE bahia_adoption_import_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.AdoptionImportDurations)
			fmt.Fprintf(w, "bahia_adoption_import_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_adoption_import_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_adoption_import_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_adoption_import_duration_seconds_count %d\n", len(m.AdoptionImportDurations))
		}

		fmt.Fprintln(w, "# HELP bahia_runtime_actions_total Direct runtime actions by action and status")
		fmt.Fprintln(w, "# TYPE bahia_runtime_actions_total counter")
		for key, count := range m.RuntimeActionsTotal {
			fmt.Fprintf(w, "bahia_runtime_actions_total{key=%q} %d\n", key, count)
		}

		if len(m.RuntimeActionDurations) > 0 {
			fmt.Fprintln(w, "# HELP bahia_runtime_action_duration_seconds Direct runtime action duration in seconds")
			fmt.Fprintln(w, "# TYPE bahia_runtime_action_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.RuntimeActionDurations)
			fmt.Fprintf(w, "bahia_runtime_action_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			fmt.Fprintf(w, "bahia_runtime_action_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			fmt.Fprintf(w, "bahia_runtime_action_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			fmt.Fprintf(w, "bahia_runtime_action_duration_seconds_count %d\n", len(m.RuntimeActionDurations))
		}

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

		// Fleet health metrics
		fmt.Fprintln(w, "# HELP bahia_fleet_health_entities Fleet health entity counts by bounded domain and health status")
		fmt.Fprintln(w, "# TYPE bahia_fleet_health_entities gauge")
		for _, domain := range fleetHealthDomains {
			for _, status := range fleetHealthStatuses {
				key := domain + ":" + status
				fmt.Fprintf(w, "bahia_fleet_health_entities{domain=%q,status=%q} %d\n", domain, status, m.FleetHealthEntities[key])
			}
		}

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

func appendBounded(values []float64, value float64, max int) []float64 {
	values = append(values, value)
	if max > 0 && len(values) > max {
		return values[len(values)-max:]
	}
	return values
}
