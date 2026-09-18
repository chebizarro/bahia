package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const controlPlaneInstrumentationName = "github.com/openagentsinc/bahia/controlplane"

var (
	controlPlaneMeter = otel.Meter(controlPlaneInstrumentationName)

	driftCounter, _ = controlPlaneMeter.Int64Counter(
		"bahia.controlplane.drift",
		metric.WithDescription("Drift detections observed by the Bahia reconciler"),
	)
	reconcileLatency, _ = controlPlaneMeter.Float64Histogram(
		"bahia.controlplane.reconcile.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of Bahia reconciliation cycles"),
	)
	dispatchCounter, _ = controlPlaneMeter.Int64Counter(
		"bahia.controlplane.dispatch",
		metric.WithDescription("ContextVM and Loom dispatch outcomes"),
	)
	promotionCounter, _ = controlPlaneMeter.Int64Counter(
		"bahia.controlplane.release.outcome",
		metric.WithDescription("Release promotion and rollback outcomes"),
	)
)

// StartOperation starts a control-plane span with stable, low-cardinality attributes.
func StartOperation(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(controlPlaneInstrumentationName).Start(ctx, spanName, trace.WithAttributes(attrs...))
}

// EndOperation records an outcome and error, emits a correlated OTel lifecycle
// log record, and ends the span. Existing zap logging remains unchanged.
func EndOperation(ctx context.Context, span trace.Span, operation, outcome string, err error, attrs ...attribute.KeyValue) {
	outcome = boundedOutcome(outcome, err)
	attrs = append(attrs, attribute.String("outcome", outcome))
	span.SetAttributes(attrs...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, outcome)
	}
	EmitLifecycle(ctx, operation, outcome, err, attrs...)
	span.End()
}

// RecordReconcile records the reconcile latency histogram and the existing
// Prometheus-compatible in-process metrics.
func RecordReconcile(ctx context.Context, duration time.Duration, statesChecked int, outcome string) {
	outcome = boundedOutcome(outcome, nil)
	reconcileLatency.Record(ctx, duration.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
	if metrics := activeMetrics.Load(); metrics != nil {
		metrics.RecordReconcile(duration, statesChecked)
	}
}

// RecordDrift records one drift detection in both OTel and the existing metrics endpoint.
func RecordDrift(ctx context.Context) {
	driftCounter.Add(ctx, 1)
	if metrics := activeMetrics.Load(); metrics != nil {
		metrics.RecordDriftDetected()
	}
}

// RecordDispatch records one bounded kind/outcome dispatch result.
func RecordDispatch(ctx context.Context, kind int, outcome string) {
	outcome = boundedOutcome(outcome, nil)
	kindLabel := strconv.Itoa(kind)
	dispatchCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("nostr.kind", kindLabel),
		attribute.String("outcome", outcome),
	))
	if metrics := activeMetrics.Load(); metrics != nil {
		metrics.RecordControlPlaneDispatch(kindLabel, outcome)
	}
}

// RecordReleaseOutcome records a promotion or rollback outcome.
//
// Labels stay bounded to (operation, outcome). The OCI manifest digest is
// deliberately excluded: it is unique per build, so as a metric label it would
// create an unbounded time series per artifact. The digest is carried on the
// promotion/rollback span and its lifecycle log record instead, which is where
// the artifact↔trace join is performed.
func RecordReleaseOutcome(ctx context.Context, operation, outcome string) {
	operation = boundedOperation(operation)
	outcome = boundedOutcome(outcome, nil)
	promotionCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("outcome", outcome),
	))
	if metrics := activeMetrics.Load(); metrics != nil {
		metrics.RecordReleaseOutcome(operation, outcome)
	}
}

// OCIManifestDigestAttribute is the span attribute key that carries the OCI
// image manifest digest of a release artifact.
//
// The value is deliberately identical to loom-worker's
// OCI_MANIFEST_DIGEST_ATTRIBUTE (src/telemetry/job-lifecycle.ts), which
// loom-worker stamps on its `loom.oci.build` span. Keeping the key byte-for-byte
// identical is what lets a trace backend join loom's build span to Bahia's
// promotion span on the same artifact. Do not rename one side without the other.
const OCIManifestDigestAttribute = "oci.manifest.digest"

// OCIManifestDigestRestoredAttribute carries the digest a rollback restores.
// A rollback span continues the trace of the release being rolled back, so
// OCIManifestDigestAttribute names the failing release and this key names the
// known-good artifact that replaced it.
const OCIManifestDigestRestoredAttribute = "oci.manifest.digest.restored"

// traceCarrierKeys are the W3C trace-context fields propagated across hops.
var traceCarrierKeys = [...]string{"traceparent", "tracestate"}

// OCIManifestDigest returns the manifest-digest span attribute.
//
// Digests are unbounded cardinality, so this belongs on spans and log records
// only. It is deliberately NOT added to the release-outcome metric labels (see
// RecordReleaseOutcome) — one label value per build would blow up the metric's
// time-series count in VictoriaMetrics.
func OCIManifestDigest(digest string) attribute.KeyValue {
	return attribute.String(OCIManifestDigestAttribute, strings.TrimSpace(digest))
}

// OCIManifestDigestRestored returns the restored-digest span attribute.
func OCIManifestDigestRestored(digest string) attribute.KeyValue {
	return attribute.String(OCIManifestDigestRestoredAttribute, strings.TrimSpace(digest))
}

