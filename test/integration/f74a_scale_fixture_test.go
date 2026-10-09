//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	f74aScalePackageRows      = 20_000
	f74aScaleSemanticPackages = 10_000
	f74aScaleObservations     = 1_500_000
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
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))

	// Rollback keeps the fixture from changing a reusable test database. The
	// disposable-database guard is still required: seeding and migration use
	// substantial space, WAL, and I/O before rollback finishes.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	require.NoError(t, err)
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer rollbackCancel()
		require.NoError(t, tx.Rollback(rollbackCtx))
	}()

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
		_, err := tx.Exec(ctx, row.query, row.args...)
		require.NoError(t, err)
	}

	// Two physical rows for each exact semantic package coordinate. One
	// nullable field exercises the NULL-to-empty identity rule.
	_, err = tx.Exec(ctx, `INSERT INTO sbom_packages
		(sbom_id, name, version, ecosystem, license, purl, cpe)
		SELECT $1, 'package-' || (n % $2)::text, '1.0', NULL, '', NULL, NULL
		FROM generate_series(0, $3::int - 1) AS n`,
		sbomID, f74aScaleSemanticPackages, f74aScalePackageRows)
	require.NoError(t, err)

	// Historical samples greatly outnumber canonical current links. Material
	// runs change periodically; the other samples repeat the same normalized
	// hash. A state row points to exactly one observation.
	_, err = tx.Exec(ctx, `INSERT INTO runtime_observations
		(service_id, environment_id, observed_image_digest, health_status,
		 source, normalized_hash, observed_at)
		SELECT $1, $2, 'sha256:' || lpad((n / 100000)::text, 64, '0'),
		       'healthy', 'f74a-scale-fixture', (n / 100000)::text,
		       now() - ($3::int - n) * interval '1 second'
		FROM generate_series(1, $3::int) AS n`,
		serviceID, environmentID, f74aScaleObservations)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO environment_service_state
		(service_id, environment_id, current_observation_id)
		SELECT $1, $2, id FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY observed_at DESC, id DESC LIMIT 1`, serviceID, environmentID)
	require.NoError(t, err)

	var packages, coordinates, observations, linked int64
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*), count(DISTINCT name)
		FROM sbom_packages WHERE sbom_id = $1`, sbomID).Scan(&packages, &coordinates))
	require.EqualValues(t, f74aScalePackageRows, packages)
	require.EqualValues(t, f74aScaleSemanticPackages, coordinates)
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM runtime_observations
		WHERE service_id = $1 AND environment_id = $2`, serviceID, environmentID).Scan(&observations))
	require.EqualValues(t, f74aScaleObservations, observations)
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*)
		FROM environment_service_state s
		JOIN runtime_observations o ON o.id = s.current_observation_id
		 AND o.service_id = s.service_id AND o.environment_id = s.environment_id
		WHERE s.service_id = $1 AND s.environment_id = $2`, serviceID, environmentID).Scan(&linked))
	require.EqualValues(t, 1, linked)

	// A current-state backfill should traverse state links, not all historical
	// observations. Keep this query independent of any repository method name.
	rows, err := tx.Query(ctx, `SELECT o.id FROM environment_service_state s
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
	t.Logf("fixture verified: %d physical packages, %d semantic packages, %d historical observations, %d linked current observation", packages, coordinates, observations, linked)
}
