package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// MLPlacementRequest captures generic AI/ML runtime placement requirements.
type MLPlacementRequest struct {
	TaskKind          domain.MLTaskKind
	RuntimeKind       domain.MLRuntimeKind
	ArtifactFormats   []domain.MLArtifactFormat
	Accelerator       string
	MinVRAMGB         int
	MinSystemMemoryGB int
	Toolchains        []string
	CachedArtifact    string
	WorkerSelector    map[string]any
	MaxPrice          int
	PinnedWorker      string
	LabelSelector     map[string]string
	Rollout           *WorkerPolicyRollout
}

// MLPlacementCandidate describes a worker/runtime target and its placement eligibility.
type MLPlacementCandidate struct {
	RuntimeKind domain.MLRuntimeKind `json:"runtime_kind"`
	Worker      *domain.Worker       `json:"worker"`
	Score       float64              `json:"score"`
	Reason      string               `json:"reason"`
	Eligible    bool                 `json:"eligible"`
}

// MLPlacementService filters and scores normalized worker AI/ML capabilities.
type MLPlacementService struct {
	workerRepo repository.WorkerRepository
	logger     *zap.Logger
}

func NewMLPlacementService(workerRepo repository.WorkerRepository, logger *zap.Logger) *MLPlacementService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &MLPlacementService{workerRepo: workerRepo, logger: logger}
}

func (s *MLPlacementService) SelectCandidate(ctx context.Context, req MLPlacementRequest) (*MLPlacementCandidate, error) {
	candidates, err := s.PreviewCandidates(ctx, req)
	if err != nil {
		return nil, err
	}

	var rejected []string
	for i := range candidates {
		if candidates[i].Eligible {
			best := candidates[i]
			s.logger.Info("ML placement selected", zap.String("runtime", string(best.RuntimeKind)), zap.String("worker", best.Worker.PubKey), zap.String("reason", best.Reason))
			return &best, nil
		}
		rejected = append(rejected, candidates[i].Reason)
	}

	return nil, fmt.Errorf("no compatible ML placement target found (%s)", strings.Join(compactReasons(rejected), "; "))
}

// PreviewCandidates returns eligible and rejected ML placement candidates with
// operator-visible reasons. New placements must only use candidates marked
// Eligible; rejected entries are retained for preview/read-model projection.
func (s *MLPlacementService) PreviewCandidates(ctx context.Context, req MLPlacementRequest) ([]MLPlacementCandidate, error) {
	if err := validateMLPlacementRequest(req); err != nil {
		return nil, err
	}

	workers, err := s.listMLWorkersForRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	candidates := make([]MLPlacementCandidate, 0, len(workers))
	for i := range workers {
		w := &workers[i]
		candidate, reason, ok := scoreMLWorker(w, req)
		if !ok {
			candidates = append(candidates, MLPlacementCandidate{RuntimeKind: req.RuntimeKind, Worker: w, Score: 0, Reason: reason, Eligible: false})
			continue
		}
		candidates = append(candidates, candidate)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Eligible != candidates[j].Eligible {
			return candidates[i].Eligible
		}
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Worker.CurrentQueueDepth != candidates[j].Worker.CurrentQueueDepth {
			return candidates[i].Worker.CurrentQueueDepth < candidates[j].Worker.CurrentQueueDepth
		}
		if lowestPrice(*candidates[i].Worker) != lowestPrice(*candidates[j].Worker) {
			return lowestPrice(*candidates[i].Worker) < lowestPrice(*candidates[j].Worker)
		}
		return candidates[i].Worker.PubKey < candidates[j].Worker.PubKey
	})

	return candidates, nil
}

func (s *MLPlacementService) listMLWorkersForRequest(ctx context.Context, req MLPlacementRequest) ([]domain.Worker, error) {
	workers, err := s.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 500)
	if err != nil {
		return nil, fmt.Errorf("listing online workers: %w", err)
	}
	if req.PinnedWorker == "" {
		return workers, nil
	}
	for _, worker := range workers {
		if worker.PubKey == req.PinnedWorker {
			return workers, nil
		}
	}
	worker, err := s.workerRepo.GetByPubKey(ctx, req.PinnedWorker)
	if err != nil {
		return nil, fmt.Errorf("looking up pinned worker: %w", err)
	}
	if worker != nil {
		workers = append(workers, *worker)
	}
	return workers, nil
}

