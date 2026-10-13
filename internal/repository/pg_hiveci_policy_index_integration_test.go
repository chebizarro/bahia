package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// Run with DATABASE_URL pointing to a disposable PostgreSQL 16 instance.
func TestPgHiveCIPolicyIndexReconcilesLegacyIDsAndRetainsReleaseReferences(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; requires disposable PostgreSQL 16")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "hiveci_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	defer func() {
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TABLE hiveci_pipeline_policies (
		id UUID PRIMARY KEY, repo_coordinate TEXT NOT NULL, workflow_path TEXT NOT NULL,
		branch_pattern TEXT, service_id UUID NOT NULL, environment_id UUID NOT NULL,
		enabled BOOLEAN NOT NULL, metadata JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE (repo_coordinate, workflow_path, branch_pattern, service_id, environment_id));
		CREATE TABLE hiveci_accepted_releases (
		release_identity TEXT PRIMARY KEY,
		policy_id UUID REFERENCES hiveci_pipeline_policies(id) ON DELETE RESTRICT);`)
	require.NoError(t, err)
	legacyID, staleID, policyID := uuid.New(), uuid.New(), uuid.Must(uuid.NewV7())
	serviceID, envID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	_, err = pool.Exec(ctx, `INSERT INTO hiveci_pipeline_policies
		(id, repo_coordinate, workflow_path, service_id, environment_id, enabled, metadata, created_at, updated_at)
		VALUES ($1,'repo:old','ci.yml',$3,$4,true,'{}',$5,$5),
		($2,'repo:stale','ci.yml',$3,$4,true,'{}',$5,$5)`, legacyID, staleID, serviceID, envID, now)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO hiveci_accepted_releases VALUES ('historical-release',$1)`, legacyID)
	require.NoError(t, err)
	migration, err := os.ReadFile("../db/migrations/000086_hiveci_policy_index_archive.up.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err)
	repo := NewPgHiveCIRepository(pool)
	policy := domain.HiveCIPipelinePolicy{ID: policyID, RepoCoordinate: "repo:old", WorkflowPath: "ci.yml",
		ServiceID: serviceID, EnvironmentID: envID, Enabled: true, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, repo.EnsurePipelinePolicy(ctx, policy))
	assertActiveHiveCIPolicyIDs(t, repo, legacyID, staleID, policyID)
	var referenced uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT policy_id FROM hiveci_accepted_releases WHERE release_identity='historical-release'`).Scan(&referenced))
	require.Equal(t, legacyID, referenced, "historical release retains its original FK")
	var archived bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM hiveci_pipeline_policies WHERE id=$1`, legacyID).Scan(&archived))
	require.True(t, archived)

	policy.RepoCoordinate = "repo:renamed"
	policy.UpdatedAt = now.Add(time.Minute)
	require.NoError(t, repo.EnsurePipelinePolicy(ctx, policy))
	assertActiveHiveCIPolicyIDs(t, repo, legacyID, staleID, policyID)
	require.NoError(t, repo.ReconcilePipelinePolicies(ctx, []domain.HiveCIPipelinePolicy{policy}))
	listed, err := repo.ListPolicies(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, policyID, listed[0].ID)
	require.NoError(t, repo.DeletePipelinePolicy(ctx, policyID))
	listed, err = repo.ListPolicies(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
	require.NoError(t, repo.ReconcilePipelinePolicies(ctx, nil))
	require.NoError(t, pool.QueryRow(ctx, `SELECT policy_id FROM hiveci_accepted_releases WHERE release_identity='historical-release'`).Scan(&referenced))
	require.Equal(t, legacyID, referenced)
}

func assertActiveHiveCIPolicyIDs(t *testing.T, repo *PgHiveCIRepository, legacyID, staleID, policyID uuid.UUID) {
	t.Helper()
	listed, err := repo.ListPolicies(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.NotEqual(t, legacyID, listed[0].ID)
	require.NotEqual(t, legacyID, listed[1].ID)
	require.ElementsMatch(t, []uuid.UUID{staleID, policyID}, []uuid.UUID{listed[0].ID, listed[1].ID})
}
