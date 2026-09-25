package telemetry

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const applicationInstrumentationName = "github.com/openagentsinc/bahia/application"

type appMetricInstruments struct {
	httpRequests            metric.Int64Counter
	httpRequestDuration     metric.Float64Histogram
	deployments             metric.Int64Counter
	driftDetected           metric.Int64Counter
	hygieneScans            metric.Int64Counter
	hygieneCandidates       metric.Int64Counter
	hygieneActions          metric.Int64Counter
	hygienePressureBreaches metric.Int64Counter
	fleetHealthEntities     metric.Int64Gauge
	adoptionScans           metric.Int64Counter
	adoptionTargetsScanned  metric.Int64Counter
	adoptionCandidates      metric.Int64Counter
	adoptionRedactedKeys    metric.Int64Counter
	adoptionScanDuration    metric.Float64Histogram
	adoptionImports         metric.Int64Counter
	adoptionImportSuccess   metric.Int64Counter
	adoptionImportFailure   metric.Int64Counter
	adoptionImportDuration  metric.Float64Histogram
	runtimeActions          metric.Int64Counter
	runtimeActionDuration   metric.Float64Histogram
	reconcileTotal          metric.Int64Counter
	reconcileStatesChecked  metric.Int64Gauge
	reconcileDuration       metric.Float64Histogram
	controlPlaneDispatch    metric.Int64Counter
	releaseOutcomes         metric.Int64Counter
	nostrEventsPublished    metric.Int64Counter
	nostrEventsReceived     metric.Int64Counter
	audit4903Anomalies      metric.Int64Counter
	authorizationRejections metric.Int64Counter
	tierRejections          metric.Int64Counter
	nostrEOSELatency        metric.Float64Histogram
	nostrPublishOK          metric.Int64Counter
	nostrPublishFailed      metric.Int64Counter
	nostrPublishLatency     metric.Float64Histogram
	nostrReconnects         metric.Int64Counter
	nostrBackoff            metric.Float64Histogram
	nostrRelayHealthy       metric.Int64Gauge
	nostrRelayDegraded      metric.Int64Gauge
	nostrRelaySuccessRate   metric.Float64Gauge
	nostrRelayClosed        metric.Int64Counter
	nostrRelayReREQAttempts metric.Int64Counter
	nostrRelayReconnects    metric.Int64Counter
	nostrOutboxDepth        metric.Int64Gauge
	nostrEventStoreBytes    metric.Int64Gauge
	nostrEventStoreRows     metric.Int64Gauge
	nostrEventStoreOldest   metric.Int64Gauge
	nostrArchiveBatches     metric.Int64Gauge
	nostrRelaysHealthy      metric.Int64Gauge
	nostrRelaysDegraded     metric.Int64Gauge
	nostrRelaysUnhealthy    metric.Int64Gauge
	workersActive           metric.Int64Gauge
	workersTotal            metric.Int64Gauge
	loomJobsInflight        metric.Int64Gauge
	loomJobs                metric.Int64Counter
	cashuPayments           metric.Int64Counter
	cashuPaymentsSats       metric.Int64Counter
	cashuWalletBalance      metric.Int64Gauge
}

type metricInstrumentBuilder struct {
	meter metric.Meter
	errs  []error
}

func (b *metricInstrumentBuilder) int64Counter(name, description string) metric.Int64Counter {
	instrument, err := b.meter.Int64Counter(name, metric.WithDescription(description))
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("creating counter %s: %w", name, err))
	}
	return instrument
}
func (b *metricInstrumentBuilder) int64Gauge(name, description string) metric.Int64Gauge {
	instrument, err := b.meter.Int64Gauge(name, metric.WithDescription(description))
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("creating gauge %s: %w", name, err))
	}
	return instrument
}
func (b *metricInstrumentBuilder) float64Gauge(name, description string) metric.Float64Gauge {
	instrument, err := b.meter.Float64Gauge(name, metric.WithDescription(description))
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("creating gauge %s: %w", name, err))
	}
	return instrument
}
func (b *metricInstrumentBuilder) secondsHistogram(name, description string) metric.Float64Histogram {
	instrument, err := b.meter.Float64Histogram(name, metric.WithUnit("s"), metric.WithDescription(description))
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("creating histogram %s: %w", name, err))
	}
	return instrument
}

