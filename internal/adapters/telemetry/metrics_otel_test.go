package telemetry

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

func TestApplicationMetricsReachConfiguredMeterProvider(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := setup(Config{Enabled: true, ServiceName: "meter-provider-test"}, zap.NewNop(), reader)
	t.Cleanup(func() {
		if err := provider.Shutdown(ctx); err != nil {
			t.Errorf("shutdown telemetry: %v", err)
		}
	})

	metrics := provider.GetMetrics()
	metrics.RecordHTTPRequest("GET", "/ready", 200, 25*time.Millisecond)
	metrics.SetWorkersActive(7)
	RecordDrift(ctx)
	if err := RecordVirtualization(ctx, "capacity", VirtualizationLabels{Provider: "libvirt", Dimension: "cpu"}, 3); err != nil {
		t.Fatalf("record virtualization metric: %v", err)
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("collect configured provider: %v", err)
	}
	byName := make(map[string]metricdata.Aggregation)
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			byName[metric.Name] = metric.Data
		}
	}

	assertInt64SumValue(t, byName, "bahia_http_requests_total", 1)
	assertHistogramCount(t, byName, "bahia_http_request_duration_seconds", 1)
	assertInt64GaugeValue(t, byName, "bahia_workers_active", 7)
	assertInt64SumValue(t, byName, "bahia_drift_detected_total", 1)
	assertInt64SumValue(t, byName, "bahia.controlplane.drift", 1)
	assertFloat64GaugeValue(t, byName, "bahia.virtualization.capacity", 3)
}

func assertInt64SumValue(t *testing.T, metrics map[string]metricdata.Aggregation, name string, want int64) {
	t.Helper()
	sum, ok := metrics[name].(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) == 0 || sum.DataPoints[0].Value != want {
		t.Fatalf("%s = %#v, want int64 sum %d", name, metrics[name], want)
	}
}

func assertHistogramCount(t *testing.T, metrics map[string]metricdata.Aggregation, name string, want uint64) {
	t.Helper()
	histogram, ok := metrics[name].(metricdata.Histogram[float64])
	if !ok || len(histogram.DataPoints) == 0 || histogram.DataPoints[0].Count != want {
		t.Fatalf("%s = %#v, want histogram count %d", name, metrics[name], want)
	}
}

func assertInt64GaugeValue(t *testing.T, metrics map[string]metricdata.Aggregation, name string, want int64) {
	t.Helper()
	gauge, ok := metrics[name].(metricdata.Gauge[int64])
	if !ok || len(gauge.DataPoints) == 0 || gauge.DataPoints[0].Value != want {
		t.Fatalf("%s = %#v, want int64 gauge %d", name, metrics[name], want)
	}
}

func assertFloat64GaugeValue(t *testing.T, metrics map[string]metricdata.Aggregation, name string, want float64) {
	t.Helper()
	gauge, ok := metrics[name].(metricdata.Gauge[float64])
	if !ok || len(gauge.DataPoints) == 0 || gauge.DataPoints[0].Value != want {
		t.Fatalf("%s = %#v, want float64 gauge %f", name, metrics[name], want)
	}
}
