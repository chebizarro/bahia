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

const backupPlacementAllWorkersLimit = 1<<31 - 1

// BackupPlacementRequest supplies the composed backup definition and resolved backend kind.
type BackupPlacementRequest struct {
	Definition      *domain.BackupDefinition
	BackendKind     domain.BackupBackendKind
	SelectionPolicy domain.BackupExecutorSelectionPolicy
	MaxWorkers      int
}

// BackupPlacementService resolves backup definition executor placement against Bahia workers.
type BackupPlacementService struct {
	workerRepo      repository.WorkerRepository
	backendResolver BackupBackendResolver
	logger          *zap.Logger
}

func NewBackupPlacementService(workerRepo repository.WorkerRepository, backendResolver BackupBackendResolver, logger *zap.Logger) *BackupPlacementService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BackupPlacementService{workerRepo: workerRepo, backendResolver: backendResolver, logger: logger}
}

// ValidatePlacement returns an explainable decision and does not treat ordinary unplaceability as an error.
func (s *BackupPlacementService) ValidatePlacement(ctx context.Context, req BackupPlacementRequest) (*domain.BackupPlacementDecision, error) {
	return s.ResolveExecutors(ctx, req)
}

// ResolveExecutors returns eligible candidates and selected worker pubkeys for the backup definition.
func (s *BackupPlacementService) ResolveExecutors(ctx context.Context, req BackupPlacementRequest) (*domain.BackupPlacementDecision, error) {
	if s == nil || s.workerRepo == nil {
		return nil, fmt.Errorf("backup placement worker repository is required")
	}
	policy, err := effectiveBackupExecutorSelectionPolicy(req)
	if err != nil {
		return nil, err
	}
	decision := &domain.BackupPlacementDecision{SelectionPolicy: policy}
	if req.Definition == nil {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonInvalidDefinition, Message: "backup definition is required"})
		return decision, nil
	}
	definition := *req.Definition
	decision.DefinitionID = definition.ID
	if err := domain.ValidateBackupDefinition(&definition); err != nil {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonInvalidDefinition, Message: err.Error()})
		return decision, nil
	}

	requiredWorkerCapabilities := backupWorkerCapabilityRequirements(definition.CapabilityRequirements)
	backendBlocked := s.appendBackendCapabilityReasons(decision, req.BackendKind, definition.CapabilityRequirements)

	workers, err := s.workerRepo.List(ctx, "", backupPlacementAllWorkersLimit)
	if err != nil {
		return nil, fmt.Errorf("listing workers for backup placement: %w", err)
	}
	if len(workers) == 0 {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonNoWorkers, Message: "no workers are registered for backup placement"})
		return decision, nil
	}

	decision.Candidates = make([]domain.BackupPlacementCandidate, 0, len(workers))
	for i := range workers {
		decision.Candidates = append(decision.Candidates, evaluateBackupPlacementWorker(workers[i], definition.ExecutorLabels, requiredWorkerCapabilities))
	}
	sortBackupPlacementCandidates(decision.Candidates, policy)

	if !backendBlocked {
		decision.SelectedWorkerPubKeys = selectedBackupWorkers(decision.Candidates, policy, req.MaxWorkers)
		decision.Placeable = len(decision.SelectedWorkerPubKeys) > 0
	}
	if !decision.Placeable && len(decision.Reasons) == 0 {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonNoWorkers, Message: "no workers satisfy backup executor targeting requirements"})
	}
	if decision.Placeable {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonPlaceable, Message: "backup definition has at least one eligible executor worker"})
		s.logger.Info("backup placement resolved", zap.String("definition_id", definition.ID.String()), zap.Strings("workers", decision.SelectedWorkerPubKeys), zap.String("policy", string(policy)))
	}
	return decision, nil
}

func effectiveBackupExecutorSelectionPolicy(req BackupPlacementRequest) (domain.BackupExecutorSelectionPolicy, error) {
	if req.SelectionPolicy != "" {
		if !req.SelectionPolicy.IsValid() {
			return "", fmt.Errorf("%w: backup executor selection policy %q is not valid", domain.ErrInvalidValue, req.SelectionPolicy)
		}
		return req.SelectionPolicy, nil
	}
	policy := domain.BackupExecutorSelectionLeastQueued
	if req.Definition != nil && req.Definition.Metadata != nil {
		for _, key := range []string{"executor_selection_policy", "executor_policy", "selection_policy"} {
			if value, ok := req.Definition.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				policy = domain.BackupExecutorSelectionPolicy(strings.TrimSpace(value))
				break
			}
		}
	}
	if !policy.IsValid() {
		return "", fmt.Errorf("%w: backup executor selection policy %q is not valid", domain.ErrInvalidValue, policy)
	}
	return policy, nil
}

