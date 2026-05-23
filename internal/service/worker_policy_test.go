package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// mockWorkerRepo is an in-memory WorkerRepository for testing.
type mockWorkerRepo struct {
	workers []domain.Worker
}

func (m *mockWorkerRepo) Upsert(_ context.Context, w *domain.Worker) error {
	for i, existing := range m.workers {
		if existing.PubKey == w.PubKey {
			m.workers[i] = *w
			return nil
		}
	}
	m.workers = append(m.workers, *w)
	return nil
}

func (m *mockWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	for _, w := range m.workers {
		if w.PubKey == pubkey {
			return &w, nil
		}
	}
	return nil, nil
}

func (m *mockWorkerRepo) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	var result []domain.Worker
	for _, w := range m.workers {
		if status != "" && string(w.Status) != status {
			continue
		}
		result = append(result, w)
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (m *mockWorkerRepo) UpdateStatus(_ context.Context, pubkey string, status domain.WorkerStatus) error {
	for i, w := range m.workers {
		if w.PubKey == pubkey {
			m.workers[i].Status = status
			return nil
		}
	}
	return nil
}

func makeWorker(pubkey, name string, queueDepth int, price int, geohash, arch string, software ...string) domain.Worker {
	w := domain.Worker{
		PubKey:              pubkey,
		Name:                name,
		Status:              domain.WorkerStatusOnline,
		CurrentQueueDepth:   queueDepth,
		MaxConcurrentJobs:   4,
		Architecture:        arch,
		Geohash:             geohash,
		LastAdvertisementAt: time.Now().UTC(),
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if price > 0 {
		w.Pricing = []domain.WorkerPricing{{
			MintURL:        "https://mint.example.com",
			PricePerSecond: price,
			Unit:           "sat",
		}}
	}
	for _, sw := range software {
		w.Software = append(w.Software, domain.WorkerSoftware{Name: sw})
	}
	return w
}

func makeEnv(policyMap map[string]any) *domain.Environment {
	env := &domain.Environment{
		ID:   uuid.New(),
		Name: "test-env",
	}
	if policyMap != nil {
		env.RuntimeConfig = map[string]any{
			"worker_policy": policyMap,
		}
	}
	return env
}

func TestSelectWorker_CheapestStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-expensive", "expensive", 0, 100, "", "linux/amd64"),
			makeWorker("pk-cheap", "cheap", 0, 10, "", "linux/amd64"),
			makeWorker("pk-medium", "medium", 0, 50, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"strategy": "cheapest"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-cheap" {
		t.Errorf("expected pk-cheap, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_CheapestFreeWorkersWin(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-paid", "paid", 0, 10, "", "linux/amd64"),
			makeWorker("pk-free", "free", 0, 0, "", "linux/amd64"), // no pricing = free
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"strategy": "cheapest"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-free" {
		t.Errorf("expected pk-free, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_FastestStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-busy", "busy", 3, 10, "", "linux/amd64"),
			makeWorker("pk-idle", "idle", 0, 10, "", "linux/amd64"),
			makeWorker("pk-medium", "medium", 1, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"strategy": "fastest"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-idle" {
		t.Errorf("expected pk-idle, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_NearestStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-far", "far", 0, 10, "u0000", "linux/amd64"),        // different region
			makeWorker("pk-near", "near", 0, 10, "9q8yy", "linux/amd64"),      // same region as reference
			makeWorker("pk-closer", "closer", 0, 10, "9q8yyk", "linux/amd64"), // closer prefix match
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy": "nearest",
		"geohash":  "9q8yykb", // San Francisco area
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-closer" {
		t.Errorf("expected pk-closer, got %s (score=%.2f)", result.Worker.PubKey, result.Score)
	}
}

func TestSelectWorker_PreferredStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-arm", "arm-worker", 0, 10, "", "linux/arm64"),
			makeWorker("pk-amd", "amd-worker", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := &domain.Environment{
		ID:   uuid.New(),
		Name: "arm-env",
		LoomWorkerSelector: map[string]any{
			"architecture": "linux/arm64",
		},
		RuntimeConfig: map[string]any{
			"worker_policy": map[string]any{
				"strategy": "preferred",
			},
		},
	}

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-arm" {
		t.Errorf("expected pk-arm, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_PreferredBySoftware(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-no-docker", "no-docker", 0, 10, "", "linux/amd64"),
			makeWorker("pk-docker", "has-docker", 0, 10, "", "linux/amd64", "docker"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := &domain.Environment{
		ID:   uuid.New(),
		Name: "docker-env",
		LoomWorkerSelector: map[string]any{
			"software": "docker",
		},
		RuntimeConfig: map[string]any{
			"worker_policy": map[string]any{
				"strategy": "preferred",
			},
		},
	}

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-docker" {
		t.Errorf("expected pk-docker, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_MaxPriceFilter(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-expensive", "expensive", 0, 200, "", "linux/amd64"),
			makeWorker("pk-cheap", "cheap", 0, 50, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":  "cheapest",
		"max_price": float64(100), // filter out workers above 100 sat/sec
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-cheap" {
		t.Errorf("expected pk-cheap, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_MaxQueueDepthFilter(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-overloaded", "overloaded", 10, 10, "", "linux/amd64"),
			makeWorker("pk-light", "light", 1, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":        "fastest",
		"max_queue_depth": float64(5),
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-light" {
		t.Errorf("expected pk-light, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_ExcludeWorkers(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-banned", "banned", 0, 10, "", "linux/amd64"),
			makeWorker("pk-allowed", "allowed", 0, 20, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":        "cheapest",
		"exclude_workers": []any{"pk-banned"},
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-allowed" {
		t.Errorf("expected pk-allowed, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_RequireSoftware(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-bare", "bare", 0, 10, "", "linux/amd64"),
			makeWorker("pk-full", "full", 0, 10, "", "linux/amd64", "docker", "kubectl"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":         "cheapest",
		"require_software": []any{"docker", "kubectl"},
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-full" {
		t.Errorf("expected pk-full, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_NoWorkersAvailable(t *testing.T) {
	repo := &mockWorkerRepo{workers: nil}
	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(nil)

	_, err := svc.SelectWorker(context.Background(), env)
	if err == nil {
		t.Fatal("expected error for no workers")
	}
}

func TestSelectWorker_AllFilteredOut(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-only", "only", 0, 500, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":  "cheapest",
		"max_price": float64(10), // too low for any worker
	})

	_, err := svc.SelectWorker(context.Background(), env)
	if err == nil {
		t.Fatal("expected error when all workers filtered out")
	}
}

func TestSelectWorker_DefaultStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-a", "worker-a", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	// No policy configured — should default to cheapest.
	env := &domain.Environment{ID: uuid.New(), Name: "test"}

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-a" {
		t.Errorf("expected pk-a, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_ReputationStrategy(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-good", "good-worker", 0, 10, "", "linux/amd64"),
			makeWorker("pk-bad", "bad-worker", 0, 10, "", "linux/amd64"),
			makeWorker("pk-new", "new-worker", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())

	// Set up job stats tracker with history
	tracker := NewJobStatsTracker(100)
	// pk-good: 90% success rate, 20 jobs
	for i := 0; i < 18; i++ {
		tracker.RecordJobCompletion("pk-good", 30000, true)
	}
	for i := 0; i < 2; i++ {
		tracker.RecordJobCompletion("pk-good", 0, false)
	}
	// pk-bad: 50% success rate, 10 jobs
	for i := 0; i < 5; i++ {
		tracker.RecordJobCompletion("pk-bad", 60000, true)
	}
	for i := 0; i < 5; i++ {
		tracker.RecordJobCompletion("pk-bad", 0, false)
	}
	// pk-new: only 2 jobs (below minimum threshold)
	tracker.RecordJobCompletion("pk-new", 25000, true)
	tracker.RecordJobCompletion("pk-new", 25000, true)

	svc.SetJobStatsTracker(tracker)

	env := makeEnv(map[string]any{"strategy": "reputation"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-good" {
		t.Errorf("expected pk-good (highest reputation), got %s (score=%.2f, reason=%s)",
			result.Worker.PubKey, result.Score, result.Reason)
	}
}

func TestSelectWorker_ReputationStrategy_NoStats(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-a", "worker-a", 0, 10, "", "linux/amd64"),
			makeWorker("pk-b", "worker-b", 0, 20, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	// No job stats tracker set
	env := makeEnv(map[string]any{"strategy": "reputation"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result")
	}
	// Without stats, all workers should get neutral score (50)
	if result.Score != 50.0 {
		t.Errorf("expected score 50.0 without stats, got %.2f", result.Score)
	}
}

func TestSelectWorker_ReputationStrategy_NewWorkerBootstrap(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-new", "new-worker", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	tracker := NewJobStatsTracker(100)
	// Only 2 jobs - below default threshold of 5
	tracker.RecordJobCompletion("pk-new", 25000, true)
	tracker.RecordJobCompletion("pk-new", 25000, true)
	svc.SetJobStatsTracker(tracker)

	env := makeEnv(map[string]any{"strategy": "reputation"})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// New workers get bootstrap score of 45
	if result.Score != 45.0 {
		t.Errorf("expected bootstrap score 45.0, got %.2f", result.Score)
	}
}

func TestSelectWorker_ReputationStrategy_MinSuccessRate(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-high", "high-success", 0, 10, "", "linux/amd64"),
			makeWorker("pk-low", "low-success", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	tracker := NewJobStatsTracker(100)
	// pk-high: 90% success
	for i := 0; i < 9; i++ {
		tracker.RecordJobCompletion("pk-high", 30000, true)
	}
	tracker.RecordJobCompletion("pk-high", 0, false)
	// pk-low: 60% success (below 0.75 threshold)
	for i := 0; i < 6; i++ {
		tracker.RecordJobCompletion("pk-low", 30000, true)
	}
	for i := 0; i < 4; i++ {
		tracker.RecordJobCompletion("pk-low", 0, false)
	}
	svc.SetJobStatsTracker(tracker)

	env := makeEnv(map[string]any{
		"strategy":         "reputation",
		"min_success_rate": 0.75, // 75% minimum
	})

	result, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Worker.PubKey != "pk-high" {
		t.Errorf("expected pk-high (above min success rate), got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_ReputationStrategy_FasterWorkerBonus(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-fast", "fast-worker", 0, 10, "", "linux/amd64"),
			makeWorker("pk-slow", "slow-worker", 0, 10, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	tracker := NewJobStatsTracker(100)
	// Both have 100% success rate but different speeds
	for i := 0; i < 10; i++ {
		tracker.RecordJobCompletion("pk-fast", 20000, true)  // 20s avg
		tracker.RecordJobCompletion("pk-slow", 120000, true) // 120s avg
	}
	svc.SetJobStatsTracker(tracker)

	env := makeEnv(map[string]any{"strategy": "reputation"})

	ranked, err := svc.RankWorkers(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ranked) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(ranked))
	}
	if ranked[0].Worker.PubKey != "pk-fast" {
		t.Errorf("expected pk-fast (faster) to rank higher, got %s", ranked[0].Worker.PubKey)
	}
	// Fast worker should have higher score due to response time bonus
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("fast worker score (%.2f) should be higher than slow worker (%.2f)",
			ranked[0].Score, ranked[1].Score)
	}
}

func TestScoreReputation_Components(t *testing.T) {
	repo := &mockWorkerRepo{}
	svc := NewWorkerPolicyService(repo, zap.NewNop())
	tracker := NewJobStatsTracker(100)

	// Perfect worker: 100% success, fast, many jobs, no queue
	for i := 0; i < 50; i++ {
		tracker.RecordJobCompletion("pk-perfect", 30000, true) // 30s = half baseline (60s)
	}
	svc.SetJobStatsTracker(tracker)

	w := makeWorker("pk-perfect", "perfect", 0, 10, "", "linux/amd64")
	policy := WorkerPolicy{Strategy: StrategyReputation}

	score, reason := svc.scoreReputation(w, policy)

	// Expected max score breakdown:
	// - Success rate: 50 (100% * 50)
	// - Response time: 25 (capped, since 30s is faster than 60s baseline)
	// - Experience: ~15 (log2(51) * 3 ≈ 17, capped at 15)
	// - Availability: 10 (queue=0)
	// Total: ~100
	if score < 95 || score > 105 {
		t.Errorf("expected score ~100 for perfect worker, got %.2f (reason: %s)", score, reason)
	}
}

func TestRankWorkers(t *testing.T) {
	repo := &mockWorkerRepo{
		workers: []domain.Worker{
			makeWorker("pk-a", "a", 3, 100, "", "linux/amd64"),
			makeWorker("pk-b", "b", 0, 10, "", "linux/amd64"),
			makeWorker("pk-c", "c", 1, 50, "", "linux/amd64"),
		},
	}

	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"strategy": "cheapest"})

	ranked, err := svc.RankWorkers(context.Background(), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ranked) != 3 {
		t.Fatalf("expected 3 ranked workers, got %d", len(ranked))
	}
	// Cheapest first.
	if ranked[0].Worker.PubKey != "pk-b" {
		t.Errorf("expected pk-b as top ranked, got %s", ranked[0].Worker.PubKey)
	}
	// Verify descending score.
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Score > ranked[i-1].Score {
			t.Errorf("rank[%d].Score (%.2f) > rank[%d].Score (%.2f) — not sorted descending",
				i, ranked[i].Score, i-1, ranked[i-1].Score)
		}
	}
}

func TestSelectWorker_ExcludesNonActiveSchedulingStates(t *testing.T) {
	cordoned := makeWorker("pk-cordoned", "cordoned", 0, 1, "", "linux/amd64")
	cordoned.SchedulingState = domain.WorkerSchedulingCordoned
	draining := makeWorker("pk-draining", "draining", 0, 1, "", "linux/amd64")
	draining.SchedulingState = domain.WorkerSchedulingDraining
	maintenance := makeWorker("pk-maintenance", "maintenance", 0, 1, "", "linux/amd64")
	maintenance.SchedulingState = domain.WorkerSchedulingMaintenance
	disabled := makeWorker("pk-disabled", "disabled", 0, 1, "", "linux/amd64")
	disabled.SchedulingState = domain.WorkerSchedulingDisabled
	active := makeWorker("pk-active", "active", 0, 100, "", "linux/amd64")
	active.SchedulingState = domain.WorkerSchedulingActive

	repo := &mockWorkerRepo{workers: []domain.Worker{cordoned, draining, maintenance, disabled, active}}
	svc := NewWorkerPolicyService(repo, zap.NewNop())

	result, err := svc.SelectWorker(context.Background(), makeEnv(map[string]any{"strategy": "cheapest"}))
	if err != nil {
		t.Fatalf("select worker: %v", err)
	}
	if result.Worker.PubKey != "pk-active" {
		t.Fatalf("expected only active worker to be selected, got %s", result.Worker.PubKey)
	}
}

func TestSelectWorker_HonorsPinnedWorker(t *testing.T) {
	repo := &mockWorkerRepo{workers: []domain.Worker{
		makeWorker("pk-cheap", "cheap", 0, 1, "", "linux/amd64"),
		makeWorker("pk-pinned", "pinned", 0, 100, "", "linux/amd64"),
	}}
	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"strategy": "cheapest", "pinned_worker": "pk-pinned"})

	selected, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("select worker: %v", err)
	}
	if selected.Worker.PubKey != "pk-pinned" {
		t.Fatalf("expected pinned worker, got %s", selected.Worker.PubKey)
	}

	ranked, err := svc.RankWorkers(context.Background(), env)
	if err != nil {
		t.Fatalf("rank workers: %v", err)
	}
	if !ranked[0].Eligible || ranked[0].Worker.PubKey != "pk-pinned" {
		t.Fatalf("expected pinned worker eligible first, got %#v", ranked[0])
	}
	if ranked[1].Eligible || ranked[1].Reason != "worker does not match pinned_worker pk-pinned" {
		t.Fatalf("expected non-pinned worker rejection, got %#v", ranked[1])
	}
}

func TestSelectWorker_LoadsPinnedWorkerOutsideFirstPage(t *testing.T) {
	workers := make([]domain.Worker, 0, 101)
	for i := 0; i < 100; i++ {
		workers = append(workers, makeWorker("pk-page-"+string(rune('a'+i%26)), "page", 0, 1, "", "linux/amd64"))
	}
	workers = append(workers, makeWorker("pk-pinned-outside-page", "pinned", 0, 100, "", "linux/amd64"))
	repo := &mockWorkerRepo{workers: workers}
	svc := NewWorkerPolicyService(repo, zap.NewNop())

	selected, err := svc.SelectWorker(context.Background(), makeEnv(map[string]any{"pinned_worker": "pk-pinned-outside-page"}))
	if err != nil {
		t.Fatalf("select worker: %v", err)
	}
	if selected.Worker.PubKey != "pk-pinned-outside-page" {
		t.Fatalf("expected pinned worker outside first page, got %s", selected.Worker.PubKey)
	}
}

func TestRankWorkers_PinnedWorkerShowsRequirementConflict(t *testing.T) {
	pinned := makeWorker("pk-pinned", "pinned", 0, 10, "", "linux/amd64")
	repo := &mockWorkerRepo{workers: []domain.Worker{pinned, makeWorker("pk-other", "other", 0, 10, "", "linux/amd64", "docker")}}
	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{"pinned_worker": "pk-pinned", "require_software": []any{"docker"}})

	ranked, err := svc.RankWorkers(context.Background(), env)
	if err != nil {
		t.Fatalf("rank workers: %v", err)
	}
	reasons := map[string]string{}
	for _, candidate := range ranked {
		reasons[candidate.Worker.PubKey] = candidate.Reason
	}
	if reasons["pk-pinned"] != "worker missing required software docker" {
		t.Fatalf("expected pinned worker compatibility reason, got %q", reasons["pk-pinned"])
	}
	if reasons["pk-other"] != "worker does not match pinned_worker pk-pinned" {
		t.Fatalf("expected non-pinned rejection, got %q", reasons["pk-other"])
	}
}

func TestSelectWorker_UsesLabelSelectorAndRolloutTargetLabels(t *testing.T) {
	canary := makeWorker("pk-canary", "canary", 0, 1, "", "linux/amd64")
	canary.Labels = map[string]string{"role": "inference", "track": "canary"}
	stable := makeWorker("pk-stable", "stable", 0, 100, "", "linux/amd64")
	stable.Labels = map[string]string{"role": "inference", "track": "stable"}
	repo := &mockWorkerRepo{workers: []domain.Worker{canary, stable}}
	svc := NewWorkerPolicyService(repo, zap.NewNop())
	env := makeEnv(map[string]any{
		"strategy":       "cheapest",
		"label_selector": map[string]any{"role": "inference"},
		"rollout": map[string]any{
			"from_labels": map[string]any{"track": "canary"},
			"to_labels":   map[string]any{"track": "stable"},
		},
	})

	selected, err := svc.SelectWorker(context.Background(), env)
	if err != nil {
		t.Fatalf("select worker: %v", err)
	}
	if selected.Worker.PubKey != "pk-stable" {
		t.Fatalf("expected stable rollout target worker, got %s", selected.Worker.PubKey)
	}
	ranked, err := svc.RankWorkers(context.Background(), env)
	if err != nil {
		t.Fatalf("rank workers: %v", err)
	}
	if ranked[1].Eligible || ranked[1].Reason != "worker label track=canary does not match required stable" {
		t.Fatalf("expected canary rollout rejection reason, got %#v", ranked[1])
	}
}

func TestRankWorkers_IncludesSchedulingRejectionReasons(t *testing.T) {
	active := makeWorker("pk-active", "active", 0, 10, "", "linux/amd64")
	active.SchedulingState = domain.WorkerSchedulingActive
	cordoned := makeWorker("pk-cordoned", "cordoned", 0, 1, "", "linux/amd64")
	cordoned.SchedulingState = domain.WorkerSchedulingCordoned
	draining := makeWorker("pk-draining", "draining", 0, 1, "", "linux/amd64")
	draining.SchedulingState = domain.WorkerSchedulingDraining

	repo := &mockWorkerRepo{workers: []domain.Worker{cordoned, draining, active}}
	svc := NewWorkerPolicyService(repo, zap.NewNop())

	ranked, err := svc.RankWorkers(context.Background(), makeEnv(map[string]any{"strategy": "cheapest"}))
	if err != nil {
		t.Fatalf("rank workers: %v", err)
	}
	if len(ranked) != 3 {
		t.Fatalf("expected active and rejected workers in rank output, got %d", len(ranked))
	}
	if !ranked[0].Eligible || ranked[0].Worker.PubKey != "pk-active" {
		t.Fatalf("expected active worker ranked first, got %#v", ranked[0])
	}

	reasonsByPubKey := map[string]string{}
	for _, scored := range ranked {
		reasonsByPubKey[scored.Worker.PubKey] = scored.Reason
	}
	if reasonsByPubKey["pk-cordoned"] != "worker is cordoned" {
		t.Fatalf("expected cordoned rejection reason, got %q", reasonsByPubKey["pk-cordoned"])
	}
	if reasonsByPubKey["pk-draining"] != "worker is draining" {
		t.Fatalf("expected draining rejection reason, got %q", reasonsByPubKey["pk-draining"])
	}
}

func TestMatchesSelector(t *testing.T) {
	w := makeWorker("pk1", "test-worker", 0, 10, "9q8yy", "linux/amd64", "docker")

	tests := []struct {
		name     string
		selector map[string]any
		want     bool
	}{
		{"empty selector", nil, true},
		{"arch match", map[string]any{"architecture": "linux/amd64"}, true},
		{"arch mismatch", map[string]any{"architecture": "linux/arm64"}, false},
		{"name match", map[string]any{"name": "test"}, true},
		{"name mismatch", map[string]any{"name": "production"}, false},
		{"software match", map[string]any{"software": "docker"}, true},
		{"software mismatch", map[string]any{"software": "kubectl"}, false},
		{"pubkey match", map[string]any{"pubkey": "pk1"}, true},
		{"pubkey mismatch", map[string]any{"pubkey": "pk2"}, false},
		{"geohash match", map[string]any{"geohash": "9q8"}, true},
		{"geohash mismatch", map[string]any{"geohash": "u09"}, false},
		{"multi-field match", map[string]any{"architecture": "linux/amd64", "software": "docker"}, true},
		{"multi-field partial", map[string]any{"architecture": "linux/amd64", "software": "kubectl"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchesSelector(w, tc.selector)
			if got != tc.want {
				t.Errorf("matchesSelector() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGeohashCommonPrefix(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"9q8yykb", "9q8yyk", 6},
		{"9q8yykb", "9q8yy", 5},
		{"9q8yykb", "u0000", 0},
		{"", "9q8", 0},
		{"abc", "abc", 3},
	}
	for _, tc := range tests {
		got := geohashCommonPrefix(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("geohashCommonPrefix(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLowestPrice(t *testing.T) {
	// No pricing.
	w := makeWorker("pk", "w", 0, 0, "", "")
	if got := lowestPrice(w); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}

	// Single price.
	w = makeWorker("pk", "w", 0, 42, "", "")
	if got := lowestPrice(w); got != 42 {
		t.Errorf("expected 42, got %d", got)
	}

	// Multiple prices — should return lowest.
	w.Pricing = []domain.WorkerPricing{
		{PricePerSecond: 100, Unit: "sat"},
		{PricePerSecond: 25, Unit: "sat"},
		{PricePerSecond: 50, Unit: "sat"},
	}
	if got := lowestPrice(w); got != 25 {
		t.Errorf("expected 25, got %d", got)
	}
}

func TestExtractPolicy_Defaults(t *testing.T) {
	svc := NewWorkerPolicyService(nil, zap.NewNop())

	// Nil environment.
	p := svc.extractPolicy(nil)
	if p.Strategy != StrategyCheapest {
		t.Errorf("expected cheapest, got %s", p.Strategy)
	}

	// No runtime config.
	env := &domain.Environment{ID: uuid.New()}
	p = svc.extractPolicy(env)
	if p.Strategy != StrategyCheapest {
		t.Errorf("expected cheapest, got %s", p.Strategy)
	}

	// RuntimeConfig without worker_policy.
	env.RuntimeConfig = map[string]any{"other": "value"}
	p = svc.extractPolicy(env)
	if p.Strategy != StrategyCheapest {
		t.Errorf("expected cheapest, got %s", p.Strategy)
	}
}

func TestExtractPolicy_FullConfig(t *testing.T) {
	svc := NewWorkerPolicyService(nil, zap.NewNop())
	env := &domain.Environment{
		ID: uuid.New(),
		RuntimeConfig: map[string]any{
			"worker_policy": map[string]any{
				"strategy":         "nearest",
				"geohash":          "9q8yy",
				"max_price":        float64(100),
				"max_queue_depth":  float64(5),
				"require_software": []any{"docker"},
				"exclude_workers":  []any{"pk-banned"},
			},
		},
	}

	p := svc.extractPolicy(env)
	if p.Strategy != StrategyNearest {
		t.Errorf("expected nearest, got %s", p.Strategy)
	}
	if p.Geohash != "9q8yy" {
		t.Errorf("expected 9q8yy, got %s", p.Geohash)
	}
	if p.MaxPrice != 100 {
		t.Errorf("expected 100, got %d", p.MaxPrice)
	}
	if p.MaxQueueDepth != 5 {
		t.Errorf("expected 5, got %d", p.MaxQueueDepth)
	}
	if len(p.RequireSoftware) != 1 || p.RequireSoftware[0] != "docker" {
		t.Errorf("expected [docker], got %v", p.RequireSoftware)
	}
	if len(p.ExcludeWorkers) != 1 || p.ExcludeWorkers[0] != "pk-banned" {
		t.Errorf("expected [pk-banned], got %v", p.ExcludeWorkers)
	}
}

func TestExtractPolicy_ReputationSettings(t *testing.T) {
	svc := NewWorkerPolicyService(nil, zap.NewNop())
	env := &domain.Environment{
		ID: uuid.New(),
		RuntimeConfig: map[string]any{
			"worker_policy": map[string]any{
				"strategy":          "reputation",
				"min_success_rate":  0.8,
				"min_jobs_required": float64(10),
			},
		},
	}

	p := svc.extractPolicy(env)
	if p.Strategy != StrategyReputation {
		t.Errorf("expected reputation, got %s", p.Strategy)
	}
	if p.MinSuccessRate != 0.8 {
		t.Errorf("expected min_success_rate 0.8, got %f", p.MinSuccessRate)
	}
	if p.MinJobsRequired != 10 {
		t.Errorf("expected min_jobs_required 10, got %d", p.MinJobsRequired)
	}
}
