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

func TestMLPlacementRejectsNonActiveSchedulingStates(t *testing.T) {
	cordoned := mlWorker("pk-cordoned", "cordoned", 0, mlVLLMCaps())
	cordoned.SchedulingState = domain.WorkerSchedulingCordoned
	cordoned.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}
	active := mlWorker("pk-active", "active", 0, mlVLLMCaps())
	active.SchedulingState = domain.WorkerSchedulingActive
	active.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}

	repo := &mockWorkerRepo{workers: []domain.Worker{cordoned, active}}
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
	if candidate.Worker.PubKey != "pk-active" {
		t.Fatalf("expected active worker, got %s", candidate.Worker.PubKey)
	}
}

func TestMLPlacementPreviewIncludesSchedulingRejectionReason(t *testing.T) {
	draining := mlWorker("pk-draining", "draining", 0, mlVLLMCaps())
	draining.SchedulingState = domain.WorkerSchedulingDraining
	draining.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}
	active := mlWorker("pk-active", "active", 0, mlVLLMCaps())
	active.SchedulingState = domain.WorkerSchedulingActive
	active.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}

	repo := &mockWorkerRepo{workers: []domain.Worker{draining, active}}
	preview, err := NewMLPlacementService(repo, zap.NewNop()).PreviewCandidates(t.Context(), MLPlacementRequest{
		TaskKind:        domain.MLTaskKindChatCompletions,
		RuntimeKind:     domain.MLRuntimeKindVLLM,
		ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
		Accelerator:     "gpu_nvidia_cuda",
		MinVRAMGB:       48,
	})
	if err != nil {
		t.Fatalf("preview candidates: %v", err)
	}
	if len(preview) != 2 {
		t.Fatalf("expected eligible and rejected preview candidates, got %d", len(preview))
	}
	if !preview[0].Eligible || preview[0].Worker.PubKey != "pk-active" {
		t.Fatalf("expected active candidate first, got %#v", preview[0])
	}
	if preview[1].Eligible || preview[1].Worker.PubKey != "pk-draining" {
		t.Fatalf("expected rejected draining candidate second, got %#v", preview[1])
	}
	if !strings.Contains(preview[1].Reason, "worker is draining") {
		t.Fatalf("expected draining rejection reason, got %q", preview[1].Reason)
	}
}

func TestMLPlacementRejectsEveryNonActiveSchedulingStateWithReason(t *testing.T) {
	tests := []struct {
		name       string
		state      domain.WorkerSchedulingState
		wantReason string
	}{
		{"cordoned", domain.WorkerSchedulingCordoned, "worker is cordoned"},
		{"draining", domain.WorkerSchedulingDraining, "worker is draining"},
		{"maintenance", domain.WorkerSchedulingMaintenance, "worker is in maintenance"},
		{"disabled", domain.WorkerSchedulingDisabled, "worker is disabled"},
		{"unknown", domain.WorkerSchedulingState("paused"), "worker scheduling state \"paused\" is not active"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worker := mlWorker("pk-"+tc.name, tc.name, 0, mlVLLMCaps())
			worker.SchedulingState = tc.state
			worker.Accelerators = []domain.WorkerAccelerator{{Vendor: "nvidia", Count: 1, MemoryGB: 48}}
			repo := &mockWorkerRepo{workers: []domain.Worker{worker}}
			svc := NewMLPlacementService(repo, zap.NewNop())
			req := MLPlacementRequest{
				TaskKind:        domain.MLTaskKindChatCompletions,
				RuntimeKind:     domain.MLRuntimeKindVLLM,
				ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors},
				Accelerator:     "gpu_nvidia_cuda",
				MinVRAMGB:       48,
			}

			_, err := svc.SelectCandidate(t.Context(), req)
			if err == nil || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("expected selection error containing %q, got %v", tc.wantReason, err)
			}

			preview, err := svc.PreviewCandidates(t.Context(), req)
			if err != nil {
				t.Fatalf("preview candidates: %v", err)
			}
			if len(preview) != 1 || preview[0].Eligible {
				t.Fatalf("expected one rejected candidate, got %#v", preview)
			}
			if !strings.Contains(preview[0].Reason, tc.wantReason) {
				t.Fatalf("expected preview reason containing %q, got %q", tc.wantReason, preview[0].Reason)
			}
		})
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
