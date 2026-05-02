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

// LLMPlacementCandidate is the selected backend/worker target for one release.
type LLMPlacementCandidate struct {
	BackendKind domain.LLMBackendKind `json:"backend_kind"`
	Worker      *domain.Worker        `json:"worker,omitempty"`
	Score       float64               `json:"score"`
	Reason      string                `json:"reason"`
}

// LLMPlacementService evaluates hardware-aware backend placement policy.
type LLMPlacementService struct {
	workerRepo repository.WorkerRepository
	logger     *zap.Logger
}

// NewLLMPlacementService creates an LLM placement evaluator.
func NewLLMPlacementService(workerRepo repository.WorkerRepository, logger *zap.Logger) *LLMPlacementService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &LLMPlacementService{workerRepo: workerRepo, logger: logger}
}

// SelectCandidate returns the best backend candidate for route/release/env.
func (s *LLMPlacementService) SelectCandidate(ctx context.Context, route *domain.LLMRoute, release *domain.LLMRelease, env *domain.Environment) (*LLMPlacementCandidate, error) {
	if release == nil {
		return nil, fmt.Errorf("LLM release is required for placement")
	}
	policy := effectiveLLMPlacementPolicy(route, release, env)
	preferences := backendPreferenceOrder(route, release, policy)
	if len(preferences) > 0 && preferences[0] == domain.LLMBackendKindExternalAPI {
		if externalAllowed(policy, release) {
			return &LLMPlacementCandidate{
				BackendKind: domain.LLMBackendKindExternalAPI,
				Reason:      "external_api selected by policy/release",
			}, nil
		}
		return nil, fmt.Errorf("no compatible LLM placement target found (external_api unavailable: policy disallows external or release lacks external_backend)")
	}

	workers, err := s.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 500)
	if err != nil {
		return nil, fmt.Errorf("listing online workers: %w", err)
	}
	if !hasExplicitBackendPreference(route, release, policy) {
		preferences = hardwareAwareDefaultOrder(workers)
	}
	var reasons []string
	for _, kind := range preferences {
		if kind == domain.LLMBackendKindExternalAPI {
			if externalAllowed(policy, release) {
				return &LLMPlacementCandidate{
					BackendKind: kind,
					Reason:      "external_api selected by policy/release",
				}, nil
			}
			reasons = append(reasons, "external_api unavailable: policy disallows external or release lacks external_backend")
			continue
		}
		candidates, rejected := s.runtimeCandidates(kind, workers, policy, release, env)
		reasons = append(reasons, rejected...)
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Worker.CurrentQueueDepth != candidates[j].Worker.CurrentQueueDepth {
				return candidates[i].Worker.CurrentQueueDepth < candidates[j].Worker.CurrentQueueDepth
			}
			if candidates[i].Score != candidates[j].Score {
				return candidates[i].Score > candidates[j].Score
			}
			return lowestPrice(*candidates[i].Worker) < lowestPrice(*candidates[j].Worker)
		})
		best := candidates[0]
		s.logger.Info("LLM placement selected",
			zap.String("backend_kind", string(best.BackendKind)),
			zap.String("worker", best.Worker.PubKey),
			zap.String("reason", best.Reason),
		)
		return &best, nil
	}
	return nil, fmt.Errorf("no compatible LLM placement target found (%s)", strings.Join(compactReasons(reasons), "; "))
}

func (s *LLMPlacementService) runtimeCandidates(kind domain.LLMBackendKind, workers []domain.Worker, policy domain.LLMPlacementPolicy, release *domain.LLMRelease, env *domain.Environment) ([]LLMPlacementCandidate, []string) {
	var candidates []LLMPlacementCandidate
	var rejected []string
	for i := range workers {
		w := &workers[i]
		if w.RuntimeTarget == nil {
			rejected = append(rejected, fmt.Sprintf("%s rejected for %s: runtime_target missing", w.Name, kind))
			continue
		}
		if strings.TrimSpace(w.RuntimeTarget.PublicBaseURL) == "" {
			rejected = append(rejected, fmt.Sprintf("%s rejected for %s: public_base_url missing", w.Name, kind))
			continue
		}
		if !matchesSelector(*w, selectorFor(policy, env)) {
			rejected = append(rejected, fmt.Sprintf("%s rejected for %s: selector mismatch", w.Name, kind))
			continue
		}
		if policy.MaxPrice > 0 {
			if price := lowestPrice(*w); price > 0 && price > policy.MaxPrice {
				rejected = append(rejected, fmt.Sprintf("%s rejected for %s: price above max", w.Name, kind))
				continue
			}
		}
		if policy.MinSystemMemoryGB > 0 && (w.Resources == nil || w.Resources.MemoryGB < policy.MinSystemMemoryGB) {
			rejected = append(rejected, fmt.Sprintf("%s rejected for %s: system memory below minimum", w.Name, kind))
			continue
		}
		if !gpuCompatible(*w, kind, policy, release) {
			rejected = append(rejected, fmt.Sprintf("%s rejected for %s: GPU thresholds/hardware policy mismatch", w.Name, kind))
			continue
		}
		gpuSurplus := totalGPUMemoryGB(*w) - max(policy.MinGPUMemoryGB, release.EstimatedVRAMGB)
		score := float64(gpuSurplus)
		if hardwarePrefersKind(*w, kind) {
			score += 10000
		}
		candidates = append(candidates, LLMPlacementCandidate{
			BackendKind: kind,
			Worker:      w,
			Score:       score,
			Reason:      placementReason(*w, kind),
		})
	}
	return candidates, rejected
}

