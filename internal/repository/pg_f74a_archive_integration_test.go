//go:build integration

package repository_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/db"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func f74aArchiveDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL")
	require.NotEmpty(t, dsn, "integration requires disposable BAHIA_MIGRATE_TEST_DATABASE_URL")
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	schema := "f74a_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, cleanupErr := admin.Exec(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, cleanupErr)
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	return pool, schema
}

func waitF74aAdvisoryWaiters(t *testing.T, ctx context.Context, pool *pgxpool.Pool, minimum int) {
	t.Helper()
	for {
		var waiting int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory'
			AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&waiting))
		if waiting >= minimum {
			return
		}
		require.NoError(t, ctx.Err(), "waiting for PostgreSQL advisory-lock queue")
	}
}

func TestF74aArchiveAndDirectStateLinkBothLockOrders(t *testing.T) {
	for _, order := range []string{"state-first", "archive-first"} {
		t.Run(order, func(t *testing.T) {
			pool, _ := f74aArchiveDatabase(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			serviceID, environmentID := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "lock-"+serviceID.String())
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "lock-"+environmentID.String())
			require.NoError(t, err)
			base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
			obsRepo := repository.NewPgRuntimeObservationRepository(pool)
			for _, hour := range []int{1, 3} {
				obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
					ObservedImageDigest: "sha256:aaaa", ObservedImageRepo: "registry.example/test",
					ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
					HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{},
					ObservedAt: base.Add(time.Duration(hour) * time.Hour)}
				require.NoError(t, obsRepo.Create(ctx, obs))
			}
			history, err := obsRepo.ListByServiceEnv(ctx, serviceID, environmentID, 2)
			require.NoError(t, err)
			candidate := history[0]
			archiveRepo := repository.NewPgF74aObservationArchiveRepository(pool)
			run, err := archiveRepo.StartRun(ctx, base.Add(24*time.Hour), 2)
			require.NoError(t, err)
			archiveDone := make(chan error, 1)
			linkDone := make(chan error, 1)
			if order == "state-first" {
				stateTx, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = stateTx.Rollback(context.Background()) }()
				_, err = stateTx.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
					VALUES ($1,$2,$3)`, serviceID, environmentID, candidate.ID)
				require.NoError(t, err)
				go func() { _, archiveErr := archiveRepo.ArchiveNextBatch(ctx, run.ID); archiveDone <- archiveErr }()
				waitF74aAdvisoryWaiters(t, ctx, pool, 1)
				require.NoError(t, stateTx.Commit(ctx))
				require.NoError(t, <-archiveDone)
			} else {
				_, err = pool.Exec(ctx, `CREATE FUNCTION f74a_test_pause_archive() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN PERFORM pg_advisory_xact_lock(740199); RETURN NEW; END $$`)
				require.NoError(t, err)
				_, err = pool.Exec(ctx, `CREATE TRIGGER f74a_test_pause BEFORE INSERT ON runtime_observation_archive
					FOR EACH ROW EXECUTE FUNCTION f74a_test_pause_archive()`)
				require.NoError(t, err)
				pauseTx, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = pauseTx.Rollback(context.Background()) }()
				_, err = pauseTx.Exec(ctx, `SELECT pg_advisory_xact_lock(740199)`)
				require.NoError(t, err)
				go func() { _, archiveErr := archiveRepo.ArchiveNextBatch(ctx, run.ID); archiveDone <- archiveErr }()
				waitF74aAdvisoryWaiters(t, ctx, pool, 1)
				go func() {
					_, linkErr := pool.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
						VALUES ($1,$2,$3)`, serviceID, environmentID, candidate.ID)
					linkDone <- linkErr
				}()
				waitF74aAdvisoryWaiters(t, ctx, pool, 2)
				require.NoError(t, pauseTx.Commit(ctx))
				require.NoError(t, <-archiveDone)
				require.NoError(t, <-linkDone)
			}
			var hotCount int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, candidate.ID).Scan(&hotCount))
			require.Equal(t, 1, hotCount)
			var linked uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT current_observation_id FROM environment_service_state
				WHERE service_id=$1 AND environment_id=$2`, serviceID, environmentID).Scan(&linked))
			require.Equal(t, candidate.ID, linked)
		})
	}
}

func TestF74aArchiveCancellationRollsBackCopyAndCursor(t *testing.T) {
	pool, _ := f74aArchiveDatabase(t)
	ctx := t.Context()
	serviceID, environmentID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "abort-"+serviceID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "abort-"+environmentID.String())
	require.NoError(t, err)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	obsRepo := repository.NewPgRuntimeObservationRepository(pool)
	var candidate uuid.UUID
	for _, hour := range []int{1, 3} {
		obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
			ObservedImageDigest: "sha256:aaaa", ObservedImageRepo: "registry.example/test",
			ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
			HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{},
			ObservedAt: base.Add(time.Duration(hour) * time.Hour)}
		require.NoError(t, obsRepo.Create(ctx, obs))
		candidate = obs.ID
	}
	archiveRepo := repository.NewPgF74aObservationArchiveRepository(pool)
	run, err := archiveRepo.StartRun(ctx, base.Add(24*time.Hour), 2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE FUNCTION f74a_test_pause_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_xact_lock(740198); RETURN OLD; END $$`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TRIGGER f74a_test_pause BEFORE DELETE ON runtime_observations
		FOR EACH ROW EXECUTE FUNCTION f74a_test_pause_delete()`)
	require.NoError(t, err)
	pauseTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = pauseTx.Rollback(context.Background()) }()
	_, err = pauseTx.Exec(ctx, `SELECT pg_advisory_xact_lock(740198)`)
	require.NoError(t, err)
	abortCtx, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { _, archiveErr := archiveRepo.ArchiveNextBatch(abortCtx, run.ID); result <- archiveErr }()
	waitCtx, stopWaiting := context.WithTimeout(ctx, 10*time.Second)
	defer stopWaiting()
	waitF74aAdvisoryWaiters(t, waitCtx, pool, 1)
	cancel()
	require.Error(t, <-result)
	require.NoError(t, pauseTx.Commit(ctx))
	var hot, archived, examined, moved, batches int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, candidate).Scan(&hot))
	require.Equal(t, 1, hot)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=$1`, candidate).Scan(&archived))
	require.Zero(t, archived)
	require.NoError(t, pool.QueryRow(ctx, `SELECT examined_count,archived_count FROM f74a_observation_compaction_runs WHERE id=$1`, run.ID).Scan(&examined, &moved))
	require.Zero(t, examined)
	require.Zero(t, moved)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_observation_archive_batches WHERE run_id=$1`, run.ID).Scan(&batches))
	require.Zero(t, batches)
	batch, err := archiveRepo.ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, batch.Archived)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=$1`, candidate).Scan(&archived))
	require.Equal(t, 1, archived)
}