func (s *BackupPlacementService) appendBackendCapabilityReasons(decision *domain.BackupPlacementDecision, backendKind domain.BackupBackendKind, requirements []string) bool {
	backendRequirements := backupLifecycleCapabilityRequirements(requirements)
	if len(backendRequirements) == 0 {
		return false
	}
	if s.backendResolver == nil {
		missingStrings := make([]string, 0, len(backendRequirements))
		for _, capability := range backendRequirements {
			missingStrings = append(missingStrings, string(capability))
		}
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonBackendUnsupported, Message: "backup backend resolver is required for lifecycle capability validation", MissingCapabilities: missingStrings, Backend: backendKind})
		return true
	}
	if !backendKind.IsValid() {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonBackendUnsupported, Message: "backup backend kind is required for lifecycle capability validation", Backend: backendKind})
		return true
	}
	capabilities, ok := s.backendResolver.Capabilities(backendKind)
	if !ok {
		decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonBackendUnsupported, Message: fmt.Sprintf("backup backend %q is not registered", backendKind), Backend: backendKind})
		return true
	}
	missing := capabilities.Missing(backendRequirements...)
	if len(missing) == 0 {
		return false
	}
	missingStrings := make([]string, 0, len(missing))
	for _, capability := range missing {
		missingStrings = append(missingStrings, string(capability))
	}
	decision.Reasons = append(decision.Reasons, domain.BackupPlacementReason{Code: domain.BackupPlacementReasonBackendUnsupported, Message: fmt.Sprintf("backup backend %q does not support required capabilities %s", backendKind, strings.Join(missingStrings, ", ")), MissingCapabilities: missingStrings, Backend: backendKind})
	return true
}

func evaluateBackupPlacementWorker(w domain.Worker, executorLabels []string, requirements []string) domain.BackupPlacementCandidate {
	candidate := domain.BackupPlacementCandidate{WorkerPubKey: w.PubKey, WorkerName: w.Name}
	admission := Evaluate(WorkerAdmissionRequest{Scope: AdmissionScopeBackup, Worker: &w})
	if !admission.Eligible {
		code := domain.BackupPlacementReasonWorkerStatus
		if admission.Code == "worker_scheduling" {
			code = domain.BackupPlacementReasonWorkerScheduling
		}
		candidate.Reasons = append(candidate.Reasons, workerPlacementReason(w, code, admission.Reason, nil, nil))
	}
	if missing := missingBackupExecutorLabels(w, executorLabels); len(missing) > 0 {
		candidate.Reasons = append(candidate.Reasons, workerPlacementReason(w, domain.BackupPlacementReasonLabelMismatch, fmt.Sprintf("worker missing executor labels %s", strings.Join(missing, ", ")), missing, nil))
	}
	if missing := missingBackupWorkerCapabilities(w, requirements); len(missing) > 0 {
		candidate.Reasons = append(candidate.Reasons, workerPlacementReason(w, domain.BackupPlacementReasonCapabilityMismatch, fmt.Sprintf("worker missing capabilities %s", strings.Join(missing, ", ")), nil, missing))
	}
	candidate.Eligible = len(candidate.Reasons) == 0
	if candidate.Eligible {
		candidate.Score = backupPlacementScore(w)
		candidate.Reasons = append(candidate.Reasons, workerPlacementReason(w, domain.BackupPlacementReasonPlaceable, fmt.Sprintf("worker %s satisfies backup executor requirements", w.Name), nil, nil))
	}
	return candidate
}

func workerPlacementReason(w domain.Worker, code domain.BackupPlacementReasonCode, message string, missingLabels []string, missingCapabilities []string) domain.BackupPlacementReason {
	return domain.BackupPlacementReason{Code: code, Message: message, WorkerPubKey: w.PubKey, WorkerName: w.Name, MissingLabels: missingLabels, MissingCapabilities: missingCapabilities}
}

func backupPlacementScore(w domain.Worker) float64 {
	return 1000000 - float64(w.CurrentQueueDepth)
}

func sortBackupPlacementCandidates(candidates []domain.BackupPlacementCandidate, policy domain.BackupExecutorSelectionPolicy) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Eligible != candidates[j].Eligible {
			return candidates[i].Eligible
		}
		if !candidates[i].Eligible {
			return candidates[i].WorkerPubKey < candidates[j].WorkerPubKey
		}
		switch policy {
		case domain.BackupExecutorSelectionFirstEligible, domain.BackupExecutorSelectionAllEligible:
			return candidates[i].WorkerPubKey < candidates[j].WorkerPubKey
		default:
			if candidates[i].Score != candidates[j].Score {
				return candidates[i].Score > candidates[j].Score
			}
			return candidates[i].WorkerPubKey < candidates[j].WorkerPubKey
		}
	})
}