func validateMLPlacementRequest(req MLPlacementRequest) error {
	if req.RuntimeKind == "" || !req.RuntimeKind.IsValid() {
		return fmt.Errorf("unsupported or missing ML runtime %q", req.RuntimeKind)
	}
	if req.TaskKind != "" && !req.TaskKind.IsValid() {
		return fmt.Errorf("unsupported ML task %q", req.TaskKind)
	}
	for _, format := range req.ArtifactFormats {
		if !format.IsValid() {
			return fmt.Errorf("unsupported ML artifact format %q", format)
		}
	}
	return nil
}

func scoreMLWorker(w *domain.Worker, req MLPlacementRequest) (MLPlacementCandidate, string, bool) {
	if w.Status != domain.WorkerStatusOnline {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: worker status %s is not online", w.Name, w.Status), false
	}
	if req.PinnedWorker != "" && w.PubKey != req.PinnedWorker {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: worker does not match pinned_worker %s", w.Name, req.PinnedWorker), false
	}
	if !workerSchedulingStateAllowsNewPlacement(w.SchedulingState) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: %s", w.Name, workerSchedulingStateRejectionReason(w.SchedulingState)), false
	}
	if w.RuntimeTarget == nil || strings.TrimSpace(w.RuntimeTarget.PublicBaseURL) == "" {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: runtime target missing", w.Name), false
	}
	if !matchesSelector(*w, req.WorkerSelector) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: selector mismatch", w.Name), false
	}
	if reason, ok := workerLabelsMatchReason(*w, requiredMLPlacementLabels(req)); !ok {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: %s", w.Name, reason), false
	}
	if req.MaxPrice > 0 {
		if price := lowestPrice(*w); price > 0 && price > req.MaxPrice {
			return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: price above max", w.Name), false
		}
	}
	caps := domain.NormalizeWorkerMLCapabilities(*w)
	if !containsRuntime(caps.Runtimes, req.RuntimeKind) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: runtime %s not advertised", w.Name, req.RuntimeKind), false
	}
	if req.TaskKind != "" && !containsTask(caps.Tasks, req.TaskKind) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: task %s not advertised", w.Name, req.TaskKind), false
	}
	for _, format := range req.ArtifactFormats {
		if !containsFormat(caps.ArtifactFormats, format) {
			return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: artifact format %s not advertised", w.Name, format), false
		}
	}
	if req.Accelerator != "" && !containsNormalizedString(caps.Accelerators, req.Accelerator) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: accelerator %s not advertised", w.Name, req.Accelerator), false
	}
	for _, toolchain := range req.Toolchains {
		if !containsNormalizedString(caps.Toolchains, toolchain) {
			return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: toolchain %s not advertised", w.Name, toolchain), false
		}
	}
	if req.MinSystemMemoryGB > 0 && (w.Resources == nil || w.Resources.MemoryGB < req.MinSystemMemoryGB) {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: system memory below minimum", w.Name), false
	}
	if req.MinVRAMGB > 0 && totalGPUMemoryGB(*w) < req.MinVRAMGB {
		return MLPlacementCandidate{}, fmt.Sprintf("%s rejected: VRAM below minimum", w.Name), false
	}

	score := float64(totalGPUMemoryGB(*w) - req.MinVRAMGB)
	if req.RuntimeKind == domain.MLRuntimeKindVLLM && containsNormalizedString(caps.Accelerators, "gpu_nvidia_cuda") {
		score += 10000
	}
	if req.CachedArtifact != "" && containsNormalizedString(caps.CachedArtifacts, req.CachedArtifact) {
		score += 1000
	}
	return MLPlacementCandidate{RuntimeKind: req.RuntimeKind, Worker: w, Score: score, Reason: fmt.Sprintf("worker %s satisfies ML placement requirements", w.Name), Eligible: true}, "", true
}

func requiredMLPlacementLabels(req MLPlacementRequest) map[string]string {
	policy := WorkerPolicy{LabelSelector: req.LabelSelector, Rollout: req.Rollout}
	return requiredWorkerPolicyLabels(policy)
}

func containsRuntime(values []domain.MLRuntimeKind, want domain.MLRuntimeKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsTask(values []domain.MLTaskKind, want domain.MLTaskKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsFormat(values []domain.MLArtifactFormat, want domain.MLArtifactFormat) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsNormalizedString(values []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}
