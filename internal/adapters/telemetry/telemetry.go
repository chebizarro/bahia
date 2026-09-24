// Package telemetry provides OpenTelemetry integration for tracing and metrics.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlploggrpc "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otlploghttp "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otlpmetricgrpc "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	otlpmetrichttp "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otlptracegrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otlptracehttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	sdklog "go.opentelemetry.io/otel/sdk/log"
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
	openClawSagaMu     sync.RWMutex
	openClawSaga       func(context.Context, io.Writer) error
	config             Config
	logger             *zap.Logger
	metrics            *Metrics
	fleetHealthWorkers fleetHealthWorkerSource
	fleetHealthStates  fleetHealthStateSource
	now                func() time.Time
	nostrFleetHealth   *nostrFleetHealthProjector
	tracerProvider     *sdktrace.TracerProvider
	meterProvider      *sdkmetric.MeterProvider
	loggerProvider     *sdklog.LoggerProvider
	setupErr           error
	shutdownOnce       sync.Once
	shutdownErr        error
}

var activeMetrics atomic.Pointer[Metrics]

// Metrics collects application-level counters and gauges.
type Metrics struct {
	mu             sync.RWMutex
	virtualization map[virtualizationMetricKey]virtualizationMetricValue

	// HTTP metrics
	HTTPRequestsTotal        map[string]int64 // key: method:path:status
	HTTPRequestDurations     []float64        // in seconds
	HTTPRequestDurationSum   float64          // sum for average calculation
	HTTPRequestDurationCount int64

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
	ControlPlaneDispatches map[string]int64 // key: kind:outcome
	ReleaseOutcomes        map[string]int64 // key: operation:outcome

	// Nostr metrics
	NostrEventsPublished    map[string]int64 // key: kind
	NostrEventsReceived     map[string]int64
	Audit4903AnomaliesTotal int64
	AuthorizationRejections map[string]int64 // key: bounded reason
	TierRejections          map[string]int64 // key: requested tier

	// Nostr protocol metrics (NIP-01 frames)
	NostrEOSELatencies    []float64        // EOSE latency in seconds
	NostrPublishOK        map[string]int64 // key: relay_url - successful publishes
	NostrPublishFailed    map[string]int64 // key: relay_url:reason - failed publishes
	NostrPublishLatencies []float64        // publish latency in seconds
	NostrReconnects       map[string]int64 // key: relay_url - reconnection count
	NostrBackoffDurations []float64        // backoff durations in seconds

	// Relay health metrics
	NostrRelayHealthy           map[string]bool             // key: relay_url - is healthy
	NostrRelayDegraded          map[string]bool             // key: relay_url - is degraded
	NostrRelaySuccessRate       map[string]float64          // key: relay_url - success rate (0-1)
	NostrRelayClosedReasons     map[string]map[string]int64 // key: relay_url, then bounded CLOSED reason
	NostrRelayReREQAttempts     map[string]int64            // key: relay_url
	NostrRelayReconnectAttempts map[string]int64            // key: relay_url
	NostrOutboxDepth            int64
	NostrEventStoreTotalBytes   int64
	NostrEventStoreHeapBytes    int64
	NostrEventStoreIndexBytes   int64
	NostrEventStoreLiveRows     int64
	NostrEventStoreDeadRows     int64
	NostrEventStoreOldestUnix   int64
	NostrArchiveBatches         map[string]int64 // key: claimed, exported, protected, pruned

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
		virtualization:              make(map[virtualizationMetricKey]virtualizationMetricValue),
		HTTPRequestsTotal:           make(map[string]int64),
		DeploymentsTotal:            make(map[string]int64),
		AdoptionScansTotal:          make(map[string]int64),
		AdoptionImportsTotal:        make(map[string]int64),
		RuntimeActionsTotal:         make(map[string]int64),
		NostrEventsPublished:        make(map[string]int64),
		NostrEventsReceived:         make(map[string]int64),
		AuthorizationRejections:     make(map[string]int64),
		TierRejections:              make(map[string]int64),
		NostrPublishOK:              make(map[string]int64),
		NostrPublishFailed:          make(map[string]int64),
		NostrReconnects:             make(map[string]int64),
		NostrRelayHealthy:           make(map[string]bool),
		NostrRelayDegraded:          make(map[string]bool),
		NostrRelaySuccessRate:       make(map[string]float64),
		NostrRelayClosedReasons:     make(map[string]map[string]int64),
		NostrRelayReREQAttempts:     make(map[string]int64),
		NostrRelayReconnectAttempts: make(map[string]int64),
		NostrArchiveBatches:         make(map[string]int64),
		LoomJobsTotal:               make(map[string]int64),
		CashuPaymentsTotal:          make(map[string]int64),
		CashuWalletBalance:          make(map[string]int64),
		HygieneCandidatesTotal:      make(map[string]int64),
		HygieneActionsTotal:         make(map[string]int64),
		FleetHealthEntities:         make(map[string]int64),
		ControlPlaneDispatches:      make(map[string]int64),
		ReleaseOutcomes:             make(map[string]int64),
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
		now:     time.Now,
	}
	activeMetrics.Store(p.metrics)
	p.nostrFleetHealth = newNostrFleetHealthProjector(func() time.Time { return p.now() })

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
		tracerProvider, meterProvider, loggerProvider, err := configureOTLP(context.Background(), cfg, serviceName)
		if err != nil {
			p.setupErr = fmt.Errorf("configuring OTLP telemetry: %w", err)
			logger.Error("telemetry OTLP initialization failed", zap.Error(p.setupErr))
		} else {
			p.tracerProvider = tracerProvider
			p.meterProvider = meterProvider
			p.loggerProvider = loggerProvider
			otel.SetTracerProvider(tracerProvider)
			otel.SetMeterProvider(meterProvider)
			global.SetLoggerProvider(loggerProvider)
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

// LoggerProvider returns the configured OTLP logger provider, or nil when OTLP is disabled or failed to initialize.
func (p *Provider) LoggerProvider() otellog.LoggerProvider {
	if p.loggerProvider == nil {
		return nil
	}
	return p.loggerProvider
}

// Err reports any exporter initialization failure retained by Setup.
func (p *Provider) Err() error {
	return p.setupErr
}

// Shutdown flushes and shuts down all configured telemetry exporters exactly once.
func (p *Provider) Shutdown(ctx context.Context) error {
	p.shutdownOnce.Do(func() {
		var shutdownErrors []error
		if p.loggerProvider != nil {
			if err := p.loggerProvider.Shutdown(ctx); err != nil {
				shutdownErrors = append(shutdownErrors, fmt.Errorf("shutting down OTLP logs: %w", err))
			}
		}
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

func configureOTLP(ctx context.Context, cfg Config, serviceName string) (*sdktrace.TracerProvider, *sdkmetric.MeterProvider, *sdklog.LoggerProvider, error) {
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
		logExporter    sdklog.Exporter
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
		if err == nil {
			logOptions := []otlploggrpc.Option{otlploggrpc.WithEndpoint(endpoint)}
			if insecure {
				logOptions = append(logOptions, otlploggrpc.WithInsecure())
			}
			logExporter, err = otlploggrpc.New(ctx, logOptions...)
		}
	case "http":
		traceOptions, metricOptions := httpExporterOptions(cfg.OTLPEndpoint)
		traceExporter, err = otlptracehttp.New(ctx, traceOptions...)
		if err == nil {
			metricExporter, err = otlpmetrichttp.New(ctx, metricOptions...)
		}
		if err == nil {
			logExporter, err = otlploghttp.New(ctx, httpLogExporterOptions(cfg.OTLPEndpoint)...)
		}
	default:
		return nil, nil, nil, fmt.Errorf("unsupported OTLP protocol %q", cfg.OTLPProtocol)
	}
	if err != nil {
		if traceExporter != nil {
			_ = traceExporter.Shutdown(ctx)
		}
		if metricExporter != nil {
			_ = metricExporter.Shutdown(ctx)
		}
		if logExporter != nil {
			_ = logExporter.Shutdown(ctx)
		}
		return nil, nil, nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExporter), sdktrace.WithResource(res))
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	return tracerProvider, meterProvider, loggerProvider, nil
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

func httpLogExporterOptions(endpoint string) []otlploghttp.Option {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return []otlploghttp.Option{otlploghttp.WithEndpointURL(endpoint + "/v1/logs")}
	}
	return []otlploghttp.Option{otlploghttp.WithEndpoint(endpoint), otlploghttp.WithInsecure()}
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
	m.HTTPRequestDurationCount++

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

// RecordControlPlaneDispatch records a bounded outbound ContextVM or Loom dispatch outcome.
func (m *Metrics) RecordControlPlaneDispatch(kind, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ControlPlaneDispatches[kind+":"+outcome]++
}

// RecordReleaseOutcome records a bounded promotion or rollback outcome.
func (m *Metrics) RecordReleaseOutcome(operation, outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ReleaseOutcomes[operation+":"+outcome]++
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

// RecordAudit4903Anomaly records a rejected or contradictory audit event.
func (m *Metrics) RecordAudit4903Anomaly() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Audit4903AnomaliesTotal++
}

// RecordAuthorizationRejection records a rejection using a bounded reason.
func (m *Metrics) RecordAuthorizationRejection(reason string) {
	switch reason {
	case "policy", "identity", "replay", "signature":
	default:
		reason = "other"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AuthorizationRejections[reason]++
}

// RecordTierRejection records a request rejected by Bahia's active tier.
func (m *Metrics) RecordTierRejection(requestedTier int) {
	tier := fmt.Sprintf("%d", requestedTier)
	if requestedTier < 0 || requestedTier > 3 {
		tier = "other"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TierRejections[tier]++
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

// SetNostrRelayTransportHealth updates relay protocol recovery counters from a health snapshot.
func (m *Metrics) SetNostrRelayTransportHealth(relayURL string, closedReasons map[string]int64, reREQAttempts, reconnectAttempts int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copiedReasons := make(map[string]int64, len(closedReasons))
	for reason, count := range closedReasons {
		copiedReasons[reason] = count
	}
	m.NostrRelayClosedReasons[relayURL] = copiedReasons
	m.NostrRelayReREQAttempts[relayURL] = reREQAttempts
	m.NostrRelayReconnectAttempts[relayURL] = reconnectAttempts
}

// SetNostrOutboxDepth updates the unpublished Nostr event outbox gauge.
func (m *Metrics) SetNostrOutboxDepth(depth int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if depth < 0 {
		depth = 0
	}
	m.NostrOutboxDepth = depth
}

// SetNostrEventStorage records catalog-backed event-store lifecycle gauges.
func (m *Metrics) SetNostrEventStorage(totalBytes, heapBytes, indexBytes, liveRows, deadRows, oldestUnix int64, batches map[string]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NostrEventStoreTotalBytes = totalBytes
	m.NostrEventStoreHeapBytes = heapBytes
	m.NostrEventStoreIndexBytes = indexBytes
	m.NostrEventStoreLiveRows = liveRows
	m.NostrEventStoreDeadRows = deadRows
	m.NostrEventStoreOldestUnix = oldestUnix
	for _, status := range []string{"claimed", "exported", "protected", "pruned"} {
		m.NostrArchiveBatches[status] = batches[status]
	}
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

type prometheusWriter struct {
	writer io.Writer
	err    error
}

func (w *prometheusWriter) println(args ...interface{}) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.writer, args...)
}

func (w *prometheusWriter) printf(format string, args ...interface{}) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.writer, format, args...)
}

// MetricsHandler returns an HTTP handler that serves Prometheus-compatible metrics.
func (p *Provider) MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fleetHealth := p.fleetHealthSnapshot(r.Context(), p.now().UTC())
		nostrFleetHealth := p.nostrFleetHealth.snapshot(p.now().UTC())
		m := p.metrics
		m.mu.RLock()
		defer m.mu.RUnlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		if err := m.renderVirtualization(w); err != nil {
			// The scrape transport failed; another write cannot repair the partial response.
			return
		}

		writer := &prometheusWriter{writer: w}

		// HTTP metrics
		writer.println("# HELP bahia_http_requests_total Total HTTP requests by method, path, and status code")
		writer.println("# TYPE bahia_http_requests_total counter")
		for key, count := range m.HTTPRequestsTotal {
			writer.printf("bahia_http_requests_total{key=%q} %d\n", key, count)
		}

		if len(m.HTTPRequestDurations) > 0 {
			writer.println("# HELP bahia_http_request_duration_seconds HTTP request duration in seconds")
			writer.println("# TYPE bahia_http_request_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.HTTPRequestDurations)
			writer.printf("bahia_http_request_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_http_request_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_http_request_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_http_request_duration_seconds_sum %.6f\n", m.HTTPRequestDurationSum)
			writer.printf("bahia_http_request_duration_seconds_count %d\n", m.HTTPRequestDurationCount)
		}

		// Deployment metrics
		writer.println("# HELP bahia_deployments_total Total deployments by service, environment, and status")
		writer.println("# TYPE bahia_deployments_total counter")
		for key, count := range m.DeploymentsTotal {
			writer.printf("bahia_deployments_total{key=%q} %d\n", key, count)
		}

		writer.println("# HELP bahia_drift_detected_total Total drift detections")
		writer.println("# TYPE bahia_drift_detected_total counter")
		writer.printf("bahia_drift_detected_total %d\n", m.DriftDetectedTotal)

		writer.println("# HELP bahia_hygiene_scans_total Total hygiene dry-run scans issued")
		writer.println("# TYPE bahia_hygiene_scans_total counter")
		writer.printf("bahia_hygiene_scans_total %d\n", m.HygieneScansTotal)
		writer.println("# HELP bahia_hygiene_candidates_total Hygiene scan candidates by class")
		writer.println("# TYPE bahia_hygiene_candidates_total counter")
		for class, count := range m.HygieneCandidatesTotal {
			writer.printf("bahia_hygiene_candidates_total{class=%q} %d\n", class, count)
		}
		writer.println("# HELP bahia_hygiene_actions_total Hygiene maintenance intents by method and status")
		writer.println("# TYPE bahia_hygiene_actions_total counter")
		for key, count := range m.HygieneActionsTotal {
			parts := strings.SplitN(key, ":", 2)
			status := ""
			if len(parts) == 2 {
				status = parts[1]
			}
			writer.printf("bahia_hygiene_actions_total{method=%q,status=%q} %d\n", parts[0], status, count)
		}
		writer.println("# HELP bahia_hygiene_pressure_breaches_total Pressure threshold breaches (disk>85%% / inode)")
		writer.println("# TYPE bahia_hygiene_pressure_breaches_total counter")
		writer.printf("bahia_hygiene_pressure_breaches_total %d\n", m.HygienePressureBreachesTotal)

		// Adoption/direct-runtime operational metrics
		writer.println("# HELP bahia_adoption_scans_total Adoption scan requests by status")
		writer.println("# TYPE bahia_adoption_scans_total counter")
		for status, count := range m.AdoptionScansTotal {
			writer.printf("bahia_adoption_scans_total{status=%q} %d\n", status, count)
		}

		writer.println("# HELP bahia_adoption_targets_scanned_total Adoption runtime targets scanned")
		writer.println("# TYPE bahia_adoption_targets_scanned_total counter")
		writer.printf("bahia_adoption_targets_scanned_total %d\n", m.AdoptionTargetsScannedTotal)

		writer.println("# HELP bahia_adoption_candidates_total Adoption candidates observed or processed")
		writer.println("# TYPE bahia_adoption_candidates_total counter")
		writer.printf("bahia_adoption_candidates_total %d\n", m.AdoptionCandidatesTotal)

		writer.println("# HELP bahia_adoption_redacted_keys_total Sensitive adoption env/label keys redacted or extracted")
		writer.println("# TYPE bahia_adoption_redacted_keys_total counter")
		writer.printf("bahia_adoption_redacted_keys_total %d\n", m.AdoptionRedactedKeysTotal)

		if len(m.AdoptionScanDurations) > 0 {
			writer.println("# HELP bahia_adoption_scan_duration_seconds Adoption scan duration in seconds")
			writer.println("# TYPE bahia_adoption_scan_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.AdoptionScanDurations)
			writer.printf("bahia_adoption_scan_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_adoption_scan_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_adoption_scan_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_adoption_scan_duration_seconds_count %d\n", len(m.AdoptionScanDurations))
		}

		writer.println("# HELP bahia_adoption_imports_total Adoption import batches by status")
		writer.println("# TYPE bahia_adoption_imports_total counter")
		for status, count := range m.AdoptionImportsTotal {
			writer.printf("bahia_adoption_imports_total{status=%q} %d\n", status, count)
		}

		writer.println("# HELP bahia_adoption_import_success_total Adoption import candidates that succeeded")
		writer.println("# TYPE bahia_adoption_import_success_total counter")
		writer.printf("bahia_adoption_import_success_total %d\n", m.AdoptionImportSuccessTotal)

		writer.println("# HELP bahia_adoption_import_failure_total Adoption import candidates that failed")
		writer.println("# TYPE bahia_adoption_import_failure_total counter")
		writer.printf("bahia_adoption_import_failure_total %d\n", m.AdoptionImportFailureTotal)

		if len(m.AdoptionImportDurations) > 0 {
			writer.println("# HELP bahia_adoption_import_duration_seconds Adoption import batch duration in seconds")
			writer.println("# TYPE bahia_adoption_import_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.AdoptionImportDurations)
			writer.printf("bahia_adoption_import_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_adoption_import_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_adoption_import_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_adoption_import_duration_seconds_count %d\n", len(m.AdoptionImportDurations))
		}

		writer.println("# HELP bahia_runtime_actions_total Direct runtime actions by action and status")
		writer.println("# TYPE bahia_runtime_actions_total counter")
		for key, count := range m.RuntimeActionsTotal {
			writer.printf("bahia_runtime_actions_total{key=%q} %d\n", key, count)
		}

		if len(m.RuntimeActionDurations) > 0 {
			writer.println("# HELP bahia_runtime_action_duration_seconds Direct runtime action duration in seconds")
			writer.println("# TYPE bahia_runtime_action_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.RuntimeActionDurations)
			writer.printf("bahia_runtime_action_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_runtime_action_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_runtime_action_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_runtime_action_duration_seconds_count %d\n", len(m.RuntimeActionDurations))
		}

		// Reconciliation metrics
		writer.println("# HELP bahia_reconcile_total Total reconciliation cycles")
		writer.println("# TYPE bahia_reconcile_total counter")
		writer.printf("bahia_reconcile_total %d\n", m.ReconcileTotal)

		writer.println("# HELP bahia_reconcile_states_checked Number of states checked in last reconcile")
		writer.println("# TYPE bahia_reconcile_states_checked gauge")
		writer.printf("bahia_reconcile_states_checked %d\n", m.ReconcileStatesChecked)

		if len(m.ReconcileDurations) > 0 {
			writer.println("# HELP bahia_reconcile_duration_seconds Reconciliation cycle duration in seconds")
			writer.println("# TYPE bahia_reconcile_duration_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.ReconcileDurations)
			writer.printf("bahia_reconcile_duration_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_reconcile_duration_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_reconcile_duration_seconds{quantile=\"0.99\"} %.6f\n", p99)
		}

		writer.println("# HELP bahia_controlplane_dispatch_total ContextVM and Loom dispatch outcomes")
		writer.println("# TYPE bahia_controlplane_dispatch_total counter")
		for key, count := range m.ControlPlaneDispatches {
			parts := strings.SplitN(key, ":", 2)
			outcome := ""
			if len(parts) == 2 {
				outcome = parts[1]
			}
			writer.printf("bahia_controlplane_dispatch_total{kind=%q,outcome=%q} %d\n", parts[0], outcome, count)
		}

		writer.println("# HELP bahia_release_outcomes_total Promotion and rollback outcomes")
		writer.println("# TYPE bahia_release_outcomes_total counter")
		for key, count := range m.ReleaseOutcomes {
			parts := strings.SplitN(key, ":", 2)
			outcome := ""
			if len(parts) == 2 {
				outcome = parts[1]
			}
			writer.printf("bahia_release_outcomes_total{operation=%q,outcome=%q} %d\n", parts[0], outcome, count)
		}

		// Nostr metrics
		writer.println("# HELP bahia_nostr_events_published_total Nostr events published by kind")
		writer.println("# TYPE bahia_nostr_events_published_total counter")
		for kind, count := range m.NostrEventsPublished {
			writer.printf("bahia_nostr_events_published_total{kind=%q} %d\n", kind, count)
		}

		writer.println("# HELP bahia_nostr_events_received_total Nostr events received by kind")
		writer.println("# TYPE bahia_nostr_events_received_total counter")
		for kind, count := range m.NostrEventsReceived {
			writer.printf("bahia_nostr_events_received_total{kind=%q} %d\n", kind, count)
		}
		writer.println("# HELP bahia_audit_4903_anomalies_total Invalid or contradictory kind-4903 audit events")
		writer.println("# TYPE bahia_audit_4903_anomalies_total counter")
		writer.printf("bahia_audit_4903_anomalies_total %d\n", m.Audit4903AnomaliesTotal)
		writer.println("# HELP bahia_authorization_rejections_total Authorization rejections by bounded reason")
		writer.println("# TYPE bahia_authorization_rejections_total counter")
		for _, reason := range []string{"policy", "identity", "replay", "signature", "other"} {
			writer.printf("bahia_authorization_rejections_total{reason=%q} %d\n", reason, m.AuthorizationRejections[reason])
		}
		writer.println("# HELP bahia_tier_rejections_total Requests rejected because Bahia's active tier was insufficient")
		writer.println("# TYPE bahia_tier_rejections_total counter")
		for _, tier := range []string{"0", "1", "2", "3", "other"} {
			writer.printf("bahia_tier_rejections_total{tier=%q} %d\n", tier, m.TierRejections[tier])
		}

		// Nostr protocol metrics (EOSE, OK, reconnects)
		if len(m.NostrEOSELatencies) > 0 {
			writer.println("# HELP bahia_nostr_eose_latency_seconds Time from subscription start to EOSE receipt")
			writer.println("# TYPE bahia_nostr_eose_latency_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrEOSELatencies)
			writer.printf("bahia_nostr_eose_latency_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_nostr_eose_latency_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_nostr_eose_latency_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_nostr_eose_latency_seconds_count %d\n", len(m.NostrEOSELatencies))
		}

		writer.println("# HELP bahia_nostr_publish_ok_total Successful publishes by relay")
		writer.println("# TYPE bahia_nostr_publish_ok_total counter")
		for relay, count := range m.NostrPublishOK {
			writer.printf("bahia_nostr_publish_ok_total{relay=%q} %d\n", relay, count)
		}

		writer.println("# HELP bahia_nostr_publish_failed_total Failed publishes by relay and reason")
		writer.println("# TYPE bahia_nostr_publish_failed_total counter")
		for key, count := range m.NostrPublishFailed {
			writer.printf("bahia_nostr_publish_failed_total{key=%q} %d\n", key, count)
		}

		if len(m.NostrPublishLatencies) > 0 {
			writer.println("# HELP bahia_nostr_publish_latency_seconds Publish latency in seconds")
			writer.println("# TYPE bahia_nostr_publish_latency_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrPublishLatencies)
			writer.printf("bahia_nostr_publish_latency_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_nostr_publish_latency_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_nostr_publish_latency_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_nostr_publish_latency_seconds_count %d\n", len(m.NostrPublishLatencies))
		}

		writer.println("# HELP bahia_nostr_reconnects_total Relay reconnection attempts")
		writer.println("# TYPE bahia_nostr_reconnects_total counter")
		for relay, count := range m.NostrReconnects {
			writer.printf("bahia_nostr_reconnects_total{relay=%q} %d\n", relay, count)
		}

		if len(m.NostrBackoffDurations) > 0 {
			writer.println("# HELP bahia_nostr_backoff_seconds Backoff duration before reconnection")
			writer.println("# TYPE bahia_nostr_backoff_seconds summary")
			p50, p90, p99 := calculatePercentiles(m.NostrBackoffDurations)
			writer.printf("bahia_nostr_backoff_seconds{quantile=\"0.5\"} %.6f\n", p50)
			writer.printf("bahia_nostr_backoff_seconds{quantile=\"0.9\"} %.6f\n", p90)
			writer.printf("bahia_nostr_backoff_seconds{quantile=\"0.99\"} %.6f\n", p99)
			writer.printf("bahia_nostr_backoff_seconds_count %d\n", len(m.NostrBackoffDurations))
		}

		// Relay health metrics
		writer.println("# HELP bahia_nostr_relay_healthy Whether relay is healthy (1=yes, 0=no)")
		writer.println("# TYPE bahia_nostr_relay_healthy gauge")
		for relay, healthy := range m.NostrRelayHealthy {
			val := 0
			if healthy {
				val = 1
			}
			writer.printf("bahia_nostr_relay_healthy{relay=%q} %d\n", relay, val)
		}

		writer.println("# HELP bahia_nostr_relay_degraded Whether relay is degraded (1=yes, 0=no)")
		writer.println("# TYPE bahia_nostr_relay_degraded gauge")
		for relay, degraded := range m.NostrRelayDegraded {
			val := 0
			if degraded {
				val = 1
			}
			writer.printf("bahia_nostr_relay_degraded{relay=%q} %d\n", relay, val)
		}

		writer.println("# HELP bahia_nostr_relay_success_rate Relay publish success rate (0.0-1.0)")
		writer.println("# TYPE bahia_nostr_relay_success_rate gauge")
		for relay, rate := range m.NostrRelaySuccessRate {
			writer.printf("bahia_nostr_relay_success_rate{relay=%q} %.4f\n", relay, rate)
		}

		writer.println("# HELP bahia_nostr_relay_closed_total Relay CLOSED frames by relay and bounded reason")
		writer.println("# TYPE bahia_nostr_relay_closed_total counter")
		for relay, reasons := range m.NostrRelayClosedReasons {
			for reason, count := range reasons {
				writer.printf("bahia_nostr_relay_closed_total{relay=%q,reason=%q} %d\n", relay, reason, count)
			}
		}

		writer.println("# HELP bahia_nostr_relay_rereq_attempts_total Relay subscription recovery REQ attempts")
		writer.println("# TYPE bahia_nostr_relay_rereq_attempts_total counter")
		for relay, count := range m.NostrRelayReREQAttempts {
			writer.printf("bahia_nostr_relay_rereq_attempts_total{relay=%q} %d\n", relay, count)
		}

		writer.println("# HELP bahia_nostr_relay_reconnect_attempts_total Relay transport reconnect attempts")
		writer.println("# TYPE bahia_nostr_relay_reconnect_attempts_total counter")
		for relay, count := range m.NostrRelayReconnectAttempts {
			writer.printf("bahia_nostr_relay_reconnect_attempts_total{relay=%q} %d\n", relay, count)
		}

		writer.println("# HELP bahia_nostr_outbox_depth Unpublished events in the durable Nostr publish outbox")
		writer.println("# TYPE bahia_nostr_outbox_depth gauge")
		writer.printf("bahia_nostr_outbox_depth %d\n", m.NostrOutboxDepth)

		writer.println("# HELP bahia_nostr_event_store_bytes PostgreSQL Nostr event relation bytes by component")
		writer.println("# TYPE bahia_nostr_event_store_bytes gauge")
		writer.printf("bahia_nostr_event_store_bytes{component=\"total\"} %d\n", m.NostrEventStoreTotalBytes)
		writer.printf("bahia_nostr_event_store_bytes{component=\"heap\"} %d\n", m.NostrEventStoreHeapBytes)
		writer.printf("bahia_nostr_event_store_bytes{component=\"indexes\"} %d\n", m.NostrEventStoreIndexBytes)
		writer.println("# HELP bahia_nostr_event_store_rows Estimated PostgreSQL Nostr event rows by state")
		writer.println("# TYPE bahia_nostr_event_store_rows gauge")
		writer.printf("bahia_nostr_event_store_rows{state=\"live\"} %d\n", m.NostrEventStoreLiveRows)
		writer.printf("bahia_nostr_event_store_rows{state=\"dead\"} %d\n", m.NostrEventStoreDeadRows)
		writer.println("# HELP bahia_nostr_event_store_oldest_hot_timestamp_seconds Oldest eligible hot Nostr event Unix timestamp; zero until the online archive index exists")
		writer.println("# TYPE bahia_nostr_event_store_oldest_hot_timestamp_seconds gauge")
		writer.printf("bahia_nostr_event_store_oldest_hot_timestamp_seconds %d\n", m.NostrEventStoreOldestUnix)
		writer.println("# HELP bahia_nostr_archive_batches Archive batches by bounded lifecycle state")
		writer.println("# TYPE bahia_nostr_archive_batches gauge")
		for _, status := range []string{"claimed", "exported", "protected", "pruned"} {
			writer.printf("bahia_nostr_archive_batches{status=%q} %d\n", status, m.NostrArchiveBatches[status])
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
		writer.println("# HELP bahia_nostr_relays_healthy_total Count of healthy relays")
		writer.println("# TYPE bahia_nostr_relays_healthy_total gauge")
		writer.printf("bahia_nostr_relays_healthy_total %d\n", healthyCount)

		writer.println("# HELP bahia_nostr_relays_degraded_total Count of degraded relays")
		writer.println("# TYPE bahia_nostr_relays_degraded_total gauge")
		writer.printf("bahia_nostr_relays_degraded_total %d\n", degradedCount)

		writer.println("# HELP bahia_nostr_relays_unhealthy_total Count of unhealthy relays")
		writer.println("# TYPE bahia_nostr_relays_unhealthy_total gauge")
		writer.printf("bahia_nostr_relays_unhealthy_total %d\n", unhealthyCount)

		// Fleet health metrics
		writer.println("# HELP bahia_fleet_health_entities Fleet health entity counts by bounded domain and health status")
		writer.println("# TYPE bahia_fleet_health_entities gauge")
		for _, domain := range fleetHealthDomains {
			for _, status := range fleetHealthStatuses {
				key := domain + ":" + status
				writer.printf("bahia_fleet_health_entities{domain=%q,status=%q} %d\n", domain, status, m.FleetHealthEntities[key])
			}
		}

		// Worker metrics
		writer.println("# HELP bahia_workers_active Currently active (online) workers")
		writer.println("# TYPE bahia_workers_active gauge")
		writer.printf("bahia_workers_active %d\n", m.WorkersActive)

		writer.println("# HELP bahia_workers_total Total known workers")
		writer.println("# TYPE bahia_workers_total gauge")
		writer.printf("bahia_workers_total %d\n", m.WorkersTotal)

		writer.println("# HELP bahia_loom_jobs_inflight Currently in-flight Loom jobs")
		writer.println("# TYPE bahia_loom_jobs_inflight gauge")
		writer.printf("bahia_loom_jobs_inflight %d\n", m.LoomJobsInflight)

		writer.println("# HELP bahia_loom_jobs_total Total Loom jobs by status")
		writer.println("# TYPE bahia_loom_jobs_total counter")
		for status, count := range m.LoomJobsTotal {
			writer.printf("bahia_loom_jobs_total{status=%q} %d\n", status, count)
		}
		renderFleetHealthMetrics(writer, fleetHealth)
		renderNostrFleetHealthMetrics(writer, nostrFleetHealth)

		// Cashu payment metrics
		writer.println("# HELP bahia_cashu_payments_total Total Cashu payments by status")
		writer.println("# TYPE bahia_cashu_payments_total counter")
		for status, count := range m.CashuPaymentsTotal {
			writer.printf("bahia_cashu_payments_total{status=%q} %d\n", status, count)
		}

		writer.println("# HELP bahia_cashu_payments_sats_total Total sats paid via Cashu")
		writer.println("# TYPE bahia_cashu_payments_sats_total counter")
		writer.printf("bahia_cashu_payments_sats_total %d\n", m.CashuPaymentsSats)

		writer.println("# HELP bahia_cashu_wallet_balance_sats Current wallet balance in sats by mint")
		writer.println("# TYPE bahia_cashu_wallet_balance_sats gauge")
		for mint, balance := range m.CashuWalletBalance {
			writer.printf("bahia_cashu_wallet_balance_sats{mint=%q} %d\n", mint, balance)
		}
		if writer.err != nil {
			return
		}

		p.openClawSagaMu.RLock()
		export := p.openClawSaga
		p.openClawSagaMu.RUnlock()
		if export != nil {
			if err := export(r.Context(), w); err != nil {
				p.logger.Error("exporting OpenClaw saga metrics", zap.Error(err))
				writer.println("# OpenClaw saga metrics unavailable")
			}
		}
	}
}

// SetOpenClawSagaExporter registers an optional exporter whose Prometheus text
// output is appended to the /metrics response. It exists so the OpenClaw
// provisioning saga monitor (internal/soulfactory/saga) can surface
// bahia_openclaw_provisioning_* gauges on the same scrape the
// BahiaOpenClaw* alert rules target, without the telemetry package depending
// on the saga package. The exporter must be safe for concurrent use.
func (p *Provider) SetOpenClawSagaExporter(export func(context.Context, io.Writer) error) {
	p.openClawSagaMu.Lock()
	defer p.openClawSagaMu.Unlock()
	p.openClawSaga = export
}

func renderNostrFleetHealthMetrics(writer *prometheusWriter, snapshot NostrFleetHealthSnapshot) {
	writer.println("# HELP bahia_fleet_health_projector_subscription_active Whether the canonical relay subscription is active")
	writer.println("# TYPE bahia_fleet_health_projector_subscription_active gauge")
	writer.printf("bahia_fleet_health_projector_subscription_active %d\n", boolGauge(snapshot.SubscriptionActive))
	writer.println("# HELP bahia_fleet_health_projector_caught_up Whether canonical relay history reached EOSE")
	writer.println("# TYPE bahia_fleet_health_projector_caught_up gauge")
	writer.printf("bahia_fleet_health_projector_caught_up %d\n", boolGauge(snapshot.CaughtUp))
	writer.println("# HELP bahia_fleet_health_projector_last_event_timestamp_seconds Latest canonical observable event timestamp")
	writer.println("# TYPE bahia_fleet_health_projector_last_event_timestamp_seconds gauge")
	writer.printf("bahia_fleet_health_projector_last_event_timestamp_seconds %d\n", unixOrZero(snapshot.LastEventAt))
	writer.println("# HELP bahia_fleet_health_projector_last_ingested_timestamp_seconds Latest canonical observable ingestion timestamp")
	writer.println("# TYPE bahia_fleet_health_projector_last_ingested_timestamp_seconds gauge")
	writer.printf("bahia_fleet_health_projector_last_ingested_timestamp_seconds %d\n", unixOrZero(snapshot.LastIngestedAt))
	writer.println("# HELP bahia_fleet_health_projector_relay_closed_total Relay CLOSED frames observed by the projector subscription")
	writer.println("# TYPE bahia_fleet_health_projector_relay_closed_total counter")
	writer.printf("bahia_fleet_health_projector_relay_closed_total %d\n", snapshot.RelayClosedTotal)
	writer.println("# HELP bahia_fleet_health_projector_errors_total Distinct rejected or over-limit observable events; redeliveries of the same event are counted once")
	writer.println("# TYPE bahia_fleet_health_projector_errors_total counter")
	writer.printf("bahia_fleet_health_projector_errors_total %d\n", snapshot.ProjectionErrors)
	writer.println("# HELP bahia_fleet_health_nostr_entities Observed fleet entities by domain and health status, projected from Nostr state")
	writer.println("# TYPE bahia_fleet_health_nostr_entities gauge")
	for _, domain := range nostrFleetHealthDomains {
		for _, status := range fleetHealthStatuses {
			writer.printf("bahia_fleet_health_nostr_entities{domain=%q,status=%q} %d\n", domain, status, snapshot.Entities[domain+":"+status])
		}
	}
	writer.println("# HELP bahia_fleet_health_nostr_heartbeat_lag_seconds Seconds since the last observed heartbeat for each fleet entity")
	writer.println("# TYPE bahia_fleet_health_nostr_heartbeat_lag_seconds gauge")
	for _, entity := range sortedFloatKeys(snapshot.HeartbeatLagSeconds) {
		writer.printf("bahia_fleet_health_nostr_heartbeat_lag_seconds{entity=%q} %.0f\n", entity, snapshot.HeartbeatLagSeconds[entity])
	}
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func boolGauge(value bool) int {
	if value {
		return 1
	}
	return 0
}

func renderFleetHealthMetrics(writer *prometheusWriter, snapshot FleetHealthSnapshot) {
	writer.println("# HELP bahia_worker_capacity_class_workers Workers by placement capacity class")
	writer.println("# TYPE bahia_worker_capacity_class_workers gauge")
	for _, class := range []string{"open", "reduced", "cleanup_only", "blocked"} {
		writer.printf("bahia_worker_capacity_class_workers{class=%q} %d\n", class, snapshot.WorkerCapacity[class])
	}
	writer.println("# HELP bahia_worker_telemetry_freshness_workers Workers by telemetry freshness")
	writer.println("# TYPE bahia_worker_telemetry_freshness_workers gauge")
	for _, state := range []string{"fresh", "stale", "absent"} {
		writer.printf("bahia_worker_telemetry_freshness_workers{state=%q} %d\n", state, snapshot.TelemetryFreshness[state])
	}
	writer.println("# HELP bahia_worker_heartbeat_lag_seconds Seconds since each known worker heartbeat")
	writer.println("# TYPE bahia_worker_heartbeat_lag_seconds gauge")
	for _, worker := range sortedFloatKeys(snapshot.HeartbeatLagSeconds) {
		writer.printf("bahia_worker_heartbeat_lag_seconds{worker=%q} %.0f\n", worker, snapshot.HeartbeatLagSeconds[worker])
	}
	writer.println("# HELP bahia_fleet_health_drift_states Service states by drift status")
	writer.println("# TYPE bahia_fleet_health_drift_states gauge")
	for _, status := range []string{"in_sync", "drifted", "unknown", "deploying", "remediation_needed"} {
		writer.printf("bahia_fleet_health_drift_states{status=%q} %d\n", status, snapshot.DriftStates[status])
	}
	writer.println("# HELP bahia_fleet_health_drift_age_seconds_max Age of the oldest drifted service state")
	writer.println("# TYPE bahia_fleet_health_drift_age_seconds_max gauge")
	writer.printf("bahia_fleet_health_drift_age_seconds_max %.0f\n", snapshot.MaxDriftAgeSeconds)
	writer.println("# HELP bahia_fleet_health_drift_stuck Service states whose drift is stuck")
	writer.println("# TYPE bahia_fleet_health_drift_stuck gauge")
	writer.printf("bahia_fleet_health_drift_stuck %d\n", snapshot.StuckDriftStates)
	writer.println("# HELP bahia_fleet_health_services Services by derived health")
	writer.println("# TYPE bahia_fleet_health_services gauge")
	for _, health := range []string{"healthy", "degraded", "unknown"} {
		writer.printf("bahia_fleet_health_services{health=%q} %d\n", health, snapshot.ServiceHealth[health])
	}
	writer.println("# HELP bahia_worker_pressure_recommendations Workers by recommended pressure action")
	writer.println("# TYPE bahia_worker_pressure_recommendations gauge")
	for _, action := range []string{"none", "cleanup_recommended", "operator_intervention"} {
		writer.printf("bahia_worker_pressure_recommendations{action=%q} %d\n", action, snapshot.PressureActions[action])
	}
}

func sortedFloatKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
