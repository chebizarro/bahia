//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	f74aScalePackageRows      = 20_000
	f74aScaleSemanticPackages = 10_000
	f74aScaleObservations     = 1_500_000
	f74aScaleObservationBatch = 1_000
)

// TestF74aScaleFixture exercises the real schema at the acceptance-scale row
// counts. It is independent of the backfill interfaces and verifies the
// database fixture shape, not publisher, relay, scheduler, or compaction behavior.
func TestF74aScaleFixture(t *testing.T) {
	url := os.Getenv("BAHIA_F74A_SCALE_DATABASE_URL")
	if url == "" {
		t.Skip("set BAHIA_F74A_SCALE_DATABASE_URL to an isolated disposable PostgreSQL database")
	}
	if os.Getenv("BAHIA_F74A_SCALE_CONFIRM") != "disposable" {
		t.Fatal("set BAHIA_F74A_SCALE_CONFIRM=disposable after verifying the database is isolated and disposable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer admin.Close()
	schema := "f74a_scale_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		_, cleanupErr := admin.Exec(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr)
	}()
	config, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	seedStart := time.Now()

	serviceID, environmentID := uuid.New(), uuid.New()
	buildID, artifactID, sbomID := uuid.New(), uuid.New(), uuid.New()
	name := "f74a-scale-" + uuid.NewString()
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO services (id, name, artifact_repo) VALUES ($1, $2, $3)`, []any{serviceID, name, "example.invalid/f74a-scale"}},
		{`INSERT INTO environments (id, name) VALUES ($1, $2)`, []any{environmentID, name}},
		{`INSERT INTO builds (id, service_id, git_sha, git_ref, ci_run_id, status)
		  VALUES ($1, $2, $3, $4, $5, $6)`, []any{buildID, serviceID, "fixture", "fixture", name, "success"}},
		{`INSERT INTO artifacts (id, build_id, service_id, image_repo, image_tag, image_digest)
		  VALUES ($1, $2, $3, $4, $5, $6)`, []any{artifactID, buildID, serviceID, "example.invalid/f74a-scale", "fixture", "sha256:" + fmt.Sprintf("%064x", artifactID)}},
		{`INSERT INTO artifact_sboms (id, artifact_id, format, package_count)
		  VALUES ($1, $2, $3, $4)`, []any{sbomID, artifactID, "spdx", f74aScalePackageRows}},
	} {
		_, err := pool.Exec(ctx, row.query, row.args...)
		require.NoError(t, err)
	}

	// Two physical rows for each exact semantic package coordinate. One
	// nullable field exercises the NULL-to-empty identity rule.
	_, err = pool.Exec(ctx, `INSERT INTO sbom_packages
		(sbom_id, name, version, ecosystem, license, purl, cpe)
		SELECT $1, 'package-' || (n % $2)::text, '1.0', NULL, '', NULL, NULL
		FROM generate_series(0, $3::int - 1) AS n`,
		sbomID, f74aScaleSemanticPackages, f74aScalePackageRows)
	require.NoError(t, err)

	// Each statement commits at most 1,000 per-ID advisory locks. A single
	// 1.5-million-row transaction exhausts PostgreSQL's shared lock table and
	// does not resemble the bounded production write path.
	packageSeedDuration := time.Since(seedStart)
	observedBase := time.Now().UTC().Add(-f74aScaleObservations * time.Second).Truncate(time.Microsecond)
	observationSeedStart := time.Now()
	for first := 1; first <= f74aScaleObservations; first += f74aScaleObservationBatch {
		last := min(first+f74aScaleObservationBatch-1, f74aScaleObservations)
		_, err = pool.Exec(ctx, `INSERT INTO runtime_observations
			(service_id, environment_id, observed_image_digest, health_status,
			 source, normalized_hash, observed_at)
			SELECT $1, $2, 'sha256:' || lpad((n / 100000)::text, 64, '0'),
			       'healthy', 'f74a-scale-fixture', (n / 100000)::text,
			       $5::timestamptz + n * interval '1 second'
			FROM generate_series($3::int, $4::int) AS n`,
			serviceID, environmentID, first, last, observedBase)
		require.NoError(t, err, "observation batch %d–%d", first, last)
	}
	observationSeedDuration := time.Since(observationSeedStart)
	_, err = pool.Exec(ctx, `INSERT INTO environment_service_state
		(service_id, environment_id, current_observation_id)
		SELECT $1, $2, id FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC, id DESC LIMIT 1`, serviceID, environmentID)
	require.NoError(t, err)

	var packages, coordinates, observations, linked int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT name)
		FROM sbom_packages WHERE sbom_id = $1`, sbomID).Scan(&packages, &coordinates))
	require.EqualValues(t, f74aScalePackageRows, packages)
	require.EqualValues(t, f74aScaleSemanticPackages, coordinates)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2`, serviceID, environmentID).Scan(&observations))
	require.EqualValues(t, f74aScaleObservations, observations)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*)
		FROM environment_service_state s
		JOIN runtime_observations o ON o.id = s.current_observation_id
		 AND o.service_id = s.service_id AND o.environment_id = s.environment_id
		WHERE s.service_id = $1 AND s.environment_id = $2`, serviceID, environmentID).Scan(&linked))
	require.EqualValues(t, 1, linked)

	// A current-state backfill should traverse state links, not all historical
	// observations. Keep this query independent of any repository method name.
	rows, err := pool.Query(ctx, `SELECT o.id FROM environment_service_state s
		JOIN runtime_observations o ON o.id = s.current_observation_id
		 AND o.service_id = s.service_id AND o.environment_id = s.environment_id
		WHERE (s.service_id, s.environment_id) > ($1, $2)
		ORDER BY s.service_id, s.environment_id LIMIT 250`, uuid.Nil, uuid.Nil)
	require.NoError(t, err)
	count := 0
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		count++
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.Equal(t, 1, count)
	censusStart := time.Now()
	census, err := repository.CensusF74a(ctx, pool, observedBase.Add((f74aScaleObservations+1)*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, f74aScalePackageRows, census.PackageRows)
	require.EqualValues(t, f74aScaleSemanticPackages, census.SemanticPackages)
	require.EqualValues(t, f74aScaleObservations, census.Observations)
	require.EqualValues(t, 1, census.LinkedObservations)
	require.EqualValues(t, 16, census.MaterialRuns)
	require.EqualValues(t, f74aScaleObservations-16, census.SuppressibleObservations)
	t.Logf("fixture verified: %d physical packages, %d semantic packages, %d historical observations, %d linked current observation; package seed=%s observation seed=%s census=%s batch=%d",
		packages, coordinates, observations, linked, packageSeedDuration, observationSeedDuration, time.Since(censusStart), f74aScaleObservationBatch)
}
