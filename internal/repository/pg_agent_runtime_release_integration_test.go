package repository_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// arbInt is a small helper to seed digest fixtures for real-PG tests.
func integrationRelease(org, source uuid.UUID, marker string) *domain.AgentRuntimeRelease {
	digest := "sha256:"
	for i := 0; i < 64; i++ {
		digest += marker
	}
	return &domain.AgentRuntimeRelease{
		OrgID:       org,
		SourceID:    source,
		ImageRepo:   "registry.example/metiq",
		ImageDigest: digest,
		VerifiedAt:  time.Unix(1_800_000_000, 0).UTC(),
		Provenance: domain.RuntimeReleaseProvenance{
			Provider:           "metiq-hiveci",
			ReleaseEventID:     "release-" + marker,
			WorkflowRunEventID: "run-" + marker,
			ManifestDigest:     digest,
			SBOMDigest:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProvenanceDigest:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AttestorPubkey:     "attestor",
		},
	}
}

// bootstrapPromotionFixtures creates a fresh org + source + service and returns
// their IDs. It requires the standard bahia migrations to have been applied.
func bootstrapPromotionFixtures(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (orgID, serviceID, sourceID uuid.UUID) {
	t.Helper()
	orgID = uuid.New()
	serviceID = uuid.New()
	sourceID = uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, display_name, owner_pubkey) VALUES ($1,$2,$3,$4)`, orgID, "promo-org-"+orgID.String()[:8], "Promotion Org "+orgID.String()[:8], "test-owner-"+orgID.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO services (id, org_id, name, repo_url, artifact_repo, default_branch, runtime_type)
		VALUES ($1,$2,$3,$4,$5,'main','container')`, serviceID, orgID, "svc-"+serviceID.String()[:8], "https://example.invalid/svc", "agents/promotion")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO agent_runtime_sources (id, org_id, repository, branch, release_channel) VALUES ($1,$2,'https://example.invalid/runtime','main','stable')`, sourceID, orgID)
	require.NoError(t, err)
	return orgID, serviceID, sourceID
}

// TestAgentRuntimeReleasePostgresAppendsAToBToAPromotion is the direct
// regression: on real PostgreSQL 16, A->B->A promotion events must produce
// three durable bindings and the latest re-binding of A must resolve rollback
// to the intermediate B binding.
func TestAgentRuntimeReleasePostgresAppendsAToBToAPromotion(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL A->B->A promotion test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	orgID, serviceID, sourceID := bootstrapPromotionFixtures(ctx, t, pool)
	repo := repository.NewPgAgentRuntimeReleaseRepository(pool)

	rA := integrationRelease(orgID, sourceID, "1")
	require.NoError(t, repo.CreateRelease(ctx, rA))
	rB := integrationRelease(orgID, sourceID, "2")
	require.NoError(t, repo.CreateRelease(ctx, rB))

	bind := func(releaseID uuid.UUID, event string) *domain.AgentServiceReleaseBinding {
		b := &domain.AgentServiceReleaseBinding{OrgID: orgID, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: releaseID, ReleaseChannel: "stable", SourceEventID: event}
		require.NoError(t, repo.BindRelease(ctx, b))
		return b
	}
	a1 := bind(rA.ID, "promo-a1-"+uuid.NewString())
	b1 := bind(rB.ID, "promo-b1-"+uuid.NewString())
	a2 := bind(rA.ID, "promo-a2-"+uuid.NewString())

	require.NotEqual(t, a1.ID, a2.ID, "re-binding release A must append a new binding, not fold onto the earlier A binding")
	require.NotNil(t, a2.PreviousBindingID)
	require.Equal(t, b1.ID, *a2.PreviousBindingID, "latest A binding must point at intermediate B binding as its previous head")

	list, err := repo.ListServiceReleases(ctx, orgID, serviceID)
	require.NoError(t, err)
	require.Equal(t, 3, len(list), "A->B->A must produce three durable bindings")

	rollback, err := repo.GetRollbackRelease(ctx, orgID, "agent-a", serviceID, "stable")
	require.NoError(t, err)
	require.NotNil(t, rollback)
	require.Equal(t, b1.ID, rollback.Binding.ID, "rollback from latest A must resolve to the intermediate B binding")
	require.Equal(t, rB.ID, rollback.Release.ID)
}

// TestAgentRuntimeReleasePostgresConcurrentAppendersProduceLinearHistory
// proves that N concurrent producers appending to the same
// (org, agent, service, channel) chain end with a strictly linear
// previous_binding_id chain — no forks, no missing rows, and each binding's
// previous_binding_id points to a real prior binding in the same chain.
func TestAgentRuntimeReleasePostgresConcurrentAppendersProduceLinearHistory(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL concurrent linear-history test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	orgID, serviceID, sourceID := bootstrapPromotionFixtures(ctx, t, pool)
	repo := repository.NewPgAgentRuntimeReleaseRepository(pool)

	// Pre-seed 4 releases; each concurrent producer will pick one to bind.
	releases := make([]*domain.AgentRuntimeRelease, 4)
	for i := 0; i < 4; i++ {
		releases[i] = integrationRelease(orgID, sourceID, fmt.Sprintf("%d", i+3))
		require.NoError(t, repo.CreateRelease(ctx, releases[i]))
	}

	const producers = 16
	var wg sync.WaitGroup
	errs := make(chan error, producers)
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := &domain.AgentServiceReleaseBinding{
				OrgID:          orgID,
				AgentID:        "agent-a",
				ServiceID:      serviceID,
				ReleaseID:      releases[i%len(releases)].ID,
				ReleaseChannel: "stable",
				SourceEventID:  fmt.Sprintf("concurrent-%d-%s", i, uuid.NewString()),
			}
			if err := repo.BindRelease(ctx, b); err != nil {
				errs <- fmt.Errorf("producer %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}

	list, err := repo.ListServiceReleases(ctx, orgID, serviceID)
	require.NoError(t, err)
	require.Equal(t, producers, len(list), "each concurrent producer must have appended exactly one binding")

	byID := make(map[uuid.UUID]domain.AgentServiceReleaseBinding, len(list))
	for _, item := range list {
		byID[item.Binding.ID] = item.Binding
	}
	roots := 0
	referenced := make(map[uuid.UUID]bool, len(list))
	for _, item := range list {
		if item.Binding.PreviousBindingID == nil {
			roots++
			continue
		}
		prev, ok := byID[*item.Binding.PreviousBindingID]
		require.True(t, ok, "binding %s previous %s missing from chain", item.Binding.ID, item.Binding.PreviousBindingID)
		require.False(t, referenced[prev.ID], "binding %s referenced twice as previous -> chain forked", prev.ID)
		referenced[prev.ID] = true
	}
	require.Equal(t, 1, roots, "chain must have exactly one root binding under concurrency")
	require.Equal(t, producers-1, len(referenced), "each non-root binding must have exactly one distinct predecessor")
}

// TestAgentRuntimeReleasePostgresSameSourceEventIdempotent asserts that
// replays of the same source_event_id return the stored binding and never
// append a duplicate row.
func TestAgentRuntimeReleasePostgresSameSourceEventIdempotent(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL source_event_id idempotency test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	orgID, serviceID, sourceID := bootstrapPromotionFixtures(ctx, t, pool)
	repo := repository.NewPgAgentRuntimeReleaseRepository(pool)
	rel := integrationRelease(orgID, sourceID, "7")
	require.NoError(t, repo.CreateRelease(ctx, rel))

	event := "promo-once-" + uuid.NewString()
	first := &domain.AgentServiceReleaseBinding{OrgID: orgID, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: rel.ID, ReleaseChannel: "stable", SourceEventID: event}
	require.NoError(t, repo.BindRelease(ctx, first))
	replay := &domain.AgentServiceReleaseBinding{OrgID: orgID, AgentID: "agent-a", ServiceID: serviceID, ReleaseID: rel.ID, ReleaseChannel: "stable", SourceEventID: event}
	require.NoError(t, repo.BindRelease(ctx, replay))
	require.Equal(t, first.ID, replay.ID, "replayed source_event_id must return the stored binding")

	list, err := repo.ListServiceReleases(ctx, orgID, serviceID)
	require.NoError(t, err)
	require.Equal(t, 1, len(list), "same source_event_id must not append a duplicate binding")
}
