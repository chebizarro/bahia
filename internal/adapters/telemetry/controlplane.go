package telemetry

import (
	"context"
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
