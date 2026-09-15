package repository_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRuntimeReleaseDeploymentIntentPostgresFanoutReplayAndRollback(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL runtime release intent test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	orgID, serviceA, sourceID := bootstrapPromotionFixtures(ctx, t, pool)
	serviceB, environmentA, environmentB := uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO services (id,org_id,name,artifact_repo,default_branch,runtime_type) VALUES ($1,$2,$3,'agents/promotion','main','container')`, serviceB, orgID, "svc-"+serviceB.String()[:8])
	require.NoError(t, err)
	for id, name := range map[uuid.UUID]string{environmentA: "env-a-", environmentB: "env-b-"} {
		_, err = pool.Exec(ctx, `INSERT INTO environments (id,org_id,name) VALUES ($1,$2,$3)`, id, orgID, name+id.String()[:8])
		require.NoError(t, err)
	}

	releases := repository.NewPgAgentRuntimeReleaseRepository(pool)
	intents := repository.NewPgDeploymentIntentRepository(pool)
	releaseA := integrationRelease(orgID, sourceID, "6")
	releaseB := integrationRelease(orgID, sourceID, "7")
	require.NoError(t, releases.CreateRelease(ctx, releaseA))
	require.NoError(t, releases.CreateRelease(ctx, releaseB))
	bind := func(agent string, serviceID, releaseID uuid.UUID, event string) {
		t.Helper()
		require.NoError(t, releases.BindRelease(ctx, &domain.AgentServiceReleaseBinding{
			OrgID: orgID, AgentID: agent, ServiceID: serviceID, ReleaseID: releaseID,
			ReleaseChannel: "stable", SourceEventID: event + "-" + uuid.NewString(),
		}))
	}
	bind("agent-a", serviceA, releaseA.ID, "bind-a1")
	bind("agent-b", serviceB, releaseA.ID, "bind-b1")

	create := func(serviceID, environmentID uuid.UUID) *domain.DeploymentIntent {
		t.Helper()
		intent := &domain.DeploymentIntent{
			ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
			RequestedBy: "soul-factory-promotion", SourceKind: domain.SourceKindAutoPromote,
			ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
			Metadata: map[string]any{"runtime_release_id": releaseA.ID.String()},
		}
		created, createErr := intents.CreateForRuntimeRelease(ctx, releaseA.ID, intent)
		require.NoError(t, createErr)
		require.True(t, created)
		return intent
	}
	intentA := create(serviceA, environmentA)
	intentB := create(serviceB, environmentB)
	require.NotEqual(t, intentA.ID, intentB.ID)

	var artifactCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE service_id = ANY($1::uuid[])`, []uuid.UUID{serviceA, serviceB}).Scan(&artifactCount))
	require.Zero(t, artifactCount, "runtime release fan-out must not fabricate artifacts")

	replay := &domain.DeploymentIntent{
		ID: uuid.New(), ServiceID: serviceA, EnvironmentID: environmentA,
		RequestedBy: "soul-factory-promotion", SourceKind: domain.SourceKindAutoPromote,
		ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved,
	}
	created, err := intents.CreateForRuntimeRelease(ctx, releaseA.ID, replay)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, intentA.ID, replay.ID)

	bind("agent-a", serviceA, releaseB.ID, "bind-a2")
	rollback, err := releases.GetRollbackRelease(ctx, orgID, "agent-a", serviceA, "stable")
	require.NoError(t, err)
	require.NotNil(t, rollback)
	require.Equal(t, releaseA.ImageDigest, rollback.Release.ImageDigest)
	stored, err := intents.GetByServiceRuntimeRelease(ctx, serviceA, rollback.Release.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, intentA.ID, stored.ID)
}

func TestRuntimeReleaseDeploymentIntentMigrationPostgresDownAndUp(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL runtime release intent migration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	down, err := os.ReadFile("../db/migrations/000065_runtime_release_deployment_intents.down.sql")
	require.NoError(t, err)
	up, err := os.ReadFile("../db/migrations/000065_runtime_release_deployment_intents.up.sql")
	require.NoError(t, err)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, func() error { _, execErr := tx.Exec(ctx, string(down)); return execErr }())
	var count int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='deployment_intents' AND column_name='agent_runtime_release_id'`).Scan(&count))
	require.Zero(t, count)
	var nullable string
	require.NoError(t, tx.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name='deployment_intents' AND column_name='artifact_id'`).Scan(&nullable))
	require.Equal(t, "NO", nullable)

	require.NoError(t, func() error { _, execErr := tx.Exec(ctx, string(up)); return execErr }())
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='deployment_intents' AND column_name='agent_runtime_release_id'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, tx.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name='deployment_intents' AND column_name='artifact_id'`).Scan(&nullable))
	require.Equal(t, "YES", nullable)
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname='deployment_intents_service_runtime_release_uq'`).Scan(&count))
	require.Equal(t, 1, count)
}