func effectiveLLMPlacementPolicy(route *domain.LLMRoute, release *domain.LLMRelease, env *domain.Environment) domain.LLMPlacementPolicy {
	var out domain.LLMPlacementPolicy
	if env != nil && env.RuntimeConfig != nil {
		if raw, ok := env.RuntimeConfig["worker_policy"].(map[string]any); ok {
			mergeLLMPolicy(&out, policyFromMap(raw))
		}
	}
	if route != nil && route.DefaultPlacementPolicy != nil {
		mergeLLMPolicy(&out, *route.DefaultPlacementPolicy)
	}
	if release != nil && release.PlacementPolicy != nil {
		mergeLLMPolicy(&out, *release.PlacementPolicy)
	}
	return out
}

func mergeLLMPolicy(base *domain.LLMPlacementPolicy, override domain.LLMPlacementPolicy) {
	if len(override.PreferredKinds) > 0 {
		base.PreferredKinds = append([]domain.LLMBackendKind(nil), override.PreferredKinds...)
	}
	if len(override.WorkerSelector) > 0 {
		base.WorkerSelector = override.WorkerSelector
	}
	if override.MinGPUCount > 0 {
		base.MinGPUCount = override.MinGPUCount
	}
	if override.MinGPUMemoryGB > 0 {
		base.MinGPUMemoryGB = override.MinGPUMemoryGB
	}
	if override.MinSystemMemoryGB > 0 {
		base.MinSystemMemoryGB = override.MinSystemMemoryGB
	}
	if override.MaxPrice > 0 {
		base.MaxPrice = override.MaxPrice
	}
	base.AllowExternal = base.AllowExternal || override.AllowExternal
}

func policyFromMap(raw map[string]any) domain.LLMPlacementPolicy {
	var p domain.LLMPlacementPolicy
	if v, ok := raw["max_price"]; ok {
		p.MaxPrice = intFromAny(v)
	}
	if v, ok := raw["min_gpu_count"]; ok {
		p.MinGPUCount = intFromAny(v)
	}
	if v, ok := raw["min_gpu_memory_gb"]; ok {
		p.MinGPUMemoryGB = intFromAny(v)
	}
	if v, ok := raw["min_system_memory_gb"]; ok {
		p.MinSystemMemoryGB = intFromAny(v)
	}
	if v, ok := raw["allow_external"].(bool); ok {
		p.AllowExternal = v
	}
	if prefs, ok := raw["preferred_kinds"].([]any); ok {
		for _, item := range prefs {
			if s, ok := item.(string); ok {
				p.PreferredKinds = append(p.PreferredKinds, domain.LLMBackendKind(s))
			}
		}
	}
	if selector, ok := raw["worker_selector"].(map[string]any); ok {
		p.WorkerSelector = selector
	}
	return p
}

func backendPreferenceOrder(route *domain.LLMRoute, release *domain.LLMRelease, policy domain.LLMPlacementPolicy) []domain.LLMBackendKind {
	if release != nil {
		if release.ModelSource == domain.ModelSourceExternal || (release.ExternalBackend != nil && release.RuntimeBackend == nil && len(release.BackendPreferences) == 0) {
			return []domain.LLMBackendKind{domain.LLMBackendKindExternalAPI}
		}
		if len(release.BackendPreferences) > 0 {
			return dedupeKinds(release.BackendPreferences)
		}
	}
	if len(policy.PreferredKinds) > 0 {
		return dedupeKinds(policy.PreferredKinds)
	}
	if route != nil && route.DefaultPlacementPolicy != nil && len(route.DefaultPlacementPolicy.PreferredKinds) > 0 {
		return dedupeKinds(route.DefaultPlacementPolicy.PreferredKinds)
	}
	return []domain.LLMBackendKind{domain.LLMBackendKindVLLM, domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP, domain.LLMBackendKindExternalAPI}
}

func externalAllowed(policy domain.LLMPlacementPolicy, release *domain.LLMRelease) bool {
	if release == nil || release.ExternalBackend == nil {
		return false
	}
	if release.ModelSource == domain.ModelSourceExternal {
		return true
	}
	for _, kind := range release.BackendPreferences {
		if kind == domain.LLMBackendKindExternalAPI {
			return true
		}
	}
	return policy.AllowExternal
}

