package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type workerReadModelWorkerRepo struct{ workers []domain.Worker }

func (r *workerReadModelWorkerRepo) Upsert(context.Context, *domain.Worker) error { return nil }
func (r *workerReadModelWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	for i := range r.workers {
		if r.workers[i].PubKey == pubkey {
			w := r.workers[i]
			return &w, nil
		}
	}
	return nil, nil
}
func (r *workerReadModelWorkerRepo) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	out := []domain.Worker{}
	for _, w := range r.workers {
		if status == "" || string(w.Status) == status {
			out = append(out, w)
		}
	}
	return out, nil
}
func (r *workerReadModelWorkerRepo) UpdateStatus(context.Context, string, domain.WorkerStatus) error {
	return nil
}

type workerReadModelServiceSource struct {
	states    []domain.EnvironmentServiceState
	runs      map[uuid.UUID]*domain.DeploymentRun
	intents   map[uuid.UUID]*domain.DeploymentIntent
	artifacts map[uuid.UUID]*domain.Artifact
}

func (s *workerReadModelServiceSource) ListAllStates(context.Context) ([]domain.EnvironmentServiceState, error) {
	return s.states, nil
}
func (s *workerReadModelServiceSource) GetDeploymentRun(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return s.runs[id], nil
}
func (s *workerReadModelServiceSource) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return s.intents[id], nil
}
func (s *workerReadModelServiceSource) GetArtifact(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return s.artifacts[id], nil
}

type workerReadModelMLSource struct {
	states        []domain.MLInferenceState
	runs          map[uuid.UUID]*domain.MLDeploymentRun
	intents       map[uuid.UUID]*domain.MLDeploymentIntent
	modelVersions map[uuid.UUID]*domain.MLModelVersion
	artifactRefs  map[uuid.UUID][]domain.MLArtifactRef
}

func (s *workerReadModelMLSource) ListInferenceStates(context.Context) ([]domain.MLInferenceState, error) {
	return s.states, nil
}
func (s *workerReadModelMLSource) GetMLDeploymentRun(_ context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error) {
	return s.runs[id], nil
}
func (s *workerReadModelMLSource) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	return s.intents[id], nil
}
func (s *workerReadModelMLSource) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	return s.modelVersions[id], nil
}
func (s *workerReadModelMLSource) ListArtifactRefsByModelVersion(_ context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error) {
	return s.artifactRefs[modelVersionID], nil
}

