// Package service provides domain services.
package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// WorkerSelectionStrategy identifies a worker ranking algorithm.
type WorkerSelectionStrategy string

const (
	// StrategyCheapest selects the worker with the lowest price per second.
	StrategyCheapest WorkerSelectionStrategy = "cheapest"
	// StrategyFastest selects the worker with the lowest queue depth.
	StrategyFastest WorkerSelectionStrategy = "fastest"
	// StrategyNearest selects the worker closest by geohash proximity.
	StrategyNearest WorkerSelectionStrategy = "nearest"
	// StrategyPreferred selects workers matching the environment's loom_worker_selector.
	StrategyPreferred WorkerSelectionStrategy = "preferred"
	// StrategyReputation selects workers by reputation (future Web of Trust). Currently a no-op.
	StrategyReputation WorkerSelectionStrategy = "reputation"
)

// WorkerPolicy configures worker selection for an environment.
// Stored in Environment.RuntimeConfig under the "worker_policy" key.
type WorkerPolicy struct {
	Strategy        WorkerSelectionStrategy `json:"strategy"`
	Geohash         string                  `json:"geohash,omitempty"`          // reference geohash for "nearest"
	MaxPrice        int                     `json:"max_price,omitempty"`        // max price/sec in sats (0 = no limit)
	RequireSoftware []string                `json:"require_software,omitempty"` // required software names
	ExcludeWorkers  []string                `json:"exclude_workers,omitempty"`  // pubkeys to never select
	MaxQueueDepth   int                     `json:"max_queue_depth,omitempty"`  // skip workers above this queue depth

	// Reputation settings
	MinSuccessRate  float64 `json:"min_success_rate,omitempty"`  // minimum success rate (0-1), default 0
	MinJobsRequired int     `json:"min_jobs_required,omitempty"` // minimum jobs before reputation counts, default 5
}

// ScoredWorker pairs a worker with a selection score.
type ScoredWorker struct {
	Worker   domain.Worker `json:"worker"`
	Score    float64       `json:"score"`
	Reason   string        `json:"reason"`
	Eligible bool          `json:"eligible"`
}

// WorkerPolicyService selects workers based on environment-specific policies.
type WorkerPolicyService struct {
	workerRepo repository.WorkerRepository
	jobStats   *JobStatsTracker
	logger     *zap.Logger
}

// NewWorkerPolicyService creates a new WorkerPolicyService.
func NewWorkerPolicyService(workerRepo repository.WorkerRepository, logger *zap.Logger) *WorkerPolicyService {
	return &WorkerPolicyService{
		workerRepo: workerRepo,
		logger:     logger,
	}
}

// SetJobStatsTracker sets the job stats tracker for reputation scoring.
func (s *WorkerPolicyService) SetJobStatsTracker(tracker *JobStatsTracker) {
	s.jobStats = tracker
}

// SelectWorker picks the best worker for the given environment using the configured policy.
// It fetches online workers, applies filters, scores them, and returns the best match.
func (s *WorkerPolicyService) SelectWorker(ctx context.Context, env *domain.Environment) (*ScoredWorker, error) {
	policy := s.extractPolicy(env)

	workers, err := s.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 100)
	if err != nil {
		return nil, fmt.Errorf("listing online workers: %w", err)
	}
	if len(workers) == 0 {
		return nil, fmt.Errorf("no online workers available")
	}

	// Apply hard scheduling and policy filters.
	filtered := s.filterWorkers(workers, policy, env)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no workers match the selection policy (strategy=%s, %d online workers filtered out)",
			policy.Strategy, len(workers))
	}

	// Score workers.
	scored := s.scoreWorkers(filtered, policy)

	// Sort by score descending (highest = best).
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	best := scored[0]
	s.logger.Info("worker selected",
		zap.String("strategy", string(policy.Strategy)),
		zap.String("pubkey", best.Worker.PubKey),
		zap.String("name", best.Worker.Name),
		zap.Float64("score", best.Score),
		zap.String("reason", best.Reason),
		zap.Int("candidates", len(scored)),
	)

	return &best, nil
}