func newAppMetricInstruments(provider metric.MeterProvider) (*appMetricInstruments, error) {
	b := metricInstrumentBuilder{meter: provider.Meter(applicationInstrumentationName)}
	i := &appMetricInstruments{
		httpRequests:            b.int64Counter("bahia_http_requests_total", "Total HTTP requests by method, path, and status code"),
		httpRequestDuration:     b.secondsHistogram("bahia_http_request_duration_seconds", "HTTP request duration in seconds"),
		deployments:             b.int64Counter("bahia_deployments_total", "Total deployments by service, environment, and status"),
		driftDetected:           b.int64Counter("bahia_drift_detected_total", "Total drift detections"),
		hygieneScans:            b.int64Counter("bahia_hygiene_scans_total", "Total hygiene dry-run scans issued"),
		hygieneCandidates:       b.int64Counter("bahia_hygiene_candidates_total", "Hygiene scan candidates by class"),
		hygieneActions:          b.int64Counter("bahia_hygiene_actions_total", "Hygiene maintenance intents by method and status"),
		hygienePressureBreaches: b.int64Counter("bahia_hygiene_pressure_breaches_total", "Pressure threshold breaches (disk>85% / inode)"),
		fleetHealthEntities:     b.int64Gauge("bahia_fleet_health_entities", "Fleet health entity counts by bounded domain and health status"),
		adoptionScans:           b.int64Counter("bahia_adoption_scans_total", "Adoption scan requests by status"),
		adoptionTargetsScanned:  b.int64Counter("bahia_adoption_targets_scanned_total", "Adoption runtime targets scanned"),
		adoptionCandidates:      b.int64Counter("bahia_adoption_candidates_total", "Adoption candidates observed or processed"),
		adoptionRedactedKeys:    b.int64Counter("bahia_adoption_redacted_keys_total", "Sensitive adoption env/label keys redacted or extracted"),
		adoptionScanDuration:    b.secondsHistogram("bahia_adoption_scan_duration_seconds", "Adoption scan duration in seconds"),
		adoptionImports:         b.int64Counter("bahia_adoption_imports_total", "Adoption import batches by status"),
		adoptionImportSuccess:   b.int64Counter("bahia_adoption_import_success_total", "Adoption import candidates that succeeded"),
		adoptionImportFailure:   b.int64Counter("bahia_adoption_import_failure_total", "Adoption import candidates that failed"),
		adoptionImportDuration:  b.secondsHistogram("bahia_adoption_import_duration_seconds", "Adoption import batch duration in seconds"),
		runtimeActions:          b.int64Counter("bahia_runtime_actions_total", "Direct runtime actions by action and status"),
		runtimeActionDuration:   b.secondsHistogram("bahia_runtime_action_duration_seconds", "Direct runtime action duration in seconds"),
		reconcileTotal:          b.int64Counter("bahia_reconcile_total", "Total reconciliation cycles"),
		reconcileStatesChecked:  b.int64Gauge("bahia_reconcile_states_checked", "Number of states checked in last reconcile"),
		reconcileDuration:       b.secondsHistogram("bahia_reconcile_duration_seconds", "Reconciliation cycle duration in seconds"),
		controlPlaneDispatch:    b.int64Counter("bahia_controlplane_dispatch_total", "ContextVM and Loom dispatch outcomes"),
		releaseOutcomes:         b.int64Counter("bahia_release_outcomes_total", "Promotion and rollback outcomes"),
		nostrEventsPublished:    b.int64Counter("bahia_nostr_events_published_total", "Nostr events published by kind"),
		nostrEventsReceived:     b.int64Counter("bahia_nostr_events_received_total", "Nostr events received by kind"),
		audit4903Anomalies:      b.int64Counter("bahia_audit_4903_anomalies_total", "Invalid or contradictory kind-4903 audit events"),
		authorizationRejections: b.int64Counter("bahia_authorization_rejections_total", "Authorization rejections by bounded reason"),
		tierRejections:          b.int64Counter("bahia_tier_rejections_total", "Requests rejected because Bahia's active tier was insufficient"),
		nostrEOSELatency:        b.secondsHistogram("bahia_nostr_eose_latency_seconds", "Time from subscription start to EOSE receipt"),
		nostrPublishOK:          b.int64Counter("bahia_nostr_publish_ok_total", "Successful publishes by relay"),
		nostrPublishFailed:      b.int64Counter("bahia_nostr_publish_failed_total", "Failed publishes by relay and reason"),
		nostrPublishLatency:     b.secondsHistogram("bahia_nostr_publish_latency_seconds", "Publish latency in seconds"),
		nostrReconnects:         b.int64Counter("bahia_nostr_reconnects_total", "Relay reconnection attempts"),
		nostrBackoff:            b.secondsHistogram("bahia_nostr_backoff_seconds", "Backoff duration before reconnection"),
		nostrRelayHealthy:       b.int64Gauge("bahia_nostr_relay_healthy", "Whether relay is healthy (1=yes, 0=no)"),
		nostrRelayDegraded:      b.int64Gauge("bahia_nostr_relay_degraded", "Whether relay is degraded (1=yes, 0=no)"),
		nostrRelaySuccessRate:   b.float64Gauge("bahia_nostr_relay_success_rate", "Relay publish success rate (0.0-1.0)"),
		nostrRelayClosed:        b.int64Counter("bahia_nostr_relay_closed_total", "Relay CLOSED frames by relay and bounded reason"),
		nostrRelayReREQAttempts: b.int64Counter("bahia_nostr_relay_rereq_attempts_total", "Relay subscription recovery REQ attempts"),
		nostrRelayReconnects:    b.int64Counter("bahia_nostr_relay_reconnect_attempts_total", "Relay transport reconnect attempts"),
		nostrOutboxDepth:        b.int64Gauge("bahia_nostr_outbox_depth", "Unpublished events in the durable Nostr publish outbox"),
		nostrEventStoreBytes:    b.int64Gauge("bahia_nostr_event_store_bytes", "PostgreSQL Nostr event relation bytes by component"),
		nostrEventStoreRows:     b.int64Gauge("bahia_nostr_event_store_rows", "Estimated PostgreSQL Nostr event rows by state"),
		nostrEventStoreOldest:   b.int64Gauge("bahia_nostr_event_store_oldest_hot_timestamp_seconds", "Oldest eligible hot Nostr event Unix timestamp; zero until the online archive index exists"),
		nostrArchiveBatches:     b.int64Gauge("bahia_nostr_archive_batches", "Archive batches by bounded lifecycle state"),
		nostrRelaysHealthy:      b.int64Gauge("bahia_nostr_relays_healthy_total", "Count of healthy relays"),
		nostrRelaysDegraded:     b.int64Gauge("bahia_nostr_relays_degraded_total", "Count of degraded relays"),
		nostrRelaysUnhealthy:    b.int64Gauge("bahia_nostr_relays_unhealthy_total", "Count of unhealthy relays"),
		workersActive:           b.int64Gauge("bahia_workers_active", "Currently active (online) workers"),
		workersTotal:            b.int64Gauge("bahia_workers_total", "Total known workers"),
		loomJobsInflight:        b.int64Gauge("bahia_loom_jobs_inflight", "Currently in-flight Loom jobs"),
		loomJobs:                b.int64Counter("bahia_loom_jobs_total", "Total Loom jobs by status"),
		cashuPayments:           b.int64Counter("bahia_cashu_payments_total", "Total Cashu payments by status"),
		cashuPaymentsSats:       b.int64Counter("bahia_cashu_payments_sats_total", "Total sats paid via Cashu"),
		cashuWalletBalance:      b.int64Gauge("bahia_cashu_wallet_balance_sats", "Current wallet balance in sats by mint"),
	}
	return i, errors.Join(b.errs...)
}

