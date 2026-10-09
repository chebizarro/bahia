//go:build integration

package repository_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// This rehearsal owns its databases and Docker container. It never accepts a
// caller-supplied PostgreSQL URL, so it cannot restore over an operator DB.
func TestF74aPostgres16BackupRestoreAfterUnitRetirement(t *testing.T) {
	if os.Getenv("BAHIA_F74A_RESTORE_CONFIRM") != "disposable" {
		t.Skip("set BAHIA_F74A_RESTORE_CONFIRM=disposable to run the isolated PostgreSQL 16 rehearsal")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	container := "bahia-f74a-restore-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	docker := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	_, err := docker("run", "--detach", "--rm", "--name", container,
		"--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD=f74a-disposable", "postgres:16-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		cmd := exec.Command("docker", "rm", "--force", container)
		out, cleanupErr := cmd.CombinedOutput()
		require.NoError(t, cleanupErr, string(out))
	})
	portOutput, err := docker("port", container, "5432/tcp")
	require.NoError(t, err)
	portLine := strings.Split(portOutput, "\n")[0]
	port := portLine[strings.LastIndex(portLine, ":")+1:]
	dsn := func(database string) string {
		return fmt.Sprintf("postgres://postgres:f74a-disposable@127.0.0.1:%s/%s?sslmode=disable", port, database)
	}
	var admin *pgxpool.Pool
	for ctx.Err() == nil {
		admin, err = pgxpool.New(ctx, dsn("postgres"))
		if err == nil {
			err = admin.Ping(ctx)
			if err == nil {
				break
			}
			admin.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.NoError(t, err, "PostgreSQL 16 did not become ready")
	defer admin.Close()
	var major int
	require.NoError(t, admin.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&major))
	require.Equal(t, 16, major)
	_, err = admin.Exec(ctx, `CREATE DATABASE f74a_source`)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, `CREATE DATABASE f74a_restored`)
	require.NoError(t, err)
	source, err := pgxpool.New(ctx, dsn("f74a_source"))
	require.NoError(t, err)
	defer source.Close()
	require.NoError(t, db.Migrate(ctx, source, zap.NewNop()))

	serviceID, environmentID, unitID := uuid.New(), uuid.New(), uuid.New()
	_, err = source.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "restore-"+serviceID.String())
	require.NoError(t, err)
	_, err = source.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "restore-"+environmentID.String())
	require.NoError(t, err)
	_, err = source.Exec(ctx, `INSERT INTO deployment_units
		(id,environment_id,unit_key,runtime_type,reconcile_mode,ownership_mode)
		VALUES ($1,$2,'unit','docker','observe_only','external')`, unitID, environmentID)
	require.NoError(t, err)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	obsRepo := repository.NewPgRuntimeObservationRepository(source)
	for i, id := range ids {
		obs := &domain.RuntimeObservation{
			ID: id, ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
			ObservedImageDigest: "sha256:aaaa", ObservedImageRepo: "registry.example/test",
			ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
			HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{"sequence": i},
			ObservedAt: base.Add(time.Duration(i+1) * time.Hour),
		}
		require.NoError(t, obsRepo.Create(ctx, obs))
	}
	archive := repository.NewPgF74aObservationArchiveRepository(source)
	run, err := archive.StartRun(ctx, base.Add(24*time.Hour), 3)
	require.NoError(t, err)
	batch, err := archive.ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Archived)
	_, err = source.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1`, ids[0])
	require.NoError(t, err)
	require.NoError(t, repository.NewPgDeploymentUnitRepository(source).DeleteIfUnreferenced(ctx, unitID))
	_, err = source.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
		VALUES ($1,$2,$3)`, serviceID, environmentID, ids[1])
	require.NoError(t, err)

	// The receipt includes both archived-only and state-linked IDs, their row
	// digests, the original unit FK, the retired unit, and the hot/linked view.
	loadReceipt := func(pool *pgxpool.Pool) []string {
		t.Helper()
		rows, queryErr := pool.Query(ctx, `SELECT a.id::text, encode(a.row_digest,'hex'),
			a.deployment_unit_id::text, (u.retired_at IS NOT NULL)::text,
			(h.id IS NOT NULL)::text, (s.current_observation_id=a.id)::text,
			(f74a_observation_digest(to_jsonb(a),a.observed_at)=a.row_digest)::text
			FROM runtime_observation_archive a
			JOIN deployment_units u ON u.id=a.deployment_unit_id
			LEFT JOIN runtime_observations h ON h.id=a.id
			LEFT JOIN environment_service_state s ON s.service_id=a.service_id AND s.environment_id=a.environment_id
			ORDER BY a.id`)
		require.NoError(t, queryErr)
		defer rows.Close()
		var receipt []string
		for rows.Next() {
			parts := make([]string, 7)
			values := make([]any, len(parts))
			for i := range parts {
				values[i] = &parts[i]
			}
			require.NoError(t, rows.Scan(values...))
			require.Equal(t, "true", parts[6], "archive row digest must verify")
			receipt = append(receipt, strings.Join(parts, ":"))
		}
		require.NoError(t, rows.Err())
		return receipt
	}
	sourceReceipt := loadReceipt(source)
	require.Len(t, sourceReceipt, 2)
	var linked, archivedOnly int
	for _, entry := range sourceReceipt {
		require.Contains(t, entry, unitID.String()+":true:")
		if strings.Contains(entry, ":true:true:true") {
			linked++
		}
		if strings.Contains(entry, ":false:false:true") {
			archivedOnly++
		}
	}
	require.Equal(t, 1, linked, "one archived observation must retain its state link")
	require.Equal(t, 1, archivedOnly, "one observation must exist only in the archive")
	for _, id := range ids[1:] {
		archived, getErr := archive.GetArchivedByID(ctx, id)
		require.NoError(t, getErr)
		require.Equal(t, &unitID, archived.Observation.DeploymentUnitID)
	}
	_, err = docker("exec", container, "pg_dump", "--username=postgres", "--format=custom",
		"--file=/tmp/f74a.dump", "f74a_source")
	require.NoError(t, err)
	dumpDigest, err := docker("exec", container, "sha256sum", "/tmp/f74a.dump")
	require.NoError(t, err)
	t.Logf("PostgreSQL %d custom-format backup: %s", major, dumpDigest)

	// A restore collision must roll back as one transaction, leaving no
	// partially restored archive. The same dump then restores into the clean DB.
	target, err := pgxpool.New(ctx, dsn("f74a_restored"))
	require.NoError(t, err)
	defer target.Close()
	_, err = target.Exec(ctx, `CREATE TABLE services (id integer)`)
	require.NoError(t, err)
	_, err = docker("exec", container, "pg_restore", "--username=postgres", "--dbname=f74a_restored",
		"--single-transaction", "--exit-on-error", "/tmp/f74a.dump")
	require.Error(t, err, "collision must interrupt and roll back restore")
	var archiveTable *string
	require.NoError(t, target.QueryRow(ctx, `SELECT to_regclass('public.runtime_observation_archive')::text`).Scan(&archiveTable))
	require.Nil(t, archiveTable, "failed restore must not leave an archive behind")
	_, err = target.Exec(ctx, `DROP TABLE services`)
	require.NoError(t, err)
	_, err = docker("exec", container, "pg_restore", "--username=postgres", "--dbname=f74a_restored",
		"--single-transaction", "--exit-on-error", "/tmp/f74a.dump")
	require.NoError(t, err)
	require.Equal(t, sourceReceipt, loadReceipt(target), "restored IDs, digests, unit FK, retirement and state link must match source")

	// Aborting rehydration does not change the restored receipt. A committed
	// restore retains the immutable archive and moves the state link by ID.
	tx, err := target.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO runtime_observations
		(id,service_id,environment_id,deployment_unit_id,observed_image_digest,observed_image_repo,
		 observed_container_id,observed_host,observed_version,health_status,source,metadata,normalized_state,normalized_hash,observed_at)
		SELECT id,service_id,environment_id,deployment_unit_id,observed_image_digest,observed_image_repo,
		 observed_container_id,observed_host,observed_version,health_status,source,metadata,normalized_state,normalized_hash,observed_at
		FROM runtime_observation_archive WHERE id=$1`, ids[2])
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.Equal(t, sourceReceipt, loadReceipt(target), "aborted rehydration must leave no hot row")
	restoredArchive := repository.NewPgF74aObservationArchiveRepository(target)
	require.NoError(t, restoredArchive.RestoreByID(ctx, ids[2]))
	_, err = target.Exec(ctx, `UPDATE environment_service_state SET current_observation_id=$1
		WHERE service_id=$2 AND environment_id=$3`, ids[2], serviceID, environmentID)
	require.NoError(t, err)
	var linkID, hotUnitID uuid.UUID
	var digestEqual bool
	require.NoError(t, target.QueryRow(ctx, `SELECT current_observation_id FROM environment_service_state
		WHERE service_id=$1 AND environment_id=$2`, serviceID, environmentID).Scan(&linkID))
	require.Equal(t, ids[2], linkID)
	require.NoError(t, target.QueryRow(ctx, `SELECT h.deployment_unit_id,
		f74a_observation_digest(to_jsonb(h),h.observed_at)=a.row_digest
		FROM runtime_observations h JOIN runtime_observation_archive a USING (id) WHERE h.id=$1`, ids[2]).Scan(&hotUnitID, &digestEqual))
	require.Equal(t, unitID, hotUnitID)
	require.True(t, digestEqual)
	var archivedCount int
	require.NoError(t, target.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=ANY($1)`, ids[1:]).Scan(&archivedCount))
	require.Equal(t, 2, archivedCount)
	for _, id := range ids[1:] {
		original, getErr := archive.GetArchivedByID(ctx, id)
		require.NoError(t, getErr)
		restored, getErr := restoredArchive.GetArchivedByID(ctx, id)
		require.NoError(t, getErr)
		require.Equal(t, original, restored, "committed rehydration must not change archived ID, digest or provenance")
	}
	_, err = db.Down(ctx, target, zap.NewNop(), db.DownOptions{Confirm: true})
	require.ErrorContains(t, err, "cannot roll back F74a unit tombstones")
}
