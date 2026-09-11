package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

// TestMetricsHandlerAppendsOpenClawSagaExporter mirrors the app wiring for
// bahia-tqndf: a saga Monitor over a FileStore is registered through
// SetOpenClawSagaExporter, and its bahia_openclaw_provisioning_* gauges must
// render on the /metrics scrape the BahiaOpenClaw* alert rules target.
func TestMetricsHandlerAppendsOpenClawSagaExporter(t *testing.T) {
	store, err := saga.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := saga.NewRun("request-metrics", "run-metrics", "agent-metrics", "sha256:spec", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	monitor, err := saga.NewMonitor(saga.MonitorConfig{
		Store: store, Instance: "bahia-test", Build: "test-build",
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := Setup(Config{Enabled: true, ServiceName: "bahia"}, nil)
	provider.SetOpenClawSagaExporter(monitor.WritePrometheus)

	recorder := httptest.NewRecorder()
	provider.MetricsHandler()(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"bahia_openclaw_provisioning_build_info{instance=\"bahia-test\",build=\"test-build\"} 1",
		"bahia_openclaw_provisioning_stage{",
		"request_id=\"request-metrics\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/metrics missing %q", want)
		}
	}
}

// Without an exporter registered, /metrics must not emit saga gauges.
func TestMetricsHandlerOmitsSagaGaugesWithoutExporter(t *testing.T) {
	provider := Setup(Config{Enabled: true, ServiceName: "bahia"}, nil)
	recorder := httptest.NewRecorder()
	provider.MetricsHandler()(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(recorder.Body.String(), "bahia_openclaw_provisioning_") {
		t.Fatal("/metrics emitted saga gauges without a registered exporter")
	}
}