// RankWorkers returns all online workers for a given environment, sorted best-first.
// Eligible workers are scored normally. Ineligible workers are retained with
// exclusion reasons so preview/read-model callers can explain why a worker will
// not receive new placements.
func (s *WorkerPolicyService) RankWorkers(ctx context.Context, env *domain.Environment) ([]ScoredWorker, error) {
	policy := s.extractPolicy(env)

	workers, err := s.workerRepo.List(ctx, string(domain.WorkerStatusOnline), 100)
	if err != nil {
		return nil, fmt.Errorf("listing online workers: %w", err)
	}

	var eligibleWorkers []domain.Worker
	ranked := make([]ScoredWorker, 0, len(workers))
	for _, w := range workers {
		if reason, ok := s.workerEligibilityRejectionReason(w, policy, env); !ok {
			ranked = append(ranked, ScoredWorker{Worker: w, Score: 0, Reason: reason, Eligible: false})
			continue
		}
		eligibleWorkers = append(eligibleWorkers, w)
	}

	ranked = append(ranked, s.scoreWorkers(eligibleWorkers, policy)...)

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Eligible != ranked[j].Eligible {
			return ranked[i].Eligible
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Worker.PubKey < ranked[j].Worker.PubKey
	})

	return ranked, nil
}

// extractPolicy reads the WorkerPolicy from environment.RuntimeConfig["worker_policy"].
func (s *WorkerPolicyService) extractPolicy(env *domain.Environment) WorkerPolicy {
	policy := WorkerPolicy{Strategy: StrategyCheapest} // default

	if env == nil || env.RuntimeConfig == nil {
		return policy
	}

	wpRaw, ok := env.RuntimeConfig["worker_policy"]
	if !ok {
		return policy
	}

	// The JSONB value comes through as map[string]any after JSON unmarshaling.
	wpMap, ok := wpRaw.(map[string]any)
	if !ok {
		return policy
	}

	if strat, ok := wpMap["strategy"].(string); ok && strat != "" {
		policy.Strategy = WorkerSelectionStrategy(strat)
	}
	if geo, ok := wpMap["geohash"].(string); ok {
		policy.Geohash = geo
	}
	if mp, ok := wpMap["max_price"].(float64); ok {
		policy.MaxPrice = int(mp)
	}
	if mqd, ok := wpMap["max_queue_depth"].(float64); ok {
		policy.MaxQueueDepth = int(mqd)
	}
	if rs, ok := wpMap["require_software"].([]any); ok {
		for _, item := range rs {
			if s, ok := item.(string); ok {
				policy.RequireSoftware = append(policy.RequireSoftware, s)
			}
		}
	}
	if ew, ok := wpMap["exclude_workers"].([]any); ok {
		for _, item := range ew {
			if s, ok := item.(string); ok {
				policy.ExcludeWorkers = append(policy.ExcludeWorkers, s)
			}
		}
	}

	// Reputation settings
	if msr, ok := wpMap["min_success_rate"].(float64); ok {
		policy.MinSuccessRate = msr
	}
	if mjr, ok := wpMap["min_jobs_required"].(float64); ok {
		policy.MinJobsRequired = int(mjr)
	}

	return policy
}

// filterWorkers removes workers that don't meet hard constraints.
func (s *WorkerPolicyService) filterWorkers(workers []domain.Worker, policy WorkerPolicy, env *domain.Environment) []domain.Worker {
	var result []domain.Worker
	for _, w := range workers {
		if _, ok := s.workerEligibilityRejectionReason(w, policy, env); !ok {
			continue
		}
		result = append(result, w)
	}
	return result
}

func (s *WorkerPolicyService) workerEligibilityRejectionReason(w domain.Worker, policy WorkerPolicy, env *domain.Environment) (string, bool) {
	if !workerSchedulingStateAllowsNewPlacement(w.SchedulingState) {
		return workerSchedulingStateRejectionReason(w.SchedulingState), false
	}

	for _, pk := range policy.ExcludeWorkers {
		if pk == w.PubKey {
			return "worker excluded by policy", false
		}
	}

	if policy.MaxQueueDepth > 0 && w.CurrentQueueDepth > policy.MaxQueueDepth {
		return fmt.Sprintf("worker queue depth %d exceeds max %d", w.CurrentQueueDepth, policy.MaxQueueDepth), false
	}

	if policy.MaxPrice > 0 && !s.workerUnderPrice(w, policy.MaxPrice) {
		return fmt.Sprintf("worker price %d exceeds max %d", lowestPrice(w), policy.MaxPrice), false
	}

	for _, req := range policy.RequireSoftware {
		if !w.HasSoftware(req) {
			return fmt.Sprintf("worker missing required software %s", req), false
		}
	}

	if policy.Strategy == StrategyPreferred && env != nil && !matchesSelector(w, env.LoomWorkerSelector) {
		return "worker does not match selector", false
	}

	return "", true
}

