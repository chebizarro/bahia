package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestLLMPlacementPrefersVLLMForT7920L40S(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		llmWorker("pk-l40s", "T7920 L40S", 1, []domain.WorkerAccelerator{{Vendor: "nvidia", Model: "L40S", Count: 1, MemoryGB: 48}}),
		llmWorker("pk-p40", "T7610 2xP40", 0, []domain.WorkerAccelerator{{Vendor: "nvidia", Model: "P40", Count: 2, MemoryGB: 24}}),
	}}
	svc := NewLLMPlacementService(repo, zap.NewNop())
	candidate, err := svc.SelectCandidate(t.Context(), nil, runtimeRelease(), nil)
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if candidate.BackendKind != domain.LLMBackendKindVLLM || candidate.Worker.PubKey != "pk-l40s" {
		t.Fatalf("expected vllm on L40S worker, got %#v", candidate)
	}
}

func TestLLMPlacementPrefersOllamaForT7610P40WhenNoL40S(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		llmWorker("pk-p40", "T7610 2xP40", 0, []domain.WorkerAccelerator{{Vendor: "nvidia", Model: "P40", Count: 2, MemoryGB: 24}}),
	}}
	svc := NewLLMPlacementService(repo, zap.NewNop())
	candidate, err := svc.SelectCandidate(t.Context(), nil, runtimeRelease(), nil)
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if candidate.BackendKind != domain.LLMBackendKindOllama || candidate.Worker.PubKey != "pk-p40" {
		t.Fatalf("expected ollama on T7610/P40 worker, got %#v", candidate)
	}
}

func TestLLMPlacementExternalReleaseResolvesExternalAPI(t *testing.T) {
	repo := errorWorkerRepo{}
	svc := NewLLMPlacementService(repo, zap.NewNop())
	release := &domain.LLMRelease{
		ID:              uuid.New(),
		ModelSource:     domain.ModelSourceExternal,
		ExternalBackend: &domain.LLMExternalBackendConfig{BaseURL: "https://api.example.com"},
	}
	candidate, err := svc.SelectCandidate(t.Context(), nil, release, nil)
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if candidate.BackendKind != domain.LLMBackendKindExternalAPI || candidate.Worker != nil {
		t.Fatalf("expected external_api without worker, got %#v", candidate)
	}
}

type errorWorkerRepo struct{}

func (errorWorkerRepo) Upsert(context.Context, *domain.Worker) error {
	return fmt.Errorf("unexpected worker inventory access")
}
func (errorWorkerRepo) GetByPubKey(context.Context, string) (*domain.Worker, error) {
	return nil, fmt.Errorf("unexpected worker inventory access")
}
func (errorWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, fmt.Errorf("unexpected worker inventory access")
}
func (errorWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return fmt.Errorf("unexpected worker inventory access")
}

func TestLLMPlacementRejectsRuntimeWorkerWithoutTarget(t *testing.T) {
	worker := llmWorker("pk", "T7920 L40S", 0, []domain.WorkerAccelerator{{Model: "L40S", Count: 1, MemoryGB: 48}})
	worker.RuntimeTarget = nil
	repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
	svc := NewLLMPlacementService(repo, zap.NewNop())
	_, err := svc.SelectCandidate(t.Context(), nil, runtimeRelease(), nil)
	if err == nil {
		t.Fatal("expected no compatible target error")
	}
}

func llmWorker(pubkey, name string, queue int, accelerators []domain.WorkerAccelerator) domain.Worker {
	w := makeWorker(pubkey, name, queue, 10, "", "linux/amd64")
	w.RuntimeTarget = &domain.WorkerRuntimeTarget{
		Type:          domain.RuntimeTypeDocker,
		EndpointRef:   "prod",
		PublicBaseURL: "http://worker.example.com",
	}
	w.Resources = &domain.WorkerResources{MemoryGB: 128}
	w.Accelerators = accelerators
	w.Telemetry = &domain.WorkerTelemetry{
		Memory: &domain.WorkerMemoryTelemetry{TotalBytes: 128 * gibibyte, AvailableBytes: 96 * gibibyte},
	}
	for _, accelerator := range accelerators {
		count := accelerator.Count
		if count <= 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			w.Telemetry.Accelerators = append(w.Telemetry.Accelerators, domain.WorkerAcceleratorTelemetry{Index: len(w.Telemetry.Accelerators), MemoryTotalBytes: int64(accelerator.MemoryGB) * gibibyte, MemoryFreeBytes: int64(accelerator.MemoryGB) * gibibyte})
		}
	}
	return w
}

func runtimeRelease() *domain.LLMRelease {
	return &domain.LLMRelease{
		ID:              uuid.New(),
		ModelRef:        "hf/model",
		ModelSource:     domain.ModelSourceHuggingFace,
		EstimatedVRAMGB: 18,
		RuntimeBackend:  &domain.LLMRuntimeManagedBackendConfig{Image: "llm:latest", HostPort: 8000, ContainerPort: 8000, HealthPath: "/health"},
	}
}
