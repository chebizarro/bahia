package service

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestMLPlacementSelectsGPUVLLMWorker(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		mlWorker("pk-cpu", "cpu", 0, domain.WorkerMLCapabilities{
			Tasks:           []domain.MLTaskKind{domain.MLTaskKindChatCompletions},
			Runtimes:        []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM},
			ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
		}),
		mlWorker("pk-gpu", "gpu", 2, domain.WorkerMLCapabilities{
			Tasks:           []domain.MLTaskKind{domain.MLTaskKindChatCompletions},
			Runtimes:        []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM},
			ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
			Accelerators:    []string{"gpu_nvidia_cuda"},
		}),
	}}
	repo.workers[1].Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Model: "L40S", Count: 1, MemoryGB: 48}}

	candidate, err := NewMLPlacementService(repo, zap.NewNop()).SelectCandidate(t.Context(), MLPlacementRequest{
		TaskKind:        domain.MLTaskKindChatCompletions,
		RuntimeKind:     domain.MLRuntimeKindVLLM,
		ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
		Accelerator:     "gpu_nvidia_cuda",
		MinVRAMGB:       48,
	})
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if candidate.Worker.PubKey != "pk-gpu" {
		t.Fatalf("expected GPU vLLM worker, got %#v", candidate.Worker)
	}
}

func TestMLPlacementFailsClosedForUnsupportedRuntimeFormatToolchain(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		mlWorker("pk-rknn", "rknn", 0, domain.WorkerMLCapabilities{
			Tasks:           []domain.MLTaskKind{domain.MLTaskKindVisionInference},
			Runtimes:        []domain.MLRuntimeKind{domain.MLRuntimeKindRKNNServer},
			ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatONNX},
			Accelerators:    []string{"npu_rk3588"},
		}),
	}}

	_, err := NewMLPlacementService(repo, zap.NewNop()).SelectCandidate(t.Context(), MLPlacementRequest{
		TaskKind:        domain.MLTaskKindVisionInference,
		RuntimeKind:     domain.MLRuntimeKindRKNNServer,
		ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatRKNN},
		Accelerator:     "npu_rk3588",
		Toolchains:      []string{"rknn_toolkit2"},
	})
	if err == nil {
		t.Fatal("expected fail-closed placement error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "artifact format rknn") && !strings.Contains(msg, "toolchain rknn_toolkit2") {
		t.Fatalf("expected unsupported format/toolchain reason, got %q", msg)
	}
}

func TestMLPlacementTieBreaksByWorkerPubkey(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		mlWorker("pk-z", "z", 0, mlVLLMCaps()),
		mlWorker("pk-a", "a", 0, mlVLLMCaps()),
	}}
	for i := range repo.workers {
		repo.workers[i].Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}
	}

	candidate, err := NewMLPlacementService(repo, zap.NewNop()).SelectCandidate(t.Context(), MLPlacementRequest{
		TaskKind:        domain.MLTaskKindChatCompletions,
		RuntimeKind:     domain.MLRuntimeKindVLLM,
		ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
		Accelerator:     "gpu_nvidia_cuda",
		MinVRAMGB:       48,
	})
	if err != nil {
		t.Fatalf("select candidate: %v", err)
	}
	if candidate.Worker.PubKey != "pk-a" {
		t.Fatalf("expected deterministic lowest pubkey tie-break, got %s", candidate.Worker.PubKey)
	}
}

func mlWorker(pubkey, name string, queue int, caps domain.WorkerMLCapabilities) domain.Worker {
	w := llmWorker(pubkey, name, queue, nil)
	w.MLCapabilities = caps
	return w
}

func mlVLLMCaps() domain.WorkerMLCapabilities {
	return domain.WorkerMLCapabilities{
		Tasks:           []domain.MLTaskKind{domain.MLTaskKindChatCompletions},
		Runtimes:        []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM},
		ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
		Accelerators:    []string{"gpu_nvidia_cuda"},
	}
}
