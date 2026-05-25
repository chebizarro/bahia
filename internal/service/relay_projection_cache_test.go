package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type relayProjectionMetaMemoryRepo struct {
	mu    sync.Mutex
	metas map[string]repository.RelayProjectionMeta
}

func newRelayProjectionMetaMemoryRepo() *relayProjectionMetaMemoryRepo {
	return &relayProjectionMetaMemoryRepo{metas: make(map[string]repository.RelayProjectionMeta)}
}

func (r *relayProjectionMetaMemoryRepo) Get(_ context.Context, stream, entityKey string) (*repository.RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	meta, ok := r.metas[stream+"\x00"+entityKey]
	if !ok {
		return nil, nil
	}
	return &meta, nil
}

func (r *relayProjectionMetaMemoryRepo) Upsert(_ context.Context, meta repository.RelayProjectionMeta) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := meta.Stream + "\x00" + meta.EntityKey
	existing, ok := r.metas[key]
	if ok && !meta.UpdatedAt.After(existing.UpdatedAt) {
		return nil
	}
	r.metas[key] = meta
	return nil
}

func (r *relayProjectionMetaMemoryRepo) ListByStream(_ context.Context, stream string) ([]repository.RelayProjectionMeta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	metas := make([]repository.RelayProjectionMeta, 0)
	for _, meta := range r.metas {
		if meta.Stream == stream {
			metas = append(metas, meta)
		}
	}
	return metas, nil
}

func TestRelayProjectionCacheApplyNoPriorMetaAppliesAndStoresMeta(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-1")
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected applier to run once, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-1", false)
}

func TestRelayProjectionCacheApplyOlderThanExistingSkips(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	newer := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, repository.RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: newer, SourceEventID: "evt-newer"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, testProjectionEvent("svc-a", newer.Add(-time.Minute), "evt-old")); err != nil {
		t.Fatalf("apply older: %v", err)
	}
	if applied != 0 {
		t.Fatalf("expected stale event to skip applier, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-newer", false)
}

func TestRelayProjectionCacheApplyNewerThanExistingAppliesAndUpdatesMeta(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	older := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, repository.RelayProjectionMeta{Stream: "service", EntityKey: "svc-a", UpdatedAt: older, SourceEventID: "evt-old"}); err != nil {
		t.Fatalf("seed meta: %v", err)
	}
	applied := 0
	cache.RegisterApplier(nostr.FamilyService, func(context.Context, any) error {
		applied++
		return nil
	})

	if err := cache.Apply(ctx, testProjectionEvent("svc-a", older.Add(time.Minute), "evt-new")); err != nil {
		t.Fatalf("apply newer: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected newer event to apply once, ran %d times", applied)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-new", false)
}

func TestRelayProjectionCacheApplyTombstone(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-delete")
	event.Tombstone = true
	seenTombstone := false
	cache.RegisterApplier(nostr.FamilyService, func(_ context.Context, applied any) error {
		seenTombstone = applied.(*nostr.DecodedProjectionEvent).Tombstone
		return nil
	})

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply tombstone: %v", err)
	}
	if !seenTombstone {
		t.Fatal("expected applier to receive tombstone event")
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-delete", true)
}

func TestRelayProjectionCacheApplyNoRegisteredApplierStoresMetaOnly(t *testing.T) {
	ctx := context.Background()
	repo := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(repo, zap.NewNop())
	event := testProjectionEvent("svc-a", time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC), "evt-1")

	if err := cache.Apply(ctx, event); err != nil {
		t.Fatalf("apply without applier: %v", err)
	}
	assertStoredMeta(t, repo, "service", "svc-a", "evt-1", false)
}

func TestRelayProjectionCacheStandbyDefinitionProjectsArtifactRefs(t *testing.T) {
	workerRepo := newStandbyProjectionWorkerRepo(&domain.Worker{PubKey: "worker-a", Status: domain.WorkerStatusOnline})
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterTier1Tier2Appliers(service.ProjectionCacheRepositories{Workers: workerRepo})
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	requireApply(t, cache, standbyProjectionEvent("standby-node:worker-a:svc-hot", now, "evt-hot", nostr.StandbyNodeDefinition{
		WorkerPubKey: "worker-a",
		ServiceKey:   "svc-hot",
		Tier:         domain.StandbyTierHot,
		ArtifactRef:  "registry.example/svc-hot@sha256:111",
		Profiles:     []domain.ContinuityMode{domain.ContinuityModeDegraded},
		UpdatedAt:    now,
	}))
	requireApply(t, cache, standbyProjectionEvent("standby-node:worker-a:svc-warm", now.Add(time.Minute), "evt-warm", nostr.StandbyNodeDefinition{
		WorkerPubKey: "worker-a",
		ServiceKey:   "svc-warm",
		Tier:         domain.StandbyTierWarm,
		ArtifactRef:  "registry.example/svc-warm@sha256:222",
		Profiles:     []domain.ContinuityMode{domain.ContinuityModeEmergency},
		UpdatedAt:    now.Add(time.Minute),
	}))

	worker := workerRepo.workers["worker-a"]
	if len(worker.StandbyAssignments) != 2 {
		t.Fatalf("standby assignments length = %d, want 2", len(worker.StandbyAssignments))
	}
	refs := standbyRefsByService(worker.StandbyAssignments)
	if refs["svc-hot"] != "registry.example/svc-hot@sha256:111" {
		t.Fatalf("hot standby artifact ref = %q", refs["svc-hot"])
	}
	if refs["svc-warm"] != "registry.example/svc-warm@sha256:222" {
		t.Fatalf("warm standby artifact ref = %q", refs["svc-warm"])
	}
}

