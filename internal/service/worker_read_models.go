package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type WorkerServiceAssignmentSource interface {
	ListAllStates(ctx context.Context) ([]domain.EnvironmentServiceState, error)
	GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.DeploymentRun, error)
}

type WorkerInferenceAssignmentSource interface {
	ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error)
	GetMLDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error)
}

// WorkerReadModelService assembles operator worker read models from authoritative repositories.
type WorkerReadModelService struct {
	workers      repository.WorkerRepository
	services     WorkerServiceAssignmentSource
	ml           WorkerInferenceAssignmentSource
	workerPolicy *WorkerPolicyService
	mlPlacement  *MLPlacementService
	logger       *zap.Logger
}

func NewWorkerReadModelService(workers repository.WorkerRepository, services WorkerServiceAssignmentSource, ml WorkerInferenceAssignmentSource, workerPolicy *WorkerPolicyService, mlPlacement *MLPlacementService, logger *zap.Logger) *WorkerReadModelService {
	if logger == nil {
		logger = zap.NewNop()
	}
	if workerPolicy == nil && workers != nil {
		workerPolicy = NewWorkerPolicyService(workers, logger)
	}
	if mlPlacement == nil && workers != nil {
		mlPlacement = NewMLPlacementService(workers, logger)
	}
	return &WorkerReadModelService{workers: workers, services: services, ml: ml, workerPolicy: workerPolicy, mlPlacement: mlPlacement, logger: logger}
}

func (s *WorkerReadModelService) GetAssignmentState(ctx context.Context, workerPubKey string) (*domain.WorkerAssignmentState, error) {
	if s == nil || s.workers == nil {
		return nil, fmt.Errorf("worker read model service is not configured")
	}
	workerPubKey = strings.TrimSpace(workerPubKey)
	if workerPubKey == "" {
		return nil, fmt.Errorf("worker_pubkey is required")
	}
	worker, err := s.workers.GetByPubKey(ctx, workerPubKey)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, nil
	}
	assignments, err := s.assignmentsForWorker(ctx, workerPubKey)
	if err != nil {
		return nil, err
	}
	updatedAt := worker.UpdatedAt
	for _, assignment := range assignments {
		if assignment.UpdatedAt.After(updatedAt) {
			updatedAt = assignment.UpdatedAt
		}
	}
	return &domain.WorkerAssignmentState{WorkerPubKey: workerPubKey, ActiveAssignments: assignments, UpdatedAt: updatedAt}, nil
}

func (s *WorkerReadModelService) ListAssignmentStates(ctx context.Context) ([]domain.WorkerAssignmentState, error) {
	if s == nil || s.workers == nil {
		return nil, fmt.Errorf("worker read model service is not configured")
	}
	workers, err := s.workers.List(ctx, "", 1000)
	if err != nil {
		return nil, err
	}
	byWorker, err := s.assignmentsByWorker(ctx)
	if err != nil {
		return nil, err
	}
	states := make([]domain.WorkerAssignmentState, 0, len(workers))
	for i := range workers {
		assignments := byWorker[workers[i].PubKey]
		updatedAt := workers[i].UpdatedAt
		for _, assignment := range assignments {
			if assignment.UpdatedAt.After(updatedAt) {
				updatedAt = assignment.UpdatedAt
			}
		}
		states = append(states, domain.WorkerAssignmentState{WorkerPubKey: workers[i].PubKey, ActiveAssignments: assignments, UpdatedAt: updatedAt})
	}
	return states, nil
}

func (s *WorkerReadModelService) GetDrainStatus(ctx context.Context, workerPubKey string) (*domain.WorkerDrainStatus, error) {
	if s == nil || s.workers == nil {
		return nil, fmt.Errorf("worker read model service is not configured")
	}
	workerPubKey = strings.TrimSpace(workerPubKey)
	if workerPubKey == "" {
		return nil, fmt.Errorf("worker_pubkey is required")
	}
	worker, err := s.workers.GetByPubKey(ctx, workerPubKey)
	if err != nil {
		return nil, err
	}
	if worker == nil {
		return nil, nil
	}
	assignments, err := s.assignmentsForWorker(ctx, workerPubKey)
	if err != nil {
		return nil, err
	}
	return drainStatusForWorker(worker, assignments), nil
}