func TestWorkerReadModelServiceBuildsAssignmentAndDrainStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	worker := domain.Worker{PubKey: "worker-a", Name: "worker-a", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingDraining, SchedulingNote: "kernel upgrade", UpdatedAt: now.Add(-time.Hour)}
	svcRunID := uuid.New()
	svcIntentID := uuid.New()
	svcArtifactID := uuid.New()
	svcID := uuid.New()
	envID := uuid.New()
	mlRunID := uuid.New()
	mlIntentID := uuid.New()
	mlModelVersionID := uuid.New()
	mlArtifactID := uuid.New()
	endpointID := uuid.New()
	repo := &workerReadModelWorkerRepo{workers: []domain.Worker{worker}}
	serviceSource := &workerReadModelServiceSource{
		states:    []domain.EnvironmentServiceState{{ServiceID: svcID, EnvironmentID: envID, DesiredArtifactID: &svcArtifactID, LastSuccessfulRunID: &svcRunID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: now}},
		runs:      map[uuid.UUID]*domain.DeploymentRun{svcRunID: {ID: svcRunID, DeploymentIntentID: svcIntentID, WorkerPubkey: "worker-a", WorkerName: "worker-a", Status: domain.RunStatusSucceeded, Metadata: map[string]any{"pinned_worker": "worker-a"}, UpdatedAt: now}},
		intents:   map[uuid.UUID]*domain.DeploymentIntent{svcIntentID: {ID: svcIntentID, ServiceID: svcID, EnvironmentID: envID, ArtifactID: svcArtifactID}},
		artifacts: map[uuid.UUID]*domain.Artifact{svcArtifactID: {ID: svcArtifactID, ImageRepo: "registry.example/bahia/api", ImageTag: "2026-05-22"}},
	}
	mlSource := &workerReadModelMLSource{
		states:        []domain.MLInferenceState{{EndpointID: endpointID, EnvironmentID: envID, ActiveRunID: &mlRunID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: now.Add(time.Minute)}},
		runs:          map[uuid.UUID]*domain.MLDeploymentRun{mlRunID: {ID: mlRunID, DeploymentIntentID: mlIntentID, WorkerPubkey: "worker-a", WorkerName: "worker-a", RuntimeKind: domain.MLRuntimeKindVLLM, Status: domain.RunStatusRunning, UpdatedAt: now.Add(time.Minute)}},
		intents:       map[uuid.UUID]*domain.MLDeploymentIntent{mlIntentID: {ID: mlIntentID, EndpointID: endpointID, EnvironmentID: envID, ModelVersionID: mlModelVersionID}},
		modelVersions: map[uuid.UUID]*domain.MLModelVersion{mlModelVersionID: {ID: mlModelVersionID, ArtifactIDs: []uuid.UUID{mlArtifactID}}},
		artifactRefs:  map[uuid.UUID][]domain.MLArtifactRef{mlModelVersionID: {{ID: mlArtifactID, URI: "oci://models.example/llm:Q4_K_M"}}},
	}

	readModels := NewWorkerReadModelService(repo, serviceSource, mlSource, NewWorkerPolicyService(repo, zap.NewNop()), NewMLPlacementService(repo, zap.NewNop()), zap.NewNop())
	assignmentState, err := readModels.GetAssignmentState(ctx, "worker-a")
	if err != nil {
		t.Fatalf("get assignment state: %v", err)
	}
	if assignmentState == nil || len(assignmentState.ActiveAssignments) != 2 {
		t.Fatalf("expected service and inference assignments, got %#v", assignmentState)
	}
	if assignmentState.ActiveAssignments[0].Type != domain.WorkerAssignmentInference || assignmentState.ActiveAssignments[1].Type != domain.WorkerAssignmentService {
		t.Fatalf("assignments should be newest-first inference/service, got %#v", assignmentState.ActiveAssignments)
	}
	if assignmentState.ActiveAssignments[0].Metadata["artifact_id"] != mlArtifactID.String() || assignmentState.ActiveAssignments[0].Metadata["image_ref"] != "oci://models.example/llm:Q4_K_M" {
		t.Fatalf("inference assignment missing artifact metadata: %#v", assignmentState.ActiveAssignments[0].Metadata)
	}
	if assignmentState.ActiveAssignments[1].Metadata["artifact_id"] != svcArtifactID.String() || assignmentState.ActiveAssignments[1].Metadata["image_ref"] != "registry.example/bahia/api:2026-05-22" {
		t.Fatalf("service assignment missing artifact metadata: %#v", assignmentState.ActiveAssignments[1].Metadata)
	}

	drain, err := readModels.GetDrainStatus(ctx, "worker-a")
	if err != nil {
		t.Fatalf("get drain status: %v", err)
	}
	if drain == nil || drain.DrainStartedAt == nil || drain.Reason != "kernel upgrade" {
		t.Fatalf("unexpected drain metadata: %#v", drain)
	}
	if len(drain.RemainingAssignments) != 2 || len(drain.PinnedBlockers) != 1 || drain.SafeToEnterMaintenance || drain.SafeToDisable {
		t.Fatalf("unexpected drain blockers/safety: %#v", drain)
	}
}

func TestWorkerReadModelServicePreviewsEligibilityWithRankWorkers(t *testing.T) {
	ctx := context.Background()
	repo := &workerReadModelWorkerRepo{workers: []domain.Worker{
		{PubKey: "worker-active", Name: "active", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, CurrentQueueDepth: 0},
		{PubKey: "worker-draining", Name: "draining", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingDraining, CurrentQueueDepth: 0},
	}}
	readModels := NewWorkerReadModelService(repo, nil, nil, NewWorkerPolicyService(repo, zap.NewNop()), nil, zap.NewNop())
	preview, err := readModels.PreviewWorkerPolicyEligibility(ctx, "preview-1", &domain.Environment{RuntimeConfig: map[string]any{"worker_policy": map[string]any{"strategy": "fastest"}}}, nil)
	if err != nil {
		t.Fatalf("preview eligibility: %v", err)
	}
	if preview.PreviewID != "preview-1" || preview.SelectedWinner == nil || preview.SelectedWinner.WorkerPubKey != "worker-active" {
		t.Fatalf("unexpected selected winner: %#v", preview)
	}
	if len(preview.EligibleWorkers) != 1 || len(preview.RejectedWorkers) != 1 || preview.RejectedWorkers[0].WorkerPubKey != "worker-draining" {
		t.Fatalf("unexpected eligibility split: %#v", preview)
	}
}