func TestRelayProjectionCacheStandbyDefinitionColdWithoutArtifactProjectsEmptyRef(t *testing.T) {
	workerRepo := newStandbyProjectionWorkerRepo(&domain.Worker{PubKey: "worker-a", Status: domain.WorkerStatusOnline})
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterTier1Tier2Appliers(service.ProjectionCacheRepositories{Workers: workerRepo})
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)

	requireApply(t, cache, standbyProjectionEvent("standby-node:worker-a:svc-cold", now, "evt-cold", nostr.StandbyNodeDefinition{
		WorkerPubKey: "worker-a",
		ServiceKey:   "svc-cold",
		Tier:         domain.StandbyTierCold,
		Profiles:     []domain.ContinuityMode{domain.ContinuityModeDegraded},
		UpdatedAt:    now,
	}))

	assignments := workerRepo.workers["worker-a"].StandbyAssignments
	if len(assignments) != 1 {
		t.Fatalf("standby assignments length = %d, want 1", len(assignments))
	}
	if assignments[0].ArtifactRef != "" {
		t.Fatalf("cold standby artifact ref = %q, want empty", assignments[0].ArtifactRef)
	}
}

func requireApply(t *testing.T, cache *service.RelayProjectionCache, event *nostr.DecodedProjectionEvent) {
	t.Helper()
	if err := cache.Apply(context.Background(), event); err != nil {
		t.Fatalf("apply standby projection: %v", err)
	}
}

func standbyProjectionEvent(dTag string, timestamp time.Time, sourceID string, def nostr.StandbyNodeDefinition) *nostr.DecodedProjectionEvent {
	def.SourceEventID = sourceID
	return &nostr.DecodedProjectionEvent{
		Kind:      nostr.KindStandbyNodeDefinition,
		DTag:      dTag,
		Timestamp: timestamp,
		SourceID:  sourceID,
		Family:    nostr.FamilyContinuity,
		Continuity: &nostr.DecodedContinuity{
			StandbyNode: &def,
		},
	}
}

func standbyRefsByService(assignments []domain.WorkerStandbyAssignment) map[string]string {
	refs := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		refs[assignment.ServiceKey] = assignment.ArtifactRef
	}
	return refs
}

type standbyProjectionWorkerRepo struct {
	workers map[string]*domain.Worker
}

func newStandbyProjectionWorkerRepo(workers ...*domain.Worker) *standbyProjectionWorkerRepo {
	repo := &standbyProjectionWorkerRepo{workers: make(map[string]*domain.Worker)}
	for _, worker := range workers {
		copied := *worker
		repo.workers[worker.PubKey] = &copied
	}
	return repo
}

func (r *standbyProjectionWorkerRepo) Upsert(_ context.Context, worker *domain.Worker) error {
	copied := *worker
	r.workers[worker.PubKey] = &copied
	return nil
}

func (r *standbyProjectionWorkerRepo) GetByPubKey(_ context.Context, pubkey string) (*domain.Worker, error) {
	worker := r.workers[pubkey]
	if worker == nil {
		return nil, nil
	}
	copied := *worker
	return &copied, nil
}

func (r *standbyProjectionWorkerRepo) List(context.Context, string, int) ([]domain.Worker, error) {
	return nil, nil
}

func (r *standbyProjectionWorkerRepo) UpdateStatus(_ context.Context, pubkey string, status domain.WorkerStatus) error {
	if worker := r.workers[pubkey]; worker != nil {
		worker.Status = status
	}
	return nil
}

func testProjectionEvent(dTag string, timestamp time.Time, sourceID string) *nostr.DecodedProjectionEvent {
	return &nostr.DecodedProjectionEvent{
		Kind:      nostr.KindServiceRegistry,
		DTag:      dTag,
		Timestamp: timestamp,
		SourceID:  sourceID,
		Family:    nostr.FamilyService,
	}
}

func assertStoredMeta(t *testing.T, repo repository.RelayProjectionMetaRepository, stream, entityKey, sourceID string, tombstoned bool) {
	t.Helper()
	meta, err := repo.Get(context.Background(), stream, entityKey)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if meta == nil {
		t.Fatalf("expected meta for %s/%s", stream, entityKey)
	}
	if meta.SourceEventID != sourceID || meta.Tombstoned != tombstoned {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}
