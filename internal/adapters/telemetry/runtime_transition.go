package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const runtimeTransitionInstrumentationName = "github.com/openagentsinc/bahia/runtime-transition"

// RuntimeTransition contains only stable identifiers and bounded state values.
// It intentionally excludes observation metadata, image names, hostnames and
// user-controlled error text from the OTEL signal.
type RuntimeTransition struct {
	ServiceID, EnvironmentID, UnitID, PreviousUnitID, DeploymentID string
	FromState, ToState, FromDrift, ToDrift, Reason                 string
	ObservedAt                                                     time.Time
}

func runtimeHealthCode(state string) (string, int64) {
	switch state {
	case "starting":
		return state, 1
	case "healthy":
		return state, 2
	case "unhealthy":
		return state, 3
	case "stopped":
		return state, 4
	default:
		return "unknown", 0
	}
}

func runtimeDriftLabel(state string) string {
	switch state {
	case "in_sync", "drifted", "deploying", "remediation_needed":
		return state
	default:
		return "unknown"
	}
}

func runtimeUnitAttributes(t RuntimeTransition, unitID string) metric.RecordOption {
	return metric.WithAttributes(
		attribute.String("service.id", t.ServiceID),
		attribute.String("environment.id", t.EnvironmentID),
		attribute.String("unit.id", unitID),
	)
}

// RecordRuntimeUnitState refreshes the current gauge on successful runtime
// observations, including unchanged samples after a daemon restart.
func RecordRuntimeUnitState(ctx context.Context, serviceID, environmentID, unitID, health string) {
	if unitID == "" {
		return
	}
	metrics := activeMetrics.Load()
	if metrics == nil || metrics.otel == nil {
		return
	}
	_, code := runtimeHealthCode(health)
	metrics.otel.runtimeUnitState.Record(ctx, code, runtimeUnitAttributes(RuntimeTransition{
		ServiceID: serviceID, EnvironmentID: environmentID,
	}, unitID))
}

// RecordRuntimeTransition emits one material transition to the configured
// OTLP log and meter providers. It does not publish or sign a Nostr event.
func RecordRuntimeTransition(ctx context.Context, t RuntimeTransition) {
	from, _ := runtimeHealthCode(t.FromState)
	to, code := runtimeHealthCode(t.ToState)
	now := time.Now().UTC()
	observedAt := t.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = now
	}
	span := trace.SpanContextFromContext(ctx)
	var record otellog.Record
	record.SetTimestamp(observedAt)
	record.SetObservedTimestamp(now)
	record.SetEventName("runtime.material_transition")
	record.SetBody(attribute.StringValue("runtime.material_transition"))
	record.SetSeverity(otellog.SeverityInfo)
	record.SetSeverityText("INFO")
	record.AddAttributes(
		attribute.String("service.id", t.ServiceID),
		attribute.String("environment.id", t.EnvironmentID),
		attribute.String("unit.id", t.UnitID),
		attribute.String("deployment.id", t.DeploymentID),
		attribute.String("from.state", from),
		attribute.String("to.state", to),
		attribute.String("from.drift", runtimeDriftLabel(t.FromDrift)),
		attribute.String("to.drift", runtimeDriftLabel(t.ToDrift)),
		attribute.String("reason", t.Reason),
		attribute.String("observed_at", observedAt.Format(time.RFC3339Nano)),
		attribute.String("trace.id", span.TraceID().String()),
		attribute.String("span.id", span.SpanID().String()),
	)
	global.Logger(runtimeTransitionInstrumentationName).Emit(ctx, record)

	if metrics := activeMetrics.Load(); metrics != nil && metrics.otel != nil {
		if t.PreviousUnitID != "" && t.PreviousUnitID != t.UnitID {
			metrics.otel.runtimeUnitState.Record(ctx, 0, runtimeUnitAttributes(t, t.PreviousUnitID))
		}
		if t.UnitID != "" {
			metrics.otel.runtimeUnitState.Record(ctx, code, runtimeUnitAttributes(t, t.UnitID))
			metrics.otel.runtimeTransitions.Add(ctx, 1, metric.WithAttributes(
				attribute.String("service.id", t.ServiceID), attribute.String("environment.id", t.EnvironmentID),
				attribute.String("unit.id", t.UnitID), attribute.String("from.state", from), attribute.String("to.state", to),
			))
		}
	}
}

// RecordRuntimeObservationRetention tracks bounded batch progress and failures.
func RecordRuntimeObservationRetention(ctx context.Context, rows int64, outcome string) {
	metrics := activeMetrics.Load()
	if metrics == nil || metrics.otel == nil {
		return
	}
	if outcome != "success" {
		outcome = "failure"
	}
	metrics.otel.runtimeObservationBatches.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	if rows > 0 {
		metrics.otel.runtimeObservationRowsRetired.Add(ctx, rows)
	}
}