func TestF74aArchivePreservesHistoryAndRehydratesDelayedLink(t *testing.T) {
	pool, schema := f74aArchiveDatabase(t)
	ctx := t.Context()
	serviceID, environmentID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "archive-"+serviceID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "archive-"+environmentID.String())
	require.NoError(t, err)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	obsRepo := repository.NewPgRuntimeObservationRepository(pool)
	archiveRepo := repository.NewPgF74aObservationArchiveRepository(pool)
	makeObs := func(hour int, digest string) *domain.RuntimeObservation {
		return &domain.RuntimeObservation{
			ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
			ObservedImageDigest: digest, ObservedImageRepo: "registry.example/worker",
			ObservedContainerID: "container-1", ObservedHost: "host-1", ObservedVersion: "v1",
			HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{"sample": hour},
			ObservedAt: base.Add(time.Duration(hour) * time.Hour),
		}
	}
	a1, a3 := makeObs(1, "sha256:aaaa"), makeObs(3, "sha256:aaaa")
	require.NoError(t, obsRepo.Create(ctx, a1))
	require.NoError(t, obsRepo.Create(ctx, a3))
	before, err := obsRepo.ListByServiceEnv(ctx, serviceID, environmentID, 10)
	require.NoError(t, err)
	beforeCensus, err := repository.CensusF74a(ctx, pool, base.Add(24*time.Hour))
	require.NoError(t, err)
	run, err := archiveRepo.StartRun(ctx, base.Add(24*time.Hour), 2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE f74a_observation_compaction_runs SET cutoff=$2 WHERE id=$1`, run.ID, base.Add(12*time.Hour))
	require.ErrorContains(t, err, "cutoff and progress are immutable")
	batch, err := archiveRepo.ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Examined)
	require.Equal(t, 1, batch.Archived)
	var hotCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, a3.ID).Scan(&hotCount))
	require.Zero(t, hotCount)
	archived, err := archiveRepo.GetArchivedByID(ctx, a3.ID)
	require.NoError(t, err)
	require.NotNil(t, archived)
	require.Len(t, archived.Digest, 32)
	require.Equal(t, a3.ID, archived.Observation.ID)
	require.NotEqual(t, uuid.Nil, archived.BatchID)
	_, err = pool.Exec(ctx, `UPDATE f74a_observation_archive_batches SET archived_count=0 WHERE id=$1`, archived.BatchID)
	require.ErrorContains(t, err, "archive is immutable")
	auditPage, err := archiveRepo.ListArchivedAfter(ctx, serviceID, environmentID, time.Time{}, uuid.Nil, 1)
	require.NoError(t, err)
	require.Len(t, auditPage, 1)
	require.Equal(t, *archived, auditPage[0])
	require.NoError(t, archiveRepo.RestoreByID(ctx, a3.ID))
	require.NoError(t, archiveRepo.RestoreByID(ctx, a3.ID))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, a3.ID).Scan(&hotCount))
	require.Equal(t, 1, hotCount)
	_, err = pool.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1`, a3.ID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE runtime_observation_archive SET observed_image_digest='wrong' WHERE id=$1`, a3.ID)
	require.ErrorContains(t, err, "archive is immutable")
	_, err = pool.Exec(ctx, `DELETE FROM runtime_observation_archive WHERE id=$1`, a3.ID)
	require.ErrorContains(t, err, "archive is immutable")
	after, err := obsRepo.ListByServiceEnv(ctx, serviceID, environmentID, 10)
	require.NoError(t, err)
	require.Equal(t, before, after)
	latest, err := obsRepo.GetLatest(ctx, serviceID, environmentID)
	require.NoError(t, err)
	require.Equal(t, a3.ID, latest.ID)
	byID, err := obsRepo.GetByID(ctx, a3.ID)
	require.NoError(t, err)
	require.Equal(t, before[0], *byID)
	afterCensus, err := repository.CensusF74a(ctx, pool, base.Add(24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, beforeCensus, afterCensus)

	// A write behind the committed cursor turns archived A@t3 into the return
	// transition; union history and census must recover A→B→A immediately.
	b2 := makeObs(2, "sha256:bbbb")
	require.NoError(t, obsRepo.Create(ctx, b2))
	restarted, err := pgxpool.ParseConfig(os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL"))
	require.NoError(t, err)
	restarted.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	otherPool, err := pgxpool.NewWithConfig(ctx, restarted)
	require.NoError(t, err)
	defer otherPool.Close()
	secondBatch, err := repository.NewPgF74aObservationArchiveRepository(otherPool).ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.True(t, secondBatch.Complete)
	require.Zero(t, secondBatch.Archived)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, b2.ID).Scan(&hotCount))
	require.Equal(t, 1, hotCount)
	history, err := obsRepo.ListByServiceEnv(ctx, serviceID, environmentID, 10)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{a3.ID, b2.ID, a1.ID}, []uuid.UUID{history[0].ID, history[1].ID, history[2].ID})
	census, err := repository.CensusF74a(ctx, pool, base.Add(24*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 3, census.MaterialRuns)
	require.Zero(t, census.SuppressibleObservations)

	// Simulate reconciliation persisting the observation but failing its state
	// write, then retry using direct SQL. The trigger restores the exact FK row.
	_, err = pool.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
		VALUES ($1,$2,$3)`, serviceID, environmentID, a3.ID)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, a3.ID).Scan(&hotCount))
	require.Equal(t, 1, hotCount)
	var sameDigest bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT f74a_observation_digest(to_jsonb(h),h.observed_at)=a.row_digest
		FROM runtime_observations h JOIN runtime_observation_archive a USING (id) WHERE h.id=$1`, a3.ID).Scan(&sameDigest))
	require.True(t, sameDigest)
	_, err = pool.Exec(ctx, `UPDATE runtime_observations SET observed_image_digest='wrong' WHERE id=$1`, a3.ID)
	require.ErrorContains(t, err, "conflicts with immutable archive")
	var examined, moved, journalRows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT examined_count,archived_count FROM f74a_observation_compaction_runs WHERE id=$1`, run.ID).Scan(&examined, &moved))
	require.Equal(t, 2, examined)
	require.Equal(t, 1, moved)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_observation_archive_batches WHERE run_id=$1`, run.ID).Scan(&journalRows))
	require.Equal(t, 1, journalRows)
	_, err = pool.Exec(ctx, `DELETE FROM services WHERE id=$1`, serviceID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM environments WHERE id=$1`, environmentID)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id IN ($1,$2)`, a1.ID, a3.ID).Scan(&hotCount))
	require.Zero(t, hotCount)
	archivedAfterCascade, err := archiveRepo.GetArchivedByID(ctx, a3.ID)
	require.NoError(t, err)
	require.Equal(t, *archived, *archivedAfterCascade)
}

func TestF74aArchivedAuditRowDoesNotPinDeploymentUnit(t *testing.T) {
	pool, _ := f74aArchiveDatabase(t)
	ctx := t.Context()
	serviceID, environmentID, unitID := uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "unit-"+serviceID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "unit-"+environmentID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO deployment_units
		(id,environment_id,unit_key,runtime_type,reconcile_mode,ownership_mode)
		VALUES ($1,$2,'unit','docker','observe_only','external')`, unitID, environmentID)
	require.NoError(t, err)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	obsRepo := repository.NewPgRuntimeObservationRepository(pool)
	var first, archivedID uuid.UUID
	for _, hour := range []int{1, 3} {
		obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
			DeploymentUnitID: &unitID, ObservedImageDigest: "sha256:aaaa", ObservedImageRepo: "registry.example/test",
			ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
			HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{},
			ObservedAt: base.Add(time.Duration(hour) * time.Hour)}
		require.NoError(t, obsRepo.Create(ctx, obs))
		if hour == 1 {
			first = obs.ID
		} else {
			archivedID = obs.ID
		}
	}
	unitRepo := repository.NewPgDeploymentUnitRepository(pool)
	require.ErrorIs(t, unitRepo.DeleteIfUnreferenced(ctx, unitID), repository.ErrConflict,
		"a live hot observation must protect its deployment unit")
	archiveRepo := repository.NewPgF74aObservationArchiveRepository(pool)
	run, err := archiveRepo.StartRun(ctx, base.Add(24*time.Hour), 2)
	require.NoError(t, err)
	batch, err := archiveRepo.ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, 1, batch.Archived)
	_, err = pool.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1`, first)
	require.NoError(t, err)
	require.NoError(t, unitRepo.DeleteIfUnreferenced(ctx, unitID),
		"an immutable audit row is not a live deployment reference")
	var unitExists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deployment_units WHERE id=$1)`, unitID).Scan(&unitExists))
	require.False(t, unitExists)
	archived, err := archiveRepo.GetArchivedByID(ctx, archivedID)
	require.NoError(t, err)
	require.NotNil(t, archived)
	require.Equal(t, &unitID, archived.Observation.DeploymentUnitID,
		"archive must retain the original placement identity after unit deletion")
	assertMissingUnitFK := func(err error) {
		t.Helper()
		require.Error(t, err)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr))
		require.Equal(t, "23503", pgErr.Code, "identity-preserving hot restore must fail closed")
	}
	assertMissingUnitFK(archiveRepo.RestoreByID(ctx, archivedID))
	_, err = pool.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
		VALUES ($1,$2,$3)`, serviceID, environmentID, archivedID)
	assertMissingUnitFK(err)
	var hotCount, stateCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, archivedID).Scan(&hotCount))
	require.Zero(t, hotCount)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM environment_service_state
		WHERE service_id=$1 AND environment_id=$2`, serviceID, environmentID).Scan(&stateCount))
	require.Zero(t, stateCount)
	retained, err := archiveRepo.GetArchivedByID(ctx, archivedID)
	require.NoError(t, err)
	require.Equal(t, *archived, *retained)
}