// scoreWorkers assigns a score to each worker based on the strategy.
func (s *WorkerPolicyService) scoreWorkers(workers []domain.Worker, policy WorkerPolicy) []ScoredWorker {
	scored := make([]ScoredWorker, len(workers))

	for i, w := range workers {
		var score float64
		var reason string

		switch policy.Strategy {
		case StrategyCheapest:
			score, reason = scoreCheapest(w)
		case StrategyFastest:
			score, reason = scoreFastest(w)
		case StrategyNearest:
			score, reason = scoreNearest(w, policy.Geohash)
		case StrategyPreferred:
			score, reason = scorePreferred(w)
		case StrategyReputation:
			score, reason = s.scoreReputation(w, policy)
		default:
			score, reason = scoreCheapest(w)
		}

		scored[i] = ScoredWorker{
			Worker:   w,
			Score:    score,
			Reason:   reason,
			Eligible: true,
		}
	}

	return scored
}

// scoreCheapest scores inversely proportional to price. Lower price = higher score.
func scoreCheapest(w domain.Worker) (float64, string) {
	price := lowestPrice(w)
	if price <= 0 {
		// No pricing info — score as free (best possible).
		return 1000.0, "no pricing (treated as free)"
	}
	// Score = 1000 / price — higher score for lower price.
	score := 1000.0 / float64(price)
	return score, fmt.Sprintf("price=%d sat/sec", price)
}

// scoreFastest scores inversely proportional to queue depth. Lower queue = higher score.
func scoreFastest(w domain.Worker) (float64, string) {
	depth := w.CurrentQueueDepth
	// Score = 100 / (1 + depth) — empty queue gets highest score.
	score := 100.0 / float64(1+depth)

	// Bonus for higher max concurrency (can handle more).
	if w.MaxConcurrentJobs > 1 {
		utilization := float64(depth) / float64(w.MaxConcurrentJobs)
		// Less utilized = bonus.
		score += 10.0 * (1.0 - utilization)
	}

	return score, fmt.Sprintf("queue=%d/%d", depth, w.MaxConcurrentJobs)
}

// scoreNearest scores by geohash prefix match. Longer shared prefix = closer = higher score.
func scoreNearest(w domain.Worker, referenceGeohash string) (float64, string) {
	if referenceGeohash == "" || w.Geohash == "" {
		return 0, "no geohash available"
	}

	shared := geohashCommonPrefix(w.Geohash, referenceGeohash)
	// Each geohash character narrows the area significantly.
	// Score = shared^2 to favor closer workers exponentially.
	score := float64(shared * shared)
	return score, fmt.Sprintf("geohash=%s, shared_prefix=%d", w.Geohash, shared)
}

// scorePreferred scores workers that matched the selector (already filtered).
// Workers that passed filtering get a base score; boost by freshness.
func scorePreferred(w domain.Worker) (float64, string) {
	// Base score for matching selector + recency bonus.
	score := 100.0
	// Bonus for lower queue depth.
	if w.CurrentQueueDepth == 0 {
		score += 50.0
	} else {
		score += 50.0 / float64(1+w.CurrentQueueDepth)
	}
	return score, "matches selector"
}

