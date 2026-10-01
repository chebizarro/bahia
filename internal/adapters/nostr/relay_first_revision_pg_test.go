package nostr

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// bahia-irsry.53 against PostgreSQL: the revision token a client reads from
// the relay-first record is the one the database stored (timestamptz keeps
// microseconds), so the next revision-checked update with that token
// succeeds, and the projection of the stored rows is never signed again.
func TestRelayFirstRevisionTokenSurvivesPostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL relay-first revision test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, zap.NewNop()); err != nil {
		t.Fatal(err)
	}

	services, environments := repository.NewPgServiceRepository(pool), repository.NewPgEnvironmentRepository(pool)
	source := service.NewRegistryService(services, environments, nil, nil, nil, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRegistryTxExecutor(repository.NewPgTxExecutor(pool)))
	h := &relayFirstHarness{source: source, projectorRelay: &captureProjectionPublisher{}, relayFirst: &captureProjectionPublisher{}}
	h.projector = newRelayFirstTestProjector(source, h.projectorRelay)
	h.registry = service.NewRelayFirstRegistry(source, NewRelayFirstStatePublisher(h.projector, newRelayFirstTestPublisher(h.relayFirst)), zap.NewNop())

	suffix := uuid.NewString()[:8]
	// A nanosecond-precision create time (Linux clocks have them; darwin's
	// stop at microseconds) is stored and published at the database's
	// microsecond precision.
	nanos := time.Now().UTC().Truncate(time.Second).Add(123456789 * time.Nanosecond)
	svc := &domain.Service{ID: domain.NewEntityID(), Name: "rev-api-" + suffix, ArtifactRepo: "registry.example/rev", CreatedAt: nanos, UpdatedAt: nanos}
	if err := h.registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	t.Cleanup(func() { _ = services.Delete(context.Background(), svc.ID) })
	env := &domain.Environment{ID: domain.NewEntityID(), Name: "rev-env-" + suffix, Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "web"}, CreatedAt: nanos, UpdatedAt: nanos}
	if err := h.registry.CreateEnvironmentWithDeploymentUnits(ctx, env, []*domain.DeploymentUnit{
		{Key: "web", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/web", ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged},
	}); err != nil {
		t.Fatalf("CreateEnvironmentWithDeploymentUnits: %v", err)
	}
	t.Cleanup(func() { _ = environments.Delete(context.Background(), env.ID) })
	assertSignedOnceInSharedDB(t, h, events.EventServiceCreated, KindServiceRegistry, svc.ID, 1)
	assertSignedOnceInSharedDB(t, h, events.EventEnvironmentCreated, KindEnvironmentRegistry, env.ID, 1)

	for round := 1; round <= 2; round++ {
		token := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindServiceRegistry, svc.ID)).UpdatedAt
		stored, err := services.GetByID(ctx, svc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !token.Equal(stored.UpdatedAt) {
			t.Fatalf("round %d: service record token %s != stored revision %s", round, token.Format(time.RFC3339Nano), stored.UpdatedAt.Format(time.RFC3339Nano))
		}
		edit := *stored
		edit.DefaultBranch = "release-" + string(rune('0'+round))
		if err := h.registry.UpdateServiceWithExpectedRevision(ctx, &edit, token); err != nil {
			t.Fatalf("round %d: service update with the relay token: %v", round, err)
		}
		assertSignedOnceInSharedDB(t, h, events.EventServiceUpdated, KindServiceRegistry, svc.ID, round+1)

		envToken := decodeRegistryRecord(t, latestOn(t, h.relayFirst, KindEnvironmentRegistry, env.ID)).UpdatedAt
		storedEnv, err := environments.GetByID(ctx, env.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !envToken.Equal(storedEnv.UpdatedAt) {
			t.Fatalf("round %d: environment record token %s != stored revision %s", round, envToken.Format(time.RFC3339Nano), storedEnv.UpdatedAt.Format(time.RFC3339Nano))
		}
		envEdit := *storedEnv
		envEdit.Protected = round%2 == 1
		if err := h.registry.UpdateEnvironmentWithDeploymentUnits(ctx, &envEdit, []*domain.DeploymentUnit{
			{Key: "web", RuntimeType: domain.RuntimeTypeCompose, ComposeDir: "/srv/web-" + string(rune('0'+round)), ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged},
		}, envToken); err != nil {
			t.Fatalf("round %d: environment update with the relay token: %v", round, err)
		}
		assertSignedOnceInSharedDB(t, h, events.EventEnvironmentUpdated, KindEnvironmentRegistry, env.ID, round+1)
	}

	// A stale row written back (its old revision) still advances the stored
	// revision, so optimistic-concurrency tokens never repeat.
	stored, err := services.GetByID(ctx, svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := stored.UpdatedAt
	stale := *stored
	if err := services.Update(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if !stale.UpdatedAt.After(before) {
		t.Fatalf("stale write kept revision %s (was %s)", stale.UpdatedAt, before)
	}
	reread, _ := services.GetByID(ctx, svc.ID)
	if !reread.UpdatedAt.Equal(stale.UpdatedAt) {
		t.Fatalf("returned revision %s != stored %s", stale.UpdatedAt, reread.UpdatedAt)
	}
}

// assertSignedOnceInSharedDB is assertSignedOnce for a database other test
// packages share: the snapshot step republishes only this test's entities
// (the per-entity publish RepublishSnapshot's registry loop runs), because
// listing every row would read other packages' fixtures.
func assertSignedOnceInSharedDB(t *testing.T, h *relayFirstHarness, eventType events.EventType, legacyKind int, id uuid.UUID, writes int) {
	t.Helper()
	ctx := context.Background()
	h.projector.handleEvent(ctx, events.Event{Type: eventType, EntityID: id.String()})
	switch legacyKind {
	case KindServiceRegistry:
		svc, err := h.source.GetService(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.projector.publishServiceRegistry(ctx, svc, false); err != nil {
			t.Fatal(err)
		}
	case KindEnvironmentRegistry:
		env, err := h.source.GetEnvironment(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.projector.publishEnvironmentRegistry(ctx, env, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := signaturesOn(h.relayFirst, legacyKind, id); got != writes {
		t.Fatalf("relay-first signed %d events on %s, want %d (one per write)", got, id, writes)
	}
	if got := signaturesOn(h.projectorRelay, legacyKind, id); got != 0 {
		t.Fatalf("projector re-signed %d events on %s after %s; want 0", got, id, eventType)
	}
}
