package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestSetup_Disabled(t *testing.T) {
	logger := zap.NewNop()
	cfg := Config{Enabled: false}

	p := Setup(cfg, logger)
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if p.GetMetrics() == nil {
		t.Fatal("expected non-nil metrics")
	}
}

func TestSetup_Enabled(t *testing.T) {
	logger := zap.NewNop()
	cfg := Config{
		Enabled:     true,
		ServiceName: "test-service",
	}

	p := Setup(cfg, logger)
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if !p.config.Enabled {
		t.Fatal("expected provider config to remain enabled")
	}
	if p.config.ServiceName != "test-service" {
		t.Fatalf("expected service name to be preserved, got %q", p.config.ServiceName)
	}
}

func TestSetup_OTLPHTTPExportsAndShutdown(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := Setup(Config{
		Enabled:        true,
		ServiceName:    "test-service",
		ServiceVersion: "1.2.3",
		Environment:    "test",
		OTLPEndpoint:   server.URL,
		OTLPProtocol:   "http",
	}, zap.NewNop())
	if err := provider.Err(); err != nil {
		t.Fatalf("Setup error: %v", err)
	}
	if provider.TracerProvider() == nil || provider.MeterProvider() == nil {
		t.Fatal("expected configured trace and metric providers")
	}

	_, span := provider.TracerProvider().Tracer("telemetry-test").Start(context.Background(), "exported-span")
	span.End()
	counter, err := provider.MeterProvider().Meter("telemetry-test").Int64Counter("test.counter")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(context.Background(), 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown error: %v", err)
	}
	if err := provider.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests["/v1/traces"] == 0 {
		t.Fatalf("trace export requests = %#v, want /v1/traces", requests)
	}
	if requests["/v1/metrics"] == 0 {
		t.Fatalf("metric export requests = %#v, want /v1/metrics", requests)
	}
}

func TestSetup_UnsupportedOTLPProtocolIsObservable(t *testing.T) {
	provider := Setup(Config{Enabled: true, OTLPEndpoint: "collector:4317", OTLPProtocol: "bogus"}, zap.NewNop())
	if provider.Err() == nil {
		t.Fatal("expected OTLP setup error")
	}
	if err := provider.Shutdown(context.Background()); err == nil {
		t.Fatal("expected Shutdown to return retained setup error")
	}
}

func TestMetrics_RecordHTTPRequest(t *testing.T) {
	m := NewMetrics()

	m.RecordHTTPRequest("GET", "/api/test", 200, 100*time.Millisecond)
	m.RecordHTTPRequest("GET", "/api/test", 200, 200*time.Millisecond)
	m.RecordHTTPRequest("POST", "/api/test", 201, 50*time.Millisecond)

	if m.HTTPRequestsTotal["GET:/api/test:200"] != 2 {
		t.Errorf("expected 2 GET requests, got %d", m.HTTPRequestsTotal["GET:/api/test:200"])
	}
	if m.HTTPRequestsTotal["POST:/api/test:201"] != 1 {
		t.Errorf("expected 1 POST request, got %d", m.HTTPRequestsTotal["POST:/api/test:201"])
	}
	if len(m.HTTPRequestDurations) != 3 {
		t.Errorf("expected 3 durations, got %d", len(m.HTTPRequestDurations))
	}
}

func TestMetrics_RecordDeployment(t *testing.T) {
	m := NewMetrics()

	m.RecordDeployment("myservice", "production", "success")
	m.RecordDeployment("myservice", "production", "success")
	m.RecordDeployment("myservice", "staging", "failed")

	if m.DeploymentsTotal["myservice:production:success"] != 2 {
		t.Errorf("expected 2 successful deployments, got %d", m.DeploymentsTotal["myservice:production:success"])
	}
}

func TestMetrics_RecordAdoptionAndRuntimeOperations(t *testing.T) {
	m := NewMetrics()

	m.RecordAdoptionScan(2, 5, 3, 150*time.Millisecond, true)
	m.RecordAdoptionImport(5, 4, 1, 2, 300*time.Millisecond)
	m.RecordRuntimeAction("restart", "success", 75*time.Millisecond)
	m.RecordRuntimeAction("deploy", "failed", 125*time.Millisecond)

	if m.AdoptionScansTotal["success"] != 1 {
		t.Fatalf("expected one successful adoption scan, got %d", m.AdoptionScansTotal["success"])
	}
	if m.AdoptionTargetsScannedTotal != 2 || m.AdoptionCandidatesTotal != 10 {
		t.Fatalf("unexpected adoption aggregate counts: targets=%d candidates=%d", m.AdoptionTargetsScannedTotal, m.AdoptionCandidatesTotal)
	}
	if m.AdoptionRedactedKeysTotal != 5 {
		t.Fatalf("expected 5 redacted keys, got %d", m.AdoptionRedactedKeysTotal)
	}
	if m.AdoptionImportsTotal["partial_failure"] != 1 {
		t.Fatalf("expected partial failure import count, got %#v", m.AdoptionImportsTotal)
	}
	if m.AdoptionImportSuccessTotal != 4 || m.AdoptionImportFailureTotal != 1 {
		t.Fatalf("unexpected import counts: success=%d failure=%d", m.AdoptionImportSuccessTotal, m.AdoptionImportFailureTotal)
	}
	if m.RuntimeActionsTotal["restart:success"] != 1 || m.RuntimeActionsTotal["deploy:failed"] != 1 {
		t.Fatalf("unexpected runtime action counts: %#v", m.RuntimeActionsTotal)
	}
}