// scoreReputation scores workers based on their job history and performance.
// Score components:
//   - Success rate (0-100): 50% weight
//   - Response time (relative to peers): 25% weight
//   - Worker experience (job count): 15% weight
//   - Availability (low queue depth): 10% weight
func (s *WorkerPolicyService) scoreReputation(w domain.Worker, policy WorkerPolicy) (float64, string) {
	if s.jobStats == nil {
		// No stats tracker configured - fall back to neutral score
		return 50.0, "no job stats available"
	}

	stats := s.jobStats.GetStats(w.PubKey)
	totalJobs := stats.TotalCompleted + stats.TotalFailed

	// Minimum jobs threshold for meaningful reputation
	minJobs := policy.MinJobsRequired
	if minJobs <= 0 {
		minJobs = 5 // default
	}

	// New workers with insufficient history get a bootstrap score
	if totalJobs < int64(minJobs) {
		// Give new workers a chance with a moderate score
		// Slightly lower than established workers to prefer known quantities
		return 45.0, fmt.Sprintf("new worker (jobs=%d, need %d for full reputation)", totalJobs, minJobs)
	}

	var score float64
	var details []string

	// 1. Success rate component (50% weight, max 50 points)
	successRate := s.jobStats.SuccessRate(w.PubKey)
	successScore := successRate * 50.0
	score += successScore
	details = append(details, fmt.Sprintf("success=%.0f%%", successRate*100))

	// Check minimum success rate threshold
	if policy.MinSuccessRate > 0 && successRate < policy.MinSuccessRate {
		// Penalize workers below threshold heavily
		score *= 0.5
		details = append(details, "below min success rate")
	}

	// 2. Response time component (25% weight, max 25 points)
	// Compare to a baseline of 60 seconds (faster = better)
	baselineMs := int64(60000) // 60 seconds baseline
	if stats.AvgDurationMs > 0 {
		// Faster workers score higher
		// Score = 25 * (baseline / actual), capped at 25
		timeScore := 25.0 * (float64(baselineMs) / float64(stats.AvgDurationMs))
		if timeScore > 25.0 {
			timeScore = 25.0
		}
		score += timeScore
		details = append(details, fmt.Sprintf("avg=%dms", stats.AvgDurationMs))
	} else {
		// No timing data - give neutral score
		score += 12.5
	}

	// 3. Experience component (15% weight, max 15 points)
	// More jobs = more trusted, with diminishing returns
	// log2(jobs) * 3, capped at 15
	expScore := math.Log2(float64(totalJobs+1)) * 3.0
	if expScore > 15.0 {
		expScore = 15.0
	}
	score += expScore
	details = append(details, fmt.Sprintf("jobs=%d", totalJobs))

	// 4. Availability component (10% weight, max 10 points)
	// Lower queue depth = more available = higher score
	availScore := 10.0 / float64(1+w.CurrentQueueDepth)
	score += availScore
	if w.CurrentQueueDepth > 0 {
		details = append(details, fmt.Sprintf("queue=%d", w.CurrentQueueDepth))
	}

	return score, strings.Join(details, ", ")
}

// matchesSelector checks if a worker matches the environment's loom_worker_selector.
// The selector is a map of key-value pairs that must match worker properties.
func matchesSelector(w domain.Worker, selector map[string]any) bool {
	if len(selector) == 0 {
		return true // no selector = match all
	}

	for key, val := range selector {
		strVal := fmt.Sprintf("%v", val)
		switch key {
		case "architecture", "arch":
			if !strings.EqualFold(w.Architecture, strVal) {
				return false
			}
		case "name":
			if !strings.Contains(strings.ToLower(w.Name), strings.ToLower(strVal)) {
				return false
			}
		case "pubkey":
			if w.PubKey != strVal {
				return false
			}
		case "software":
			if !w.HasSoftware(strVal) {
				return false
			}
		case "min_concurrent":
			if minC, ok := val.(float64); ok && w.MaxConcurrentJobs < int(minC) {
				return false
			}
		case "geohash":
			if w.Geohash == "" || !strings.HasPrefix(w.Geohash, strVal) {
				return false
			}
		}
	}
	return true
}

// --- helpers ---

func lowestPrice(w domain.Worker) int {
	if len(w.Pricing) == 0 {
		return 0
	}
	min := math.MaxInt
	for _, p := range w.Pricing {
		if p.PricePerSecond < min {
			min = p.PricePerSecond
		}
	}
	return min
}

func (s *WorkerPolicyService) workerUnderPrice(w domain.Worker, maxPrice int) bool {
	price := lowestPrice(w)
	return price == 0 || price <= maxPrice // free workers always pass
}

func workerSchedulingStateAllowsNewPlacement(state domain.WorkerSchedulingState) bool {
	return normalizedWorkerSchedulingState(state) == domain.WorkerSchedulingActive
}

func workerSchedulingStateRejectionReason(state domain.WorkerSchedulingState) string {
	switch normalizedWorkerSchedulingState(state) {
	case domain.WorkerSchedulingCordoned:
		return "worker is cordoned"
	case domain.WorkerSchedulingDraining:
		return "worker is draining"
	case domain.WorkerSchedulingMaintenance:
		return "worker is in maintenance"
	case domain.WorkerSchedulingDisabled:
		return "worker is disabled"
	default:
		return fmt.Sprintf("worker scheduling state %q is not active", state)
	}
}

func normalizedWorkerSchedulingState(state domain.WorkerSchedulingState) domain.WorkerSchedulingState {
	if state == "" {
		return domain.WorkerSchedulingActive
	}
	return state
}

func geohashCommonPrefix(a, b string) int {
	maxLen := len(a)
	if len(b) < maxLen {
		maxLen = len(b)
	}
	for i := 0; i < maxLen; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return maxLen
}