func TestF74aBackdatedInsertAndArchiveBothLockOrders(t *testing.T) {
	for _, order := range []string{"insert-first", "archive-first"} {
		t.Run(order, func(t *testing.T) {
			pool, schema := f74aArchiveDatabase(t)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			serviceID, environmentID := uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "backdate-"+serviceID.String())
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "backdate-"+environmentID.String())
			require.NoError(t, err)
			base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
			obsRepo := repository.NewPgRuntimeObservationRepository(pool)
			a1, a3 := uuid.New(), uuid.New()
			insert := func(exec interface {
				Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
			}, id uuid.UUID, hour, minute int, digest string) error {
				_, err := exec.Exec(ctx, `INSERT INTO runtime_observations
					(id,service_id,environment_id,observed_image_digest,observed_image_repo,observed_container_id,
					 observed_host,observed_version,health_status,source,metadata,observed_at)
					VALUES ($1,$2,$3,$4,'registry.example/test','container','host','v1','healthy','runtime','{}',$5)`,
					id, serviceID, environmentID, digest, base.Add(time.Duration(hour)*time.Hour+time.Duration(minute)*time.Minute))
				return err
			}
			require.NoError(t, insert(pool, a1, 1, 0, "sha256:aaaa"))
			require.NoError(t, insert(pool, a3, 3, 0, "sha256:aaaa"))
			archiveRepo := repository.NewPgF74aObservationArchiveRepository(pool)
			run, err := archiveRepo.StartRun(ctx, base.Add(24*time.Hour), 2)
			require.NoError(t, err)
			b2 := uuid.New()
			moveDone := make(chan struct {
				batch repository.F74aArchiveBatch
				err   error
			}, 1)
			insertDone := make(chan error, 1)
			if order == "insert-first" {
				writeTx, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = writeTx.Rollback(context.Background()) }()
				require.NoError(t, insert(writeTx, b2, 2, 0, "sha256:bbbb"))
				go func() {
					batch, moveErr := archiveRepo.ArchiveNextBatch(ctx, run.ID)
					moveDone <- struct {
						batch repository.F74aArchiveBatch
						err   error
					}{batch, moveErr}
				}()
				waitF74aAdvisoryWaiters(t, ctx, pool, 1)
				require.NoError(t, writeTx.Commit(ctx))
			} else {
				_, err = pool.Exec(ctx, `CREATE FUNCTION f74a_test_pause_archive() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN PERFORM pg_advisory_xact_lock(740197); RETURN NEW; END $$`)
				require.NoError(t, err)
				_, err = pool.Exec(ctx, `CREATE TRIGGER f74a_test_pause BEFORE INSERT ON runtime_observation_archive
					FOR EACH ROW EXECUTE FUNCTION f74a_test_pause_archive()`)
				require.NoError(t, err)
				pauseTx, err := pool.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = pauseTx.Rollback(context.Background()) }()
				_, err = pauseTx.Exec(ctx, `SELECT pg_advisory_xact_lock(740197)`)
				require.NoError(t, err)
				go func() {
					batch, moveErr := archiveRepo.ArchiveNextBatch(ctx, run.ID)
					moveDone <- struct {
						batch repository.F74aArchiveBatch
						err   error
					}{batch, moveErr}
				}()
				waitF74aAdvisoryWaiters(t, ctx, pool, 1)
				go func() { insertDone <- insert(pool, b2, 2, 0, "sha256:bbbb") }()
				waitF74aAdvisoryWaiters(t, ctx, pool, 2)
				require.NoError(t, pauseTx.Commit(ctx))
			}
			moved := <-moveDone
			require.NoError(t, moved.err)
			if order == "insert-first" {
				require.Zero(t, moved.batch.Archived)
			} else {
				require.Equal(t, 1, moved.batch.Archived)
				require.NoError(t, <-insertDone)
			}
			// The first cursor cannot see a write behind it. Completion only means
			// that pass has exhausted its cursor, not that retention is authorized.
			end, err := archiveRepo.ArchiveNextBatch(ctx, run.ID)
			require.NoError(t, err)
			require.True(t, end.Complete)
			var b2Hot int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, b2).Scan(&b2Hot))
			require.Equal(t, 1, b2Hot)
			var a3Archived int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=$1`, a3).Scan(&a3Archived))
			if order == "insert-first" {
				require.Zero(t, a3Archived)
			} else {
				require.Equal(t, 1, a3Archived)
			}
			// A late duplicate also lands behind the first cursor. A new pass at
			// the original cutoff must examine and archive it exactly once.
			b25 := uuid.New()
			require.NoError(t, insert(pool, b25, 2, 30, "sha256:bbbb"))
			restartConfig, err := pgxpool.ParseConfig(os.Getenv("BAHIA_MIGRATE_TEST_DATABASE_URL"))
			require.NoError(t, err)
			restartConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
			restartedPool, err := pgxpool.NewWithConfig(ctx, restartConfig)
			require.NoError(t, err)
			defer restartedPool.Close()
			restartedArchive := repository.NewPgF74aObservationArchiveRepository(restartedPool)
			secondRun, err := restartedArchive.StartRun(ctx, run.Cutoff, 4)
			require.NoError(t, err)
			second, err := restartedArchive.ArchiveNextBatch(ctx, secondRun.ID)
			require.NoError(t, err)
			require.Equal(t, 1, second.Archived)
			archiveOfB25, err := archiveRepo.GetArchivedByID(ctx, b25)
			require.NoError(t, err)
			require.NotNil(t, archiveOfB25)
			var b25Hot int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, b25).Scan(&b25Hot))
			require.Zero(t, b25Hot)
			history, err := obsRepo.ListByServiceEnv(ctx, serviceID, environmentID, 10)
			require.NoError(t, err)
			require.Len(t, history, 4)
			require.Equal(t, []uuid.UUID{a3, b25, b2, a1}, []uuid.UUID{history[0].ID, history[1].ID, history[2].ID, history[3].ID})
			census, err := repository.CensusF74a(ctx, pool, run.Cutoff)
			require.NoError(t, err)
			require.EqualValues(t, 3, census.MaterialRuns)
			require.EqualValues(t, 1, census.SuppressibleObservations)
		})
	}
}
