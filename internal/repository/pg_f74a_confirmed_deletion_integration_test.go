//go:build integration

package repository

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
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type f74aTestBatchProof struct {
	proof      F74aReceiptVerification
	calls      int
	fail       bool
	failOnCall int
}

func (p *f74aTestBatchProof) proveCurrentF74aBackup(context.Context) (F74aReceiptVerification, error) {
	p.calls++
	if p.fail || p.calls == p.failOnCall {
		return F74aReceiptVerification{}, fmt.Errorf("independent backup authority unavailable")
	}
	return p.proof, nil
}

func disposableF74aDeletionDB(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	if os.Getenv("BAHIA_F74A_RESTORE_CONFIRM") != "disposable" {
		t.Skip("set BAHIA_F74A_RESTORE_CONFIRM=disposable for isolated PostgreSQL 16 rehearsal")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	container := "bahia-f74a-delete-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	_, err := docker("run", "--detach", "--rm", "--name", container,
		"--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD=f74a-disposable", "postgres:16-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		out, cleanupErr := exec.CommandContext(cleanupCtx, "docker", "rm", "--force", container).CombinedOutput()
		require.NoError(t, cleanupErr, string(out))
	})
	portOutput, err := docker("port", container, "5432/tcp")
	require.NoError(t, err)
	portLine := strings.Split(portOutput, "\n")[0]
	port := portLine[strings.LastIndex(portLine, ":")+1:]
	dsn := fmt.Sprintf("postgres://postgres:f74a-disposable@127.0.0.1:%s/postgres?sslmode=disable", port)
	var pool *pgxpool.Pool
	for ctx.Err() == nil {
		pool, err = pgxpool.New(ctx, dsn)
		if err == nil {
			err = pool.Ping(ctx)
			if err == nil {
				break
			}
			pool.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	var major int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int / 10000`).Scan(&major))
	require.Equal(t, 16, major)
	require.NoError(t, db.Migrate(ctx, pool, zap.NewNop()))
	return pool, ctx
}

func f74aDeletionCandidates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, count int) ([]uuid.UUID, time.Time) {
	t.Helper()
	serviceID, environmentID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO services(id,name,artifact_repo) VALUES ($1,$2,'archive-test')`, serviceID, "delete-"+serviceID.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO environments(id,name) VALUES ($1,$2)`, environmentID, "delete-"+environmentID.String())
	require.NoError(t, err)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	ids := make([]uuid.UUID, count)
	obsRepo := NewPgRuntimeObservationRepository(pool)
	for i := range ids {
		ids[i] = uuid.New()
		obs := &domain.RuntimeObservation{ID: ids[i], ServiceID: serviceID, EnvironmentID: environmentID,
			ObservedImageDigest: "sha256:aaaa", ObservedImageRepo: "registry.example/test",
			ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
			HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{},
			ObservedAt: base.Add(time.Duration(i+1) * time.Hour)}
		require.NoError(t, obsRepo.Create(ctx, obs))
	}
	archive := NewPgF74aObservationArchiveRepository(pool)
	cutoff := base.Add(24 * time.Hour)
	run, err := archive.StartRun(ctx, cutoff, count)
	require.NoError(t, err)
	batch, err := archive.ArchiveNextBatch(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, count-1, batch.Archived)
	for _, id := range ids[1:] {
		require.NoError(t, archive.RestoreByID(ctx, id))
	}
	return ids, cutoff
}

func f74aProofForDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) *f74aTestBatchProof {
	t.Helper()
	identity, err := ReadF74aDatabaseIdentity(ctx, pool)
	require.NoError(t, err)
	return &f74aTestBatchProof{proof: F74aReceiptVerification{
		ReceiptID: uuid.New(), ReceiptSHA256: strings.Repeat("3", 64), SourceDatabase: identity, Cutoff: cutoff,
		InventorySHA256: strings.Repeat("1", 64), BackupObjectSHA256: strings.Repeat("2", 64),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}}
}

func TestF74aConfirmedDeletionPG16FreshProofGuardsAndRestart(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 4)
	var serviceID, environmentID uuid.UUID
	var firstObserved time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT service_id,environment_id,observed_at FROM runtime_observations WHERE id=$1`, ids[0]).Scan(&serviceID, &environmentID, &firstObserved))
	// A backdated material transition makes the first archived hot candidate
	// material; a live state link protects the second candidate.
	obs := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
		ObservedImageDigest: "sha256:bbbb", ObservedImageRepo: "registry.example/test",
		ObservedContainerID: "container", ObservedHost: "host", ObservedVersion: "v1",
		HealthStatus: "healthy", Source: "runtime", Metadata: map[string]any{},
		ObservedAt: firstObserved.Add(30 * time.Minute)}
	require.NoError(t, NewPgRuntimeObservationRepository(pool).Create(ctx, obs))
	_, err := pool.Exec(ctx, `INSERT INTO environment_service_state(service_id,environment_id,current_observation_id)
		VALUES ($1,$2,$3)`, serviceID, environmentID, ids[2])
	require.NoError(t, err)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	_, err = repo.startRun(ctx, nil, 1)
	require.ErrorContains(t, err, "independent proof")
	proof := f74aProofForDB(t, ctx, pool, cutoff)
	run, err := repo.startRun(ctx, proof, 1)
	require.NoError(t, err)
	require.Equal(t, 1, proof.calls)
	first, err := repo.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Equal(t, 1, first.Examined)
	require.Zero(t, first.Deleted, "material transition must remain hot")
	second, err := repo.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Zero(t, second.Deleted, "state-linked row must remain hot")
	proof.proof.ReceiptSHA256 = strings.Repeat("4", 64)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "independent live backup proof changed")
	proof.proof.ReceiptSHA256 = strings.Repeat("3", 64)
	proof.proof.ExpiresAt = time.Now().UTC().Add(-time.Second)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "independent live backup proof changed")
	proof.proof.ExpiresAt = time.Now().UTC().Add(time.Hour)
	proof.fail = true
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "independent live backup proof")
	var journal, hot int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_batches WHERE run_id=$1`, run.ID).Scan(&journal))
	require.Equal(t, 2, journal, "failed admission must not advance the batch journal")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[3]).Scan(&hot))
	require.Equal(t, 1, hot)
	proof.fail = false
	// A fresh repository object sees the durable cursor and must invoke the
	// authority again before deleting, not reuse the prior process's result.
	restarted := newPgF74aConfirmedDeletionRepository(pool)
	third, err := restarted.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Equal(t, 1, third.Deleted)
	require.False(t, third.Complete)
	finished, err := restarted.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.True(t, finished.Complete)
	require.GreaterOrEqual(t, proof.calls, 8)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[3]).Scan(&hot))
	require.Zero(t, hot)
	var archived int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=$1`, ids[3]).Scan(&archived))
	require.Equal(t, 1, archived)
	for _, id := range ids[1:3] {
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, id).Scan(&hot))
		require.Equal(t, 1, hot)
	}
	_, err = db.Down(ctx, pool, zap.NewNop(), db.DownOptions{Confirm: true})
	require.ErrorContains(t, err, "cannot roll back F74a confirmed deletion provenance")
}