func TestMetrics_RecordDriftDetected(t *testing.T) {
	m := NewMetrics()

	m.RecordDriftDetected()
	m.RecordDriftDetected()
	m.RecordDriftDetected()

	if m.DriftDetectedTotal != 3 {
		t.Errorf("expected 3 drift detections, got %d", m.DriftDetectedTotal)
	}
}

func TestMetrics_RecordReconcile(t *testing.T) {
	m := NewMetrics()

	m.RecordReconcile(500*time.Millisecond, 10)
	m.RecordReconcile(600*time.Millisecond, 15)

	if m.ReconcileTotal != 2 {
		t.Errorf("expected 2 reconciliations, got %d", m.ReconcileTotal)
	}
	if m.ReconcileStatesChecked != 15 {
		t.Errorf("expected 15 states checked (last value), got %d", m.ReconcileStatesChecked)
	}
}

func TestMetrics_RecordNostr(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrPublished("5100")
	m.RecordNostrPublished("5100")
	m.RecordNostrReceived("5101")

	if m.NostrEventsPublished["5100"] != 2 {
		t.Errorf("expected 2 published events, got %d", m.NostrEventsPublished["5100"])
	}
	if m.NostrEventsReceived["5101"] != 1 {
		t.Errorf("expected 1 received event, got %d", m.NostrEventsReceived["5101"])
	}
}

func TestMetrics_Workers(t *testing.T) {
	m := NewMetrics()

	m.SetWorkersActive(5)
	m.SetWorkersTotal(10)
	m.SetLoomJobsInflight(3)

	if m.WorkersActive != 5 {
		t.Errorf("expected 5 active workers, got %d", m.WorkersActive)
	}
	if m.WorkersTotal != 10 {
		t.Errorf("expected 10 total workers, got %d", m.WorkersTotal)
	}
	if m.LoomJobsInflight != 3 {
		t.Errorf("expected 3 inflight jobs, got %d", m.LoomJobsInflight)
	}
}

func TestMetrics_RecordLoomJob(t *testing.T) {
	m := NewMetrics()

	m.RecordLoomJob("completed")
	m.RecordLoomJob("completed")
	m.RecordLoomJob("failed")

	if m.LoomJobsTotal["completed"] != 2 {
		t.Errorf("expected 2 completed jobs, got %d", m.LoomJobsTotal["completed"])
	}
	if m.LoomJobsTotal["failed"] != 1 {
		t.Errorf("expected 1 failed job, got %d", m.LoomJobsTotal["failed"])
	}
}

func TestMetrics_RecordCashuPayment(t *testing.T) {
	m := NewMetrics()

	m.RecordCashuPayment("sent", 100)
	m.RecordCashuPayment("sent", 200)
	m.RecordCashuPayment("redeemed", 50)
	m.RecordCashuPayment("failed", 75)

	if m.CashuPaymentsTotal["sent"] != 2 {
		t.Errorf("expected 2 sent payments, got %d", m.CashuPaymentsTotal["sent"])
	}
	if m.CashuPaymentsSats != 350 { // 100 + 200 + 50 (sent + redeemed only)
		t.Errorf("expected 350 sats, got %d", m.CashuPaymentsSats)
	}
}

func TestMetrics_SetCashuWalletBalance(t *testing.T) {
	m := NewMetrics()

	m.SetCashuWalletBalance("https://mint1.example.com", 1000)
	m.SetCashuWalletBalance("https://mint2.example.com", 500)

	if m.CashuWalletBalance["https://mint1.example.com"] != 1000 {
		t.Errorf("expected 1000 for mint1, got %d", m.CashuWalletBalance["https://mint1.example.com"])
	}
}

