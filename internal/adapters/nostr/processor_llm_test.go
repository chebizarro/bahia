package nostr

import (
	"context"
	"fmt"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type captureWorkerRepo struct{ worker *domain.Worker }

func (r *captureWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	if r.worker != nil && r.worker.LastAdvertisementAt.After(w.LastAdvertisementAt) {
		return nil
	}
	cp := *w
	r.worker = &cp
	return nil
}
func (r *captureWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	if r.worker == nil {
		return nil, nil
	}
	cp := *r.worker
	return &cp, nil
}
func (r *captureWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, nil
}
func (r *captureWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

type capturePublisher struct{ events []events.Event }

func (p *capturePublisher) Publish(_ context.Context, e events.Event) {
	p.events = append(p.events, e)
}
func (p *capturePublisher) Subscribe(events.EventType, events.Handler) {}

func processorTestWorkerPubKeyHex(t *testing.T) string {
	t.Helper()
	pubkey, err := publicKeyHexFromPrivateKeyHex(testNostrPrivateKey)
	if err != nil {
		t.Fatalf("derive worker pubkey: %v", err)
	}
	return pubkey
}

func processorTestWorkerPubKey(t *testing.T) gonostr.PubKey {
	t.Helper()
	pubkey, err := gonostr.PubKeyFromHex(processorTestWorkerPubKeyHex(t))
	if err != nil {
		t.Fatalf("decode worker pubkey: %v", err)
	}
	return pubkey
}

func TestProcessorWorkerAdvertisementParsesLLMRuntimeMetadata(t *testing.T) {
	repo := &captureWorkerRepo{}
	processor := NewProcessor(nil, repo, zap.NewNop())
	ev := &gonostr.Event{
		PubKey:    processorTestWorkerPubKey(t),
		Kind:      canonicalKind(kindLoomWorkerAd),
		CreatedAt: gonostr.Now(),
		Content:   `{"name":"gpu-worker","resources":{"cpu_cores":32,"memory_gb":256,"disk_gb":1000},"accelerators":[{"vendor":"nvidia","model":"L40S","count":1,"memory_gb":48,"driver":"535"}],"runtime_target":{"type":"compose","endpoint_ref":"gpu-a","compose_dir":"/srv/llm","public_base_url":"https://gpu-a.example"}}`,
	}
	if err := processor.handleWorkerAdvertisement(context.Background(), ev); err != nil {
		t.Fatalf("handle worker ad: %v", err)
	}
	if repo.worker == nil || repo.worker.Resources == nil || repo.worker.Resources.MemoryGB != 256 {
		t.Fatalf("resources not parsed: %#v", repo.worker)
	}
	if len(repo.worker.Accelerators) != 1 || repo.worker.Accelerators[0].Model != "L40S" {
		t.Fatalf("accelerators not parsed: %#v", repo.worker.Accelerators)
	}
	if repo.worker.RuntimeTarget == nil || repo.worker.RuntimeTarget.EndpointRef != "gpu-a" || repo.worker.RuntimeTarget.PublicBaseURL != "https://gpu-a.example" {
		t.Fatalf("runtime target not parsed: %#v", repo.worker.RuntimeTarget)
	}
}

func TestProcessorWorkerAdvertisementParsesTelemetryAssessesPressureAndPublishesEvent(t *testing.T) {
	repo := &captureWorkerRepo{}
	publisher := &capturePublisher{}
	processor := NewProcessorWithPublisher(nil, repo, publisher, zap.NewNop())
	sampledAt := time.Now().UTC().Truncate(time.Second)
	ev := &gonostr.Event{
		PubKey:    processorTestWorkerPubKey(t),
		Kind:      canonicalKind(kindLoomWorkerAd),
		CreatedAt: gonostr.Timestamp(sampledAt.Unix()),
		Content:   fmt.Sprintf(`{"name":"telemetry-worker","max_concurrent_jobs":2,"current_queue_depth":0,"telemetry":{"sampled_at":%q,"memory":{"total_bytes":68719476736,"available_bytes":42949672960},"disk":{"path":"/","total_bytes":1073741824000,"available_bytes":322122547200},"thermal":{"max_temperature_c":60,"throttled":false}}}`, sampledAt.Format(time.RFC3339)),
	}

	if err := processor.handleWorkerAdvertisement(context.Background(), ev); err != nil {
		t.Fatalf("handle worker ad: %v", err)
	}
	if repo.worker == nil || repo.worker.Telemetry == nil || repo.worker.Telemetry.Memory == nil {
		t.Fatalf("telemetry not parsed: %#v", repo.worker)
	}
	if repo.worker.Pressure == nil || repo.worker.Pressure.CapacityClass != domain.WorkerCapacityOpen {
		t.Fatalf("pressure not assessed as open: %#v", repo.worker.Pressure)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
	published := publisher.events[0]
	if published.Type != events.EventWorkerTelemetryObserved || published.EntityID != processorTestWorkerPubKeyHex(t) {
		t.Fatalf("published event = %#v", published)
	}
	payload, ok := published.Data.(events.WorkerTelemetryObserved)
	if !ok {
		t.Fatalf("published payload type = %T", published.Data)
	}
	if payload.Worker.Pressure == nil || payload.Worker.Pressure.CapacityClass != domain.WorkerCapacityOpen {
		t.Fatalf("published worker pressure = %#v", payload.Worker.Pressure)
	}
}

func TestProcessorWorkerAdvertisementUsesConfiguredPressureThresholds(t *testing.T) {
	repo := &captureWorkerRepo{}
	publisher := &capturePublisher{}
	thresholds := service.DefaultWorkerPressureThresholds()
	thresholds.MemoryWarningMinBytes = 48 * 1024 * 1024 * 1024
	thresholds.MemoryCriticalMinBytes = 24 * 1024 * 1024 * 1024
	processor := NewProcessorWithPublisher(nil, repo, publisher, zap.NewNop(), WithPressureThresholds(thresholds))
	sampledAt := time.Now().UTC().Truncate(time.Second)
	ev := &gonostr.Event{
		PubKey:    processorTestWorkerPubKey(t),
		Kind:      canonicalKind(kindLoomWorkerAd),
		CreatedAt: gonostr.Timestamp(sampledAt.Unix()),
		Content:   fmt.Sprintf(`{"name":"telemetry-worker","max_concurrent_jobs":2,"current_queue_depth":0,"telemetry":{"sampled_at":%q,"memory":{"total_bytes":68719476736,"available_bytes":42949672960},"disk":{"path":"/","total_bytes":1073741824000,"available_bytes":322122547200},"thermal":{"max_temperature_c":60,"throttled":false}}}`, sampledAt.Format(time.RFC3339)),
	}

	if err := processor.handleWorkerAdvertisement(context.Background(), ev); err != nil {
		t.Fatalf("handle worker ad: %v", err)
	}
	if repo.worker == nil || repo.worker.Pressure == nil || repo.worker.Pressure.CapacityClass != domain.WorkerCapacityReduced {
		t.Fatalf("pressure did not use configured thresholds: %#v", repo.worker)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(publisher.events))
	}
}

func TestProcessorWorkerAdvertisementSkipsTelemetryEventForStaleAd(t *testing.T) {
	repo := &captureWorkerRepo{worker: &domain.Worker{PubKey: processorTestWorkerPubKeyHex(t), LastAdvertisementAt: fixedProcessorTime().Add(time.Minute)}}
	publisher := &capturePublisher{}
	processor := NewProcessorWithPublisher(nil, repo, publisher, zap.NewNop())
	ev := &gonostr.Event{
		PubKey:    processorTestWorkerPubKey(t),
		Kind:      canonicalKind(kindLoomWorkerAd),
		CreatedAt: gonostr.Timestamp(fixedProcessorTime().Unix()),
		Content:   `{"name":"stale-worker","telemetry":{"sampled_at":"2026-05-24T12:00:00Z"}}`,
	}

	if err := processor.handleWorkerAdvertisement(context.Background(), ev); err != nil {
		t.Fatalf("handle worker ad: %v", err)
	}
	if len(publisher.events) != 0 {
		t.Fatalf("published events = %d, want 0", len(publisher.events))
	}
}

func fixedProcessorTime() time.Time {
	return time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
}