func TestF74aConfirmedDeletionPG16RollbackAndRetry(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 3)
	proof := f74aProofForDB(t, ctx, pool, cutoff)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	run, err := repo.startRun(ctx, proof, 2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE FUNCTION f74a_test_fail_second_delete() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN IF OLD.id='`+ids[2].String()+`'::uuid THEN RAISE EXCEPTION 'injected second delete failure'; END IF; RETURN OLD; END $body$`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TRIGGER f74a_test_fail_second_delete BEFORE DELETE ON runtime_observations
		FOR EACH ROW EXECUTE FUNCTION f74a_test_fail_second_delete()`)
	require.NoError(t, err)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "injected second delete failure")
	var hot, journal, examined int
	for _, id := range ids[1:] {
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, id).Scan(&hot))
		require.Equal(t, 1, hot, "first delete must roll back with second failure")
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_batches WHERE run_id=$1`, run.ID).Scan(&journal))
	require.Zero(t, journal)
	require.NoError(t, pool.QueryRow(ctx, `SELECT examined_count FROM f74a_confirmed_deletion_runs WHERE id=$1`, run.ID).Scan(&examined))
	require.Zero(t, examined)
	_, err = pool.Exec(ctx, `DROP TRIGGER f74a_test_fail_second_delete ON runtime_observations`)
	require.NoError(t, err)
	proof.failOnCall = proof.calls + 2 // initial proof passes; final pre-commit proof revokes.
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "before commit")
	for _, id := range ids[1:] {
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, id).Scan(&hot))
		require.Equal(t, 1, hot, "failed final proof must roll back every deletion")
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_batches WHERE run_id=$1`, run.ID).Scan(&journal))
	require.Zero(t, journal)
	proof.failOnCall = 0
	batch, err := newPgF74aConfirmedDeletionRepository(pool).nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Equal(t, 2, batch.Deleted)
	require.False(t, batch.Complete)
	finished, err := repo.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.True(t, finished.Complete)
}

func TestF74aConfirmedDeletionPG16RejectsCorruptArchive(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 3)
	proof := f74aProofForDB(t, ctx, pool, cutoff)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	run, err := repo.startRun(ctx, proof, 2)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE runtime_observation_archive DISABLE TRIGGER f74a_archive_no_update`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE runtime_observation_archive SET row_digest=decode(repeat('0',64),'hex') WHERE id=$1`, ids[1])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE runtime_observation_archive ENABLE TRIGGER f74a_archive_no_update`)
	require.NoError(t, err)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "immutable archive and hot observation differ")
	var hot, journal int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[1]).Scan(&hot))
	require.Equal(t, 1, hot)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_batches WHERE run_id=$1`, run.ID).Scan(&journal))
	require.Zero(t, journal)
}