func TestProvider_MetricsHandler(t *testing.T) {
	logger := zap.NewNop()
	p := Setup(Config{Enabled: true}, logger)
	m := p.GetMetrics()

	// Record some metrics
	m.RecordHTTPRequest("GET", "/test", 200, 100*time.Millisecond)
	m.RecordDeployment("svc", "prod", "success")
	m.RecordAdoptionScan(1, 2, 1, 100*time.Millisecond, true)
	m.RecordRuntimeAction("restart", "success", 100*time.Millisecond)
	m.RecordDriftDetected()
	m.SetWorkersActive(5)
	m.RecordCashuPayment("sent", 100)

	// Get metrics output
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	p.MetricsHandler()(w, req)

	body := w.Body.String()

	// Verify key metrics are present
	checks := []string{
		"bahia_http_requests_total",
		"bahia_deployments_total",
		"bahia_adoption_scans_total",
		"bahia_runtime_actions_total",
		"bahia_drift_detected_total",
		"bahia_workers_active 5",
		"bahia_cashu_payments_total",
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("expected metrics output to contain %q", check)
		}
	}

	// Check content type
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %s", ct)
	}
}

func TestProvider_MetricsHandler_FleetHealthEntities(t *testing.T) {
	logger := zap.NewNop()
	p := Setup(Config{Enabled: true}, logger)
	m := p.GetMetrics()

	m.SetFleetHealthEntities("worker", "healthy", 3)
	m.SetFleetHealthEntities("service", "degraded", 1)
	m.SetFleetHealthEntities("runtime", "unhealthy", 2)
	m.SetFleetHealthEntities("runtime", "unknown", -1)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	p.MetricsHandler()(w, req)

	body := w.Body.String()
	checks := []string{
		`bahia_fleet_health_entities{domain="worker",status="healthy"} 3`,
		`bahia_fleet_health_entities{domain="service",status="degraded"} 1`,
		`bahia_fleet_health_entities{domain="runtime",status="unhealthy"} 2`,
		`bahia_fleet_health_entities{domain="runtime",status="unknown"} 0`,
		`bahia_fleet_health_entities{domain="worker",status="degraded"} 0`,
	}
	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("expected metrics output to contain %q", check)
		}
	}
}

func TestProvider_MetricsHandler_FleetHealthLabelsAreBounded(t *testing.T) {
	logger := zap.NewNop()
	p := Setup(Config{Enabled: true}, logger)
	m := p.GetMetrics()

	m.SetFleetHealthEntities("service_id=secret-service", "raw-error-token", 9)
	m.SetFleetHealthEntities("worker", "raw-error-token", 4)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	p.MetricsHandler()(w, req)

	body := w.Body.String()
	for _, leaked := range []string{"secret-service", "raw-error-token", "service_id"} {
		if strings.Contains(body, leaked) {
			t.Errorf("expected metrics output not to contain unbounded label %q", leaked)
		}
	}
	if !strings.Contains(body, `bahia_fleet_health_entities{domain="worker",status="unknown"} 4`) {
		t.Error("expected unrecognized bounded worker status to collapse to unknown")
	}
}

func TestCalculatePercentiles(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	p50, p90, p99 := calculatePercentiles(values)

	// For 10 elements (indices 0-9):
	// p50 = index 5 = 6
	// p90 = index 9 = 10
	// p99 = index 9 (clamped) = 10
	if p50 != 6 {
		t.Errorf("expected p50=6, got %.2f", p50)
	}
	if p90 != 10 {
		t.Errorf("expected p90=10, got %.2f", p90)
	}
	if p99 != 10 {
		t.Errorf("expected p99=10, got %.2f", p99)
	}
}

func TestCalculatePercentiles_Empty(t *testing.T) {
	p50, p90, p99 := calculatePercentiles(nil)

	if p50 != 0 || p90 != 0 || p99 != 0 {
		t.Errorf("expected all zeros for empty slice, got %.2f, %.2f, %.2f", p50, p90, p99)
	}
}

// --- Nostr Protocol Metrics Tests ---

func TestMetrics_RecordNostrEOSE(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrEOSE(100 * time.Millisecond)
	m.RecordNostrEOSE(200 * time.Millisecond)
	m.RecordNostrEOSE(300 * time.Millisecond)

	if len(m.NostrEOSELatencies) != 3 {
		t.Errorf("expected 3 EOSE latencies, got %d", len(m.NostrEOSELatencies))
	}
	// First latency should be 0.1 seconds
	if m.NostrEOSELatencies[0] != 0.1 {
		t.Errorf("expected first latency 0.1s, got %.3f", m.NostrEOSELatencies[0])
	}
}