func gpuCompatible(w domain.Worker, kind domain.LLMBackendKind, policy domain.LLMPlacementPolicy, release *domain.LLMRelease) bool {
	minCount := policy.MinGPUCount
	estimatedVRAM := 0
	if release != nil {
		estimatedVRAM = release.EstimatedVRAMGB
	}
	minMemory := max(policy.MinGPUMemoryGB, estimatedVRAM)
	if kind == domain.LLMBackendKindVLLM && minCount == 0 {
		minCount = 1
	}
	if kind == domain.LLMBackendKindVLLM {
		h := hardwareSignature(w)
		if strings.Contains(h, "p40") && !strings.Contains(h, "l40s") && !strings.Contains(h, "t7920") && !strings.Contains(h, "7920") {
			return false
		}
	}
	if minCount > 0 && totalGPUCount(w) < minCount {
		return false
	}
	if minMemory > 0 && totalGPUMemoryGB(w) < minMemory {
		return false
	}
	return true
}

func hasExplicitBackendPreference(route *domain.LLMRoute, release *domain.LLMRelease, policy domain.LLMPlacementPolicy) bool {
	if release != nil && (len(release.BackendPreferences) > 0 || release.ModelSource == domain.ModelSourceExternal) {
		return true
	}
	if len(policy.PreferredKinds) > 0 {
		return true
	}
	return route != nil && route.DefaultPlacementPolicy != nil && len(route.DefaultPlacementPolicy.PreferredKinds) > 0
}

func hardwareAwareDefaultOrder(workers []domain.Worker) []domain.LLMBackendKind {
	hasL40SClass := false
	hasT7610P40 := false
	for _, w := range workers {
		h := hardwareSignature(w)
		if strings.Contains(h, "l40s") || strings.Contains(h, "t7920") || strings.Contains(h, "7920") {
			hasL40SClass = true
		}
		if (strings.Contains(h, "t7610") || strings.Contains(h, "7610")) && strings.Contains(h, "p40") && totalGPUCount(w) >= 2 {
			hasT7610P40 = true
		}
	}
	if hasL40SClass {
		return []domain.LLMBackendKind{domain.LLMBackendKindVLLM, domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP, domain.LLMBackendKindExternalAPI}
	}
	if hasT7610P40 {
		return []domain.LLMBackendKind{domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP, domain.LLMBackendKindVLLM, domain.LLMBackendKindExternalAPI}
	}
	return []domain.LLMBackendKind{domain.LLMBackendKindVLLM, domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP, domain.LLMBackendKindExternalAPI}
}

func selectorFor(policy domain.LLMPlacementPolicy, env *domain.Environment) map[string]any {
	if len(policy.WorkerSelector) > 0 {
		return policy.WorkerSelector
	}
	if env != nil {
		return env.LoomWorkerSelector
	}
	return nil
}

func hardwarePrefersKind(w domain.Worker, kind domain.LLMBackendKind) bool {
	h := hardwareSignature(w)
	switch kind {
	case domain.LLMBackendKindVLLM:
		return strings.Contains(h, "t7920") || strings.Contains(h, "7920") || strings.Contains(h, "l40s")
	case domain.LLMBackendKindOllama, domain.LLMBackendKindLlamaCPP:
		return (strings.Contains(h, "t7610") || strings.Contains(h, "7610")) && strings.Contains(h, "p40") && totalGPUCount(w) >= 2
	default:
		return false
	}
}

func placementReason(w domain.Worker, kind domain.LLMBackendKind) string {
	if hardwarePrefersKind(w, kind) {
		return fmt.Sprintf("hardware policy prefers %s for worker %s", kind, w.Name)
	}
	return fmt.Sprintf("worker %s satisfies %s backend requirements", w.Name, kind)
}

func hardwareSignature(w domain.Worker) string {
	var parts []string
	parts = append(parts, w.Name, w.Description, w.Architecture)
	for _, a := range w.Accelerators {
		parts = append(parts, a.Vendor, a.Model, a.Driver)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func totalGPUCount(w domain.Worker) int {
	total := 0
	for _, a := range w.Accelerators {
		total += a.Count
	}
	return total
}

func totalGPUMemoryGB(w domain.Worker) int {
	total := 0
	for _, a := range w.Accelerators {
		count := a.Count
		if count <= 0 {
			count = 1
		}
		total += count * a.MemoryGB
	}
	return total
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}

func dedupeKinds(kinds []domain.LLMBackendKind) []domain.LLMBackendKind {
	seen := map[domain.LLMBackendKind]bool{}
	out := make([]domain.LLMBackendKind, 0, len(kinds))
	for _, kind := range kinds {
		if kind == "" || seen[kind] != false {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	return out
}

func compactReasons(reasons []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
		if len(out) >= 8 {
			break
		}
	}
	return out
}