func selectedBackupWorkers(candidates []domain.BackupPlacementCandidate, policy domain.BackupExecutorSelectionPolicy, maxWorkers int) []string {
	limit := 1
	if policy == domain.BackupExecutorSelectionAllEligible {
		limit = len(candidates)
	}
	if maxWorkers > 0 && maxWorkers < limit {
		limit = maxWorkers
	}
	selected := make([]string, 0, limit)
	for _, candidate := range candidates {
		if !candidate.Eligible {
			continue
		}
		selected = append(selected, candidate.WorkerPubKey)
		if len(selected) >= limit {
			break
		}
	}
	return selected
}

func backupWorkerCapabilityRequirements(requirements []string) []string {
	out := normalizedUniqueBackupPlacementTokens(requirements)
	if len(out) == 0 {
		return []string{"backup"}
	}
	return out
}

func backupLifecycleCapabilityRequirements(requirements []string) []BackupCapability {
	seen := map[BackupCapability]bool{}
	out := make([]BackupCapability, 0, len(requirements))
	for _, requirement := range requirements {
		capability, ok := parseBackupLifecycleCapability(requirement)
		if ok && !seen[capability] {
			seen[capability] = true
			out = append(out, capability)
		}
	}
	return out
}

func parseBackupLifecycleCapability(requirement string) (BackupCapability, bool) {
	value := normalizeBackupPlacementToken(requirement)
	value = strings.TrimPrefix(value, "backup.")
	switch value {
	case string(BackupCapabilitySnapshotCreate):
		return BackupCapabilitySnapshotCreate, true
	case string(BackupCapabilitySnapshotVerify):
		return BackupCapabilitySnapshotVerify, true
	case string(BackupCapabilityRestore):
		return BackupCapabilityRestore, true
	case string(BackupCapabilityRetention):
		return BackupCapabilityRetention, true
	case string(BackupCapabilityProbe):
		return BackupCapabilityProbe, true
	default:
		return "", false
	}
}

func missingBackupWorkerCapabilities(w domain.Worker, requirements []string) []string {
	if len(requirements) == 0 {
		return nil
	}
	advertised := advertisedBackupWorkerCapabilities(w)
	missing := make([]string, 0)
	for _, requirement := range requirements {
		if !advertised[normalizeBackupPlacementToken(requirement)] {
			missing = append(missing, requirement)
		}
	}
	return missing
}

func advertisedBackupWorkerCapabilities(w domain.Worker) map[string]bool {
	out := map[string]bool{}
	add := func(value string) {
		value = normalizeBackupPlacementToken(value)
		if value != "" {
			out[value] = true
		}
	}
	workloadKinds := normalizedUniqueBackupPlacementTokens(w.Capabilities.WorkloadKinds)
	features := normalizedUniqueBackupPlacementTokens(w.Capabilities.Features)
	for _, value := range workloadKinds {
		add(value)
	}
	for _, value := range features {
		add(value)
		for _, workload := range workloadKinds {
			add(workload + "." + value)
		}
	}
	for _, value := range w.Capabilities.Runtimes {
		add(value)
	}
	for _, value := range w.Capabilities.ArtifactFormats {
		add(value)
	}
	for _, value := range w.Capabilities.Accelerators {
		add(value)
	}
	for _, value := range w.Capabilities.Toolchains {
		add(value)
	}
	for _, sw := range w.Software {
		add(sw.Name)
	}
	return out
}

func missingBackupExecutorLabels(w domain.Worker, labels []string) []string {
	missing := make([]string, 0)
	for _, requirement := range labels {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		if !backupExecutorLabelMatches(w.Labels, requirement) {
			missing = append(missing, requirement)
		}
	}
	return missing
}

func backupExecutorLabelMatches(labels map[string]string, requirement string) bool {
	if len(labels) == 0 {
		return false
	}
	if key, value, ok := splitBackupExecutorLabel(requirement); ok {
		return labels[key] == value
	}
	if value, ok := labels[requirement]; ok {
		return value != "" && value != "false"
	}
	for _, value := range labels {
		if value == requirement {
			return true
		}
	}
	return false
}

func splitBackupExecutorLabel(requirement string) (string, string, bool) {
	for _, sep := range []string{":", "="} {
		if idx := strings.Index(requirement, sep); idx > 0 {
			key := strings.TrimSpace(requirement[:idx])
			value := strings.TrimSpace(requirement[idx+1:])
			return key, value, key != "" && value != ""
		}
	}
	return "", "", false
}

func normalizedUniqueBackupPlacementTokens(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeBackupPlacementToken(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeBackupPlacementToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, ":", ".")
	value = strings.ReplaceAll(value, "/", ".")
	value = strings.Join(strings.Fields(value), "_")
	return value
}