func (s *WorkerReadModelService) ListDrainStatuses(ctx context.Context) ([]domain.WorkerDrainStatus, error) {
	if s == nil || s.workers == nil {
		return nil, fmt.Errorf("worker read model service is not configured")
	}
	workers, err := s.workers.List(ctx, "", 1000)
	if err != nil {
		return nil, err
	}
	byWorker, err := s.assignmentsByWorker(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]domain.WorkerDrainStatus, 0, len(workers))
	for i := range workers {
		status := drainStatusForWorker(&workers[i], byWorker[workers[i].PubKey])
		statuses = append(statuses, *status)
	}
	return statuses, nil
}

func (s *WorkerReadModelService) PreviewWorkerPolicyEligibility(ctx context.Context, previewID string, env *domain.Environment, policy map[string]any) (*domain.WorkerEligibilityPreview, error) {
	if s == nil || s.workerPolicy == nil {
		return nil, fmt.Errorf("worker policy preview service is not configured")
	}
	if env == nil {
		env = &domain.Environment{ID: uuid.New(), RuntimeConfig: map[string]any{}}
	}
	if env.RuntimeConfig == nil {
		env.RuntimeConfig = map[string]any{}
	}
	if policy != nil {
		env.RuntimeConfig["worker_policy"] = policy
	}
	ranked, err := s.workerPolicy.RankWorkers(ctx, env)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.WorkerEligibilityCandidate, 0, len(ranked))
	for _, item := range ranked {
		candidates = append(candidates, domain.WorkerEligibilityCandidate{WorkerPubKey: item.Worker.PubKey, WorkerName: item.Worker.Name, Eligible: item.Eligible, Score: item.Score, Reason: item.Reason})
	}
	return eligibilityPreview(previewID, "worker_policy", policy, candidates), nil
}

func (s *WorkerReadModelService) PreviewMLEligibility(ctx context.Context, previewID string, req MLPlacementRequest, policy map[string]any) (*domain.WorkerEligibilityPreview, error) {
	if s == nil || s.mlPlacement == nil {
		return nil, fmt.Errorf("ML placement preview service is not configured")
	}
	candidates, err := s.mlPlacement.PreviewCandidates(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]domain.WorkerEligibilityCandidate, 0, len(candidates))
	for _, item := range candidates {
		if item.Worker == nil {
			continue
		}
		out = append(out, domain.WorkerEligibilityCandidate{WorkerPubKey: item.Worker.PubKey, WorkerName: item.Worker.Name, Eligible: item.Eligible, Score: item.Score, Reason: item.Reason})
	}
	return eligibilityPreview(previewID, "inference", policy, out), nil
}

func (s *WorkerReadModelService) assignmentsForWorker(ctx context.Context, workerPubKey string) ([]domain.WorkerAssignment, error) {
	byWorker, err := s.assignmentsByWorker(ctx)
	if err != nil {
		return nil, err
	}
	return byWorker[workerPubKey], nil
}

func (s *WorkerReadModelService) assignmentsByWorker(ctx context.Context) (map[string][]domain.WorkerAssignment, error) {
	byWorker := map[string][]domain.WorkerAssignment{}
	if s.services != nil {
		states, err := s.services.ListAllStates(ctx)
		if err != nil {
			return nil, fmt.Errorf("list service states for worker assignments: %w", err)
		}
		for i := range states {
			state := states[i]
			if state.LastSuccessfulRunID == nil {
				continue
			}
			run, err := s.services.GetDeploymentRun(ctx, *state.LastSuccessfulRunID)
			if err != nil {
				return nil, fmt.Errorf("read service run %s for worker assignments: %w", state.LastSuccessfulRunID.String(), err)
			}
			if run == nil || strings.TrimSpace(run.WorkerPubkey) == "" {
				continue
			}
			assignment := domain.WorkerAssignment{Type: domain.WorkerAssignmentService, WorkloadID: state.ServiceID.String() + ":" + state.EnvironmentID.String(), Status: string(state.DriftStatus), Pinned: assignmentPinned(run.Metadata, run.WorkerPubkey), Movable: !assignmentPinned(run.Metadata, run.WorkerPubkey), StartedAt: run.StartedAt, UpdatedAt: maxTime(state.UpdatedAt, run.UpdatedAt), Metadata: compactAssignmentMetadata(map[string]any{"service_id": state.ServiceID.String(), "environment_id": state.EnvironmentID.String(), "run_id": run.ID.String(), "intent_id": run.DeploymentIntentID.String(), "worker_name": run.WorkerName})}
			byWorker[run.WorkerPubkey] = append(byWorker[run.WorkerPubkey], assignment)
		}
	}
	if s.ml != nil {
		states, err := s.ml.ListInferenceStates(ctx)
		if err != nil {
			return nil, fmt.Errorf("list inference states for worker assignments: %w", err)
		}
		for i := range states {
			state := states[i]
			if state.ActiveRunID == nil {
				continue
			}
			run, err := s.ml.GetMLDeploymentRun(ctx, *state.ActiveRunID)
			if err != nil {
				return nil, fmt.Errorf("read inference run %s for worker assignments: %w", state.ActiveRunID.String(), err)
			}
			if run == nil || strings.TrimSpace(run.WorkerPubkey) == "" {
				continue
			}
			assignment := domain.WorkerAssignment{Type: domain.WorkerAssignmentInference, WorkloadID: state.EndpointID.String() + ":" + state.EnvironmentID.String(), Status: string(state.DriftStatus), Pinned: assignmentPinned(run.Metadata, run.WorkerPubkey), Movable: !assignmentPinned(run.Metadata, run.WorkerPubkey), StartedAt: run.StartedAt, UpdatedAt: maxTime(state.UpdatedAt, run.UpdatedAt), Metadata: compactAssignmentMetadata(map[string]any{"endpoint_id": state.EndpointID.String(), "environment_id": state.EnvironmentID.String(), "run_id": run.ID.String(), "intent_id": run.DeploymentIntentID.String(), "runtime_kind": string(run.RuntimeKind), "worker_name": run.WorkerName})}
			byWorker[run.WorkerPubkey] = append(byWorker[run.WorkerPubkey], assignment)
		}
	}
	for worker := range byWorker {
		sort.SliceStable(byWorker[worker], func(i, j int) bool {
			return byWorker[worker][i].UpdatedAt.After(byWorker[worker][j].UpdatedAt)
		})
	}
	return byWorker, nil
}