// InjectTraceContext stamps the current W3C trace context on an outbound Nostr event.
// Existing trace tags are replaced so callers cannot accidentally publish stale context.
func InjectTraceContext(ctx context.Context, tags nostr.Tags) nostr.Tags {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	traceparent := strings.TrimSpace(carrier.Get("traceparent"))
	if traceparent == "" {
		return tags
	}
	out := make(nostr.Tags, 0, len(tags)+2)
	for _, tag := range tags {
		if len(tag) > 0 && (tag[0] == "traceparent" || tag[0] == "tracestate") {
			continue
		}
		out = append(out, tag)
	}
	out = append(out, nostr.Tag{"traceparent", traceparent})
	if tracestate := strings.TrimSpace(carrier.Get("tracestate")); tracestate != "" {
		out = append(out, nostr.Tag{"tracestate", tracestate})
	}
	return out
}

// ExtractTraceContext continues a valid W3C trace context carried by Nostr tags.
// Invalid or absent values are ignored by the W3C propagator.
func ExtractTraceContext(ctx context.Context, tags nostr.Tags) context.Context {
	carrier := propagation.MapCarrier{}
	for _, tag := range tags {
		if len(tag) < 2 {
			continue
		}
		if tag[0] == "traceparent" || tag[0] == "tracestate" {
			carrier.Set(tag[0], tag[1])
		}
	}
	return propagation.TraceContext{}.Extract(ctx, carrier)
}

// ExtractTraceContextFromSignedEvent continues the W3C trace context carried by
// a stored signed Nostr event, given as the raw JSON Bahia persisted at ingest
// (for example domain.HiveCIAcceptedRelease.SignedEvent, the terminal 5402).
//
// Using the persisted event rather than the live subscription makes the
// continuation durable: a promotion that happens after a restart, a replay, or
// an orphan sweep still joins the original build trace. Absent, malformed, or
// invalid context is ignored and ctx is returned unchanged.
func ExtractTraceContextFromSignedEvent(ctx context.Context, rawEvent string) context.Context {
	raw := strings.TrimSpace(rawEvent)
	if raw == "" {
		return ctx
	}
	var decoded struct {
		Tags [][]string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return ctx
	}
	tags := make(nostr.Tags, 0, len(decoded.Tags))
	for _, tag := range decoded.Tags {
		tags = append(tags, nostr.Tag(tag))
	}
	return ExtractTraceContext(ctx, tags)
}

// TraceContextMetadata returns the active W3C trace context as plain metadata
// fields, for embedding in a durable record (such as a promotion deployment
// intent) so that a much later causal hop — a rollback — can rejoin the trace.
// It returns nil when no context is recording.
func TraceContextMetadata(ctx context.Context) map[string]string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	out := map[string]string{}
	for _, key := range traceCarrierKeys {
		if value := strings.TrimSpace(carrier.Get(key)); value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 || out["traceparent"] == "" {
		return nil
	}
	return out
}

// ExtractTraceContextFromMetadata continues the trace context stamped into a
// durable metadata map by TraceContextMetadata. Non-string and absent values are
// ignored by the W3C propagator.
func ExtractTraceContextFromMetadata(ctx context.Context, metadata map[string]any) context.Context {
	if len(metadata) == 0 {
		return ctx
	}
	tags := make(nostr.Tags, 0, len(traceCarrierKeys))
	for _, key := range traceCarrierKeys {
		value, _ := metadata[key].(string)
		if value = strings.TrimSpace(value); value != "" {
			tags = append(tags, nostr.Tag{key, value})
		}
	}
	if len(tags) == 0 {
		return ctx
	}
	return ExtractTraceContext(ctx, tags)
}

// EmitLifecycle sends a key lifecycle record through the configured OTel log
// provider. It is a no-op when telemetry is disabled or exporter setup failed.
func EmitLifecycle(ctx context.Context, event, outcome string, err error, attrs ...attribute.KeyValue) {
	var record otellog.Record
	now := time.Now().UTC()
	record.SetTimestamp(now)
	record.SetObservedTimestamp(now)
	record.SetEventName(event)
	record.SetBody(otellog.StringValue(event))
	if err != nil {
		record.SetSeverity(otellog.SeverityError)
		record.SetSeverityText("ERROR")
		record.AddAttributes(otellog.String("error.type", fmt.Sprintf("%T", err)))
	} else {
		record.SetSeverity(otellog.SeverityInfo)
		record.SetSeverityText("INFO")
	}
	record.AddAttributes(otellog.String("outcome", boundedOutcome(outcome, err)))
	for _, attr := range attrs {
		record.AddAttributes(otellog.String(string(attr.Key), attr.Value.Emit()))
	}
	global.Logger(controlPlaneInstrumentationName).Emit(ctx, record)
}

func boundedOutcome(outcome string, err error) string {
	if err != nil {
		return "failure"
	}
	switch strings.ToLower(strings.TrimSpace(outcome)) {
	case "success", "failure", "skipped", "partial", "accepted", "rejected", "approved", "completed":
		return strings.ToLower(strings.TrimSpace(outcome))
	default:
		return "unknown"
	}
}

func boundedOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "promotion", "rollback":
		return strings.ToLower(strings.TrimSpace(operation))
	default:
		return "unknown"
	}
}
