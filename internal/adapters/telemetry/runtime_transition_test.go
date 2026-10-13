package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func TestRuntimeTransitionExportsLogAndBoundedUnitMetrics(t *testing.T) {
	ctx := t.Context()
	reader := sdkmetric.NewManualReader()
	provider := setup(Config{Enabled: true, ServiceName: "runtime-transition-test"}, zap.NewNop(), reader)
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	exporter := &lifecycleLogExporter{}
	logProvider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(logProvider)
	t.Cleanup(func() {
		global.SetLoggerProvider(previous)
		if err := logProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown logs: %v", err)
		}
	})
	traceID, err := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("0011223344556677")
	if err != nil {
		t.Fatal(err)
	}
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}))
	when := time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)
	RecordRuntimeTransition(ctx, RuntimeTransition{
		ServiceID: "service-uuid", EnvironmentID: "env-uuid", UnitID: "unit-uuid", PreviousUnitID: "old-unit-uuid", DeploymentID: "run-uuid",
		FromState: "starting", ToState: "healthy", FromDrift: "unknown", ToDrift: "in_sync", Reason: "health,runtime_observation", ObservedAt: when,
	})
	RecordRuntimeObservationRetention(ctx, 500, "success")
	RecordRuntimeObservationRetention(ctx, 0, "failure")
	if len(exporter.records) != 1 {
		t.Fatalf("logs=%d, want 1", len(exporter.records))
	}
	record := exporter.records[0]
	if record.EventName() != "runtime.material_transition" || !record.Timestamp().Equal(when) {
		t.Fatalf("log event/time=%q/%v", record.EventName(), record.Timestamp())
	}
	attrs := map[string]string{}
	record.WalkAttributes(func(kv attribute.KeyValue) bool { attrs[string(kv.Key)] = kv.Value.AsString(); return true })
	for key, want := range map[string]string{
		"service.id": "service-uuid", "environment.id": "env-uuid", "unit.id": "unit-uuid", "deployment.id": "run-uuid",
		"from.state": "starting", "to.state": "healthy", "from.drift": "unknown", "to.drift": "in_sync",
		"reason": "health,runtime_observation", "observed_at": when.Format(time.RFC3339Nano), "trace.id": traceID.String(), "span.id": spanID.String(),
	} {
		if attrs[key] != want {
			t.Errorf("%s=%q, want %q", key, attrs[key], want)
		}
	}
	if len(attrs) != 12 {
		t.Errorf("unexpected log attrs: %#v", attrs)
	}
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}
	byName := map[string]metricdata.Aggregation{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			byName[m.Name] = m.Data
		}
	}
	assertInt64SumValue(t, byName, "bahia_runtime_transitions_total", 1)
	assertInt64SumValue(t, byName, "bahia_runtime_observation_rows_retired_total", 500)
	batches, ok := byName["bahia_runtime_observation_retention_batches_total"].(metricdata.Sum[int64])
	if !ok || len(batches.DataPoints) != 2 {
		t.Fatalf("retention batches=%#v", byName["bahia_runtime_observation_retention_batches_total"])
	}
	gauge, ok := byName["bahia_runtime_unit_state"].(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) != 2 {
		t.Fatalf("unit gauge=%#v", byName["bahia_runtime_unit_state"])
	}
	values := map[string]int64{}
	for _, point := range gauge.DataPoints {
		unit, _ := point.Attributes.Value("unit.id")
		values[unit.AsString()] = point.Value
	}
	if values["old-unit-uuid"] != 0 || values["unit-uuid"] != 2 {
		t.Fatalf("unit state=%#v", values)
	}
}

func TestRuntimeStateLabelsAreBounded(t *testing.T) {
	if state, code := runtimeHealthCode("private-health-text"); state != "unknown" || code != 0 {
		t.Fatalf("unbounded health %q/%d", state, code)
	}
	if got := runtimeDriftLabel("private-drift-text"); got != "unknown" {
		t.Fatalf("unbounded drift %q", got)
	}
}