func drainStatusForWorker(worker *domain.Worker, assignments []domain.WorkerAssignment) *domain.WorkerDrainStatus {
	updatedAt := worker.UpdatedAt
	var startedAt *time.Time
	if worker.SchedulingState == domain.WorkerSchedulingDraining {
		started := worker.UpdatedAt
		startedAt = &started
	}
	pinned := make([]domain.WorkerAssignment, 0)
	for _, assignment := range assignments {
		if assignment.UpdatedAt.After(updatedAt) {
			updatedAt = assignment.UpdatedAt
		}
		if assignment.Pinned || !assignment.Movable {
			pinned = append(pinned, assignment)
		}
	}
	return &domain.WorkerDrainStatus{WorkerPubKey: worker.PubKey, DrainStartedAt: startedAt, Reason: worker.SchedulingNote, RemainingAssignments: assignments, PinnedBlockers: pinned, SafeToEnterMaintenance: len(assignments) == 0, SafeToDisable: len(assignments) == 0, SchedulingState: worker.SchedulingState, UpdatedAt: updatedAt}
}

func eligibilityPreview(previewID, workloadType string, policy map[string]any, candidates []domain.WorkerEligibilityCandidate) *domain.WorkerEligibilityPreview {
	if strings.TrimSpace(previewID) == "" {
		previewID = deterministicPreviewID(workloadType, policy)
	}
	eligible := make([]domain.WorkerEligibilityCandidate, 0)
	rejected := make([]domain.WorkerEligibilityCandidate, 0)
	var selected *domain.WorkerEligibilityCandidate
	for i := range candidates {
		candidate := candidates[i]
		if candidate.Eligible {
			eligible = append(eligible, candidate)
			if selected == nil {
				copy := candidate
				selected = &copy
			}
		} else {
			rejected = append(rejected, candidate)
		}
	}
	return &domain.WorkerEligibilityPreview{PreviewID: previewID, WorkloadType: workloadType, Policy: policy, EligibleWorkers: eligible, RejectedWorkers: rejected, RankingScores: candidates, SelectedWinner: selected, UpdatedAt: time.Now().UTC()}
}

func deterministicPreviewID(workloadType string, policy map[string]any) string {
	payload, _ := json.Marshal(map[string]any{"workload_type": workloadType, "policy": policy})
	sum := sha256.Sum256(payload)
	return "worker-preview:" + hex.EncodeToString(sum[:12])
}

func assignmentPinned(metadata map[string]any, workerPubKey string) bool {
	if metadata == nil {
		return false
	}
	if pinned, ok := metadata["pinned"].(bool); ok && pinned {
		return true
	}
	if pinnedWorker, ok := metadata["pinned_worker"].(string); ok && strings.TrimSpace(pinnedWorker) == workerPubKey {
		return true
	}
	return false
}

func compactAssignmentMetadata(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if fmt.Sprint(value) != "" {
			out[key] = value
		}
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
