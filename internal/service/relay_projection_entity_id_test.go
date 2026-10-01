package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// bahia-irsry.35: relay consumers decode legacy Postgres-minted (v4) and
// client-minted (v7) service coordinates identically.
func TestRelayProjectionCacheDecodesLegacyAndClientMintedServiceCoordinates(t *testing.T) {
	ctx := context.Background()
	repo := &entityIDServiceRepo{services: map[uuid.UUID]domain.Service{}}
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterTier1Tier2Appliers(service.ProjectionCacheRepositories{Services: repo})

	legacy := uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	client := domain.NewEntityID()
	now := time.Now().UTC()
	for i, id := range []uuid.UUID{legacy, client} {
		d := domain.FormatEntityCoordinate("", id)
		event := &nostr.DecodedProjectionEvent{
			Kind:      nostr.KindServiceRegistry,
			DTag:      d,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			SourceID:  d + "-event",
			Family:    nostr.FamilyService,
			Service:   &nostr.DecodedService{ID: id.String(), Name: "svc-" + id.String()[:8], ArtifactRepo: "ghcr.io/acme/api", RuntimeType: domain.RuntimeTypeDocker},
		}
		if err := cache.Apply(ctx, event); err != nil {
			t.Fatalf("apply coordinate %q: %v", d, err)
		}
		stored, ok := repo.services[id]
		if !ok || stored.ID != id {
			t.Fatalf("coordinate %q (v%d) did not decode to entity %s: %#v", d, id.Version(), id, repo.services)
		}
	}
}

type entityIDServiceRepo struct {
	mu       sync.Mutex
	services map[uuid.UUID]domain.Service
}

func (r *entityIDServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[svc.ID] = *svc
	return nil
}
func (r *entityIDServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if svc, ok := r.services[id]; ok {
		return &svc, nil
	}
	return nil, nil
}
func (r *entityIDServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *entityIDServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *entityIDServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *entityIDServiceRepo) Update(ctx context.Context, svc *domain.Service) error {
	return r.Create(ctx, svc)
}
func (r *entityIDServiceRepo) Delete(context.Context, uuid.UUID) error { return nil }