func newMetricsWithProvider(provider metric.MeterProvider) (*Metrics, error) {
	metrics := NewMetrics()
	instruments, err := newAppMetricInstruments(provider)
	if err != nil {
		return metrics, err
	}
	metrics.otel = instruments
	metrics.initializeOTelGauges(context.Background())
	return metrics, nil
}

func (m *Metrics) initializeOTelGauges(ctx context.Context) {
	if m.otel == nil {
		return
	}
	for _, domain := range fleetHealthDomains {
		for _, status := range fleetHealthStatuses {
			m.otel.fleetHealthEntities.Record(ctx, 0, metric.WithAttributes(attribute.String("domain", domain), attribute.String("status", status)))
		}
	}
	for _, status := range []string{"claimed", "exported", "protected", "pruned"} {
		m.otel.nostrArchiveBatches.Record(ctx, 0, metric.WithAttributes(attribute.String("status", status)))
	}
	m.otel.reconcileStatesChecked.Record(ctx, 0)
	m.otel.nostrOutboxDepth.Record(ctx, 0)
	m.otel.nostrEventStoreOldest.Record(ctx, 0)
	m.otel.nostrRelaysHealthy.Record(ctx, 0)
	m.otel.nostrRelaysDegraded.Record(ctx, 0)
	m.otel.nostrRelaysUnhealthy.Record(ctx, 0)
	m.otel.workersActive.Record(ctx, 0)
	m.otel.workersTotal.Record(ctx, 0)
	m.otel.loomJobsInflight.Record(ctx, 0)
}