func TestMetrics_RecordNostrPublishOK(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrPublishOK("wss://relay1.example.com", 50*time.Millisecond)
	m.RecordNostrPublishOK("wss://relay1.example.com", 75*time.Millisecond)
	m.RecordNostrPublishOK("wss://relay2.example.com", 100*time.Millisecond)

	if m.NostrPublishOK["wss://relay1.example.com"] != 2 {
		t.Errorf("expected 2 publishes to relay1, got %d", m.NostrPublishOK["wss://relay1.example.com"])
	}
	if m.NostrPublishOK["wss://relay2.example.com"] != 1 {
		t.Errorf("expected 1 publish to relay2, got %d", m.NostrPublishOK["wss://relay2.example.com"])
	}
	if len(m.NostrPublishLatencies) != 3 {
		t.Errorf("expected 3 latencies, got %d", len(m.NostrPublishLatencies))
	}
}

func TestMetrics_RecordNostrPublishFailed(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrPublishFailed("wss://relay.example.com", "auth-required")
	m.RecordNostrPublishFailed("wss://relay.example.com", "auth-required")
	m.RecordNostrPublishFailed("wss://relay.example.com", "rate-limited")
	m.RecordNostrPublishFailed("wss://other.relay.com", "blocked")

	if m.NostrPublishFailed["wss://relay.example.com:auth-required"] != 2 {
		t.Errorf("expected 2 auth-required failures, got %d", m.NostrPublishFailed["wss://relay.example.com:auth-required"])
	}
	if m.NostrPublishFailed["wss://relay.example.com:rate-limited"] != 1 {
		t.Errorf("expected 1 rate-limited failure, got %d", m.NostrPublishFailed["wss://relay.example.com:rate-limited"])
	}
	if m.NostrPublishFailed["wss://other.relay.com:blocked"] != 1 {
		t.Errorf("expected 1 blocked failure, got %d", m.NostrPublishFailed["wss://other.relay.com:blocked"])
	}
}

func TestMetrics_RecordNostrReconnect(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrReconnect("wss://relay1.example.com")
	m.RecordNostrReconnect("wss://relay1.example.com")
	m.RecordNostrReconnect("wss://relay2.example.com")

	if m.NostrReconnects["wss://relay1.example.com"] != 2 {
		t.Errorf("expected 2 reconnects to relay1, got %d", m.NostrReconnects["wss://relay1.example.com"])
	}
	if m.NostrReconnects["wss://relay2.example.com"] != 1 {
		t.Errorf("expected 1 reconnect to relay2, got %d", m.NostrReconnects["wss://relay2.example.com"])
	}
}

func TestMetrics_RecordNostrBackoff(t *testing.T) {
	m := NewMetrics()

	m.RecordNostrBackoff(1 * time.Second)
	m.RecordNostrBackoff(2 * time.Second)
	m.RecordNostrBackoff(4 * time.Second)

	if len(m.NostrBackoffDurations) != 3 {
		t.Errorf("expected 3 backoff durations, got %d", len(m.NostrBackoffDurations))
	}
	if m.NostrBackoffDurations[0] != 1.0 {
		t.Errorf("expected first backoff 1.0s, got %.3f", m.NostrBackoffDurations[0])
	}
}

func TestProvider_MetricsHandler_NostrProtocol(t *testing.T) {
	logger := zap.NewNop()
	p := Setup(Config{Enabled: true}, logger)
	m := p.GetMetrics()

	// Record Nostr protocol metrics
	m.RecordNostrEOSE(150 * time.Millisecond)
	m.RecordNostrPublishOK("wss://relay.test.com", 50*time.Millisecond)
	m.RecordNostrPublishFailed("wss://relay.test.com", "auth-required")
	m.RecordNostrReconnect("wss://relay.test.com")
	m.RecordNostrBackoff(2 * time.Second)

	// Get metrics output
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	p.MetricsHandler()(w, req)

	body := w.Body.String()

	// Verify Nostr protocol metrics are present
	checks := []string{
		"bahia_nostr_eose_latency_seconds",
		"bahia_nostr_publish_ok_total",
		"bahia_nostr_publish_failed_total",
		"bahia_nostr_reconnects_total",
		"bahia_nostr_backoff_seconds",
		"bahia_nostr_publish_latency_seconds",
	}

	for _, check := range checks {
		if !strings.Contains(body, check) {
			t.Errorf("expected metrics output to contain %q\nBody:\n%s", check, body)
		}
	}
}

func TestMetrics_NostrEOSE_MemoryLimit(t *testing.T) {
	m := NewMetrics()

	// Add more than 1000 samples
	for i := 0; i < 1100; i++ {
		m.RecordNostrEOSE(time.Duration(i) * time.Millisecond)
	}

	// Should be capped at 1000
	if len(m.NostrEOSELatencies) > 1000 {
		t.Errorf("expected max 1000 EOSE latencies, got %d", len(m.NostrEOSELatencies))
	}
}
