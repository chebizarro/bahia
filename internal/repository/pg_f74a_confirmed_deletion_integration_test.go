//go:build integration

package repository

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type f74aTestSignedProof struct {
	pool    *pgxpool.Pool
	pin     string
	receipt []byte
}

func (p *f74aTestSignedProof) proveCurrentF74aBackup(ctx context.Context, runID uuid.UUID) (F74aReceiptVerification, error) {
	if runID == uuid.Nil {
		return VerifyF74aAttestedReceipt(ctx, p.pool, p.pin, p.receipt)
	}
	return verifyF74aAttestedReceiptForDeletionRun(ctx, p.pool, p.pin, p.receipt, runID)
}

type f74aTestBatchProof struct {
	proof      F74aReceiptVerification
	calls      int
	fail       bool
	failOnCall int
}

func (p *f74aTestBatchProof) proveCurrentF74aBackup(context.Context, uuid.UUID) (F74aReceiptVerification, error) {
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
	inventory, err := PreflightF74aRestore(ctx, pool, cutoff)
	require.NoError(t, err)
	return &f74aTestBatchProof{proof: F74aReceiptVerification{
		ReceiptID: uuid.New(), ReceiptSHA256: strings.Repeat("3", 64), SourceDatabase: identity, Cutoff: cutoff,
		InventorySHA256: inventory.InventorySHA256, BackupObjectSHA256: strings.Repeat("2", 64),
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
	originalInventory := proof.proof.InventorySHA256
	proof.proof.InventorySHA256 = strings.Repeat("5", 64)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "independent live backup proof changed")
	proof.proof.InventorySHA256 = originalInventory
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
	require.GreaterOrEqual(t, proof.calls, 9)
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

// A DELETE trigger can mutate unrelated inventory in the very transaction
// being admitted. A separate-connection receipt check cannot observe it yet.
func TestF74aConfirmedDeletionPG16SameTransactionInventoryGuard(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 3)
	proof := f74aProofForDB(t, ctx, pool, cutoff)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	run, err := repo.startRun(ctx, proof, 1)
	require.NoError(t, err)
	injectedID := uuid.New()
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION f74a_test_inject_unrelated() RETURNS trigger LANGUAGE plpgsql AS $body$
		BEGIN
			INSERT INTO runtime_observations(id,service_id,environment_id,observed_image_digest,
				observed_image_repo,observed_container_id,observed_host,observed_version,
				health_status,source,metadata,observed_at)
			VALUES ('%s'::uuid,OLD.service_id,OLD.environment_id,'sha256:unrelated',
				'registry.example/test','container','host','v1','healthy','runtime','{}'::jsonb,
				OLD.observed_at + interval '10 hours');
			RETURN OLD;
		END $body$`, injectedID))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TRIGGER f74a_test_inject_unrelated AFTER DELETE ON runtime_observations
		FOR EACH ROW EXECUTE FUNCTION f74a_test_inject_unrelated()`)
	require.NoError(t, err)
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "signed source inventory changed during deletion batch")
	var hot, injected, items, batches, examined int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[1]).Scan(&hot))
	require.Equal(t, 1, hot, "deleted hot row must roll back")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, injectedID).Scan(&injected))
	require.Zero(t, injected, "trigger insertion must roll back")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_items WHERE run_id=$1`, run.ID).Scan(&items))
	require.Zero(t, items)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_batches WHERE run_id=$1`, run.ID).Scan(&batches))
	require.Zero(t, batches)
	require.NoError(t, pool.QueryRow(ctx, `SELECT examined_count FROM f74a_confirmed_deletion_runs WHERE id=$1`, run.ID).Scan(&examined))
	require.Zero(t, examined)
	_, err = pool.Exec(ctx, `DROP TRIGGER f74a_test_inject_unrelated ON runtime_observations`)
	require.NoError(t, err)
	batch, err := repo.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Equal(t, 1, batch.Deleted)
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

func TestF74aConfirmedDeletionPG16SignedReceiptReconcilesAfterRestart(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 3)
	initial, err := PreflightF74aRestore(ctx, pool, cutoff)
	require.NoError(t, err)
	sourceIdentity, err := ReadF74aDatabaseIdentity(ctx, pool)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE DATABASE f74a_isolated_restore`)
	require.NoError(t, err)
	restoreCfg := pool.Config()
	restoreCfg.ConnConfig.Database = "f74a_isolated_restore"
	restorePool, err := pgxpool.NewWithConfig(ctx, restoreCfg)
	require.NoError(t, err)
	defer restorePool.Close()
	restoreIdentity, err := ReadF74aDatabaseIdentity(ctx, restorePool)
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	payload := F74aAttestedBackupPayload{
		Version: f74aReceiptVersion, ReceiptID: uuid.New(),
		SourceDatabase: sourceIdentity, RestoreDatabase: restoreIdentity, Cutoff: cutoff,
		SnapshotID: "pg16-signed-reconciliation-test", BackupObjectRef: "file:///tmp/independent.dump",
		SnapshotCreatedAt: now.Add(-3 * time.Minute), BackupObjectSHA256: strings.Repeat("2", 64),
		SourceInventorySHA256: initial.InventorySHA256, RestoreInventorySHA256: initial.InventorySHA256,
		RestoreVerifiedAt: now.Add(-2 * time.Minute), IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	signingBytes, err := payload.SigningBytes()
	require.NoError(t, err)
	receipt, err := json.Marshal(F74aAttestedBackupReceipt{Payload: payload, Signature: hex.EncodeToString(ed25519.Sign(priv, signingBytes))})
	require.NoError(t, err)
	pin := hex.EncodeToString(pub)
	attestor := &f74aTestSignedProof{pool: pool, pin: pin, receipt: receipt}
	verified, err := attestor.proveCurrentF74aBackup(ctx, uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, initial.InventorySHA256, verified.InventorySHA256)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	run, err := repo.startRun(ctx, attestor, 1)
	require.NoError(t, err)
	first, err := repo.nextBatch(ctx, run.ID, attestor)
	require.NoError(t, err)
	require.Equal(t, 1, first.Deleted)
	physical, err := PreflightF74aRestore(ctx, pool, cutoff)
	require.NoError(t, err)
	require.NotEqual(t, initial.InventorySHA256, physical.InventorySHA256, "physical hot presence changed")
	reconciled, err := preflightF74aRestoreForRun(ctx, pool, cutoff, run.ID)
	require.NoError(t, err)
	require.Equal(t, initial.InventorySHA256, reconciled.InventorySHA256)
	require.Equal(t, initial.HotCandidates, reconciled.HotCandidates)
	// No in-memory state is needed: a new provider and repository read the
	// immutable journal and validate the exact signed receipt after restart.
	restartedProof := &f74aTestSignedProof{pool: pool, pin: pin, receipt: receipt}
	_, err = restartedProof.proveCurrentF74aBackup(ctx, run.ID)
	require.NoError(t, err)
	second, err := newPgF74aConfirmedDeletionRepository(pool).nextBatch(ctx, run.ID, restartedProof)
	require.NoError(t, err)
	require.Equal(t, 1, second.Deleted)
	_, err = restartedProof.proveCurrentF74aBackup(ctx, run.ID)
	require.NoError(t, err)
	var items int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_items WHERE run_id=$1`, run.ID).Scan(&items))
	require.Equal(t, 2, items)
	for _, id := range ids[1:] {
		var archived, hot int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observation_archive WHERE id=$1`, id).Scan(&archived))
		require.Equal(t, 1, archived)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, id).Scan(&hot))
		require.Zero(t, hot)
	}
	// A corrupt per-row journal cannot launder the changed physical hot flag.
	_, err = pool.Exec(ctx, `ALTER TABLE f74a_confirmed_deletion_items DISABLE TRIGGER f74a_confirmed_deletion_item_immutable`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE f74a_confirmed_deletion_items SET row_digest=decode(repeat('0',64),'hex')
		WHERE run_id=$1 AND observation_id=$2`, run.ID, ids[1])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE f74a_confirmed_deletion_items ENABLE TRIGGER f74a_confirmed_deletion_item_immutable`)
	require.NoError(t, err)
	_, err = restartedProof.proveCurrentF74aBackup(ctx, run.ID)
	require.ErrorContains(t, err, "deletion provenance differs")
	_, err = pool.Exec(ctx, `ALTER TABLE f74a_confirmed_deletion_items DISABLE TRIGGER f74a_confirmed_deletion_item_immutable`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE f74a_confirmed_deletion_items i SET row_digest=a.row_digest
		FROM runtime_observation_archive a WHERE i.run_id=$1 AND i.observation_id=$2 AND a.id=i.observation_id`, run.ID, ids[1])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `ALTER TABLE f74a_confirmed_deletion_items ENABLE TRIGGER f74a_confirmed_deletion_item_immutable`)
	require.NoError(t, err)
	_, err = restartedProof.proveCurrentF74aBackup(ctx, run.ID)
	require.NoError(t, err)
	// A hot row removed outside the journal cannot be laundered into the
	// original signed inventory by this run's reconciliation.
	_, err = pool.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1`, ids[0])
	require.NoError(t, err)
	_, err = restartedProof.proveCurrentF74aBackup(ctx, run.ID)
	require.ErrorContains(t, err, "inventory differs")
}

// The test authority signs only fixture claims; operational custody and
// non-revocable holds must be supplied by a separate deployment component.
func TestF74aConfirmedDeletionPG16LiveAttestorRollbackAndRestart(t *testing.T) {
	pool, ctx := disposableF74aDeletionDB(t)
	ids, cutoff := f74aDeletionCandidates(t, ctx, pool, 3)
	initial, err := PreflightF74aRestore(ctx, pool, cutoff)
	require.NoError(t, err)
	sourceIdentity, err := ReadF74aDatabaseIdentity(ctx, pool)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE DATABASE f74a_isolated_restore`)
	require.NoError(t, err)
	restoreCfg := pool.Config()
	restoreCfg.ConnConfig.Database = "f74a_isolated_restore"
	restorePool, err := pgxpool.NewWithConfig(ctx, restoreCfg)
	require.NoError(t, err)
	defer restorePool.Close()
	restoreIdentity, err := ReadF74aDatabaseIdentity(ctx, restorePool)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, wrongPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	receiptPayload := F74aAttestedBackupPayload{
		Version: f74aReceiptVersion, ReceiptID: uuid.New(), SourceDatabase: sourceIdentity,
		RestoreDatabase: restoreIdentity, Cutoff: cutoff, SnapshotID: "pg16-live-attestor-test",
		BackupObjectRef:   "s3://independent-bucket/postgres-backup/version-1",
		SnapshotCreatedAt: now.Add(-3 * time.Minute), BackupObjectSHA256: strings.Repeat("2", 64),
		SourceInventorySHA256: initial.InventorySHA256, RestoreInventorySHA256: initial.InventorySHA256,
		RestoreVerifiedAt: now.Add(-2 * time.Minute), IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	message, err := receiptPayload.SigningBytes()
	require.NoError(t, err)
	receipt, err := json.Marshal(F74aAttestedBackupReceipt{Payload: receiptPayload,
		Signature: hex.EncodeToString(ed25519.Sign(private, message))})
	require.NoError(t, err)
	mode := "valid"
	calls := 0
	revokeOnCall := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request f74aLiveGrantRequest
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls++
		if mode == "unavailable" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		issued := time.Now().UTC()
		grant := f74aLiveGrantPayload{
			Version: f74aLiveGrantVersion, Nonce: request.Nonce, ReceiptID: request.ReceiptID,
			ReceiptSHA256: request.ReceiptSHA256, RunID: request.RunID,
			SourceDatabase: request.SourceDatabase, Cutoff: request.Cutoff,
			SourceInventorySHA256: request.SourceInventorySHA256,
			BackupObjectRef:       request.BackupObjectRef, BackupObjectSHA256: request.BackupObjectSHA256,
			IssuedAt: issued, ExpiresAt: issued.Add(time.Minute), RetainedUntil: issued.Add(48 * time.Hour),
			CredentialRecoveryVerifiedAt: issued, CustodyHoldID: "custody-fence-1", HoldUntilExplicitRelease: true,
		}
		if mode == "revoked" || calls == revokeOnCall {
			grant.Revoked = true
		}
		if mode == "stale" {
			grant.IssuedAt = issued.Add(-time.Minute)
		}
		key := private
		if mode == "bad-signature" {
			key = wrongPrivate
		}
		_, _ = w.Write(f74aSignedLiveGrant(t, key, grant))
	}))
	defer server.Close()
	pin := hex.EncodeToString(public)
	proof, err := newF74aLiveAttestorProof(pool, pin, receipt, server.URL, server.Client())
	require.NoError(t, err)
	repo := newPgF74aConfirmedDeletionRepository(pool)
	run, err := repo.startRun(ctx, proof, 1)
	require.NoError(t, err)
	proof, err = newF74aLiveAttestorProof(pool, pin, receipt, server.URL, server.Client())
	require.NoError(t, err)
	first, err := repo.nextBatch(ctx, run.ID, proof)
	require.NoError(t, err)
	require.Equal(t, 1, first.Deleted)
	for _, rejected := range []string{"revoked", "stale", "bad-signature"} {
		mode = rejected
		_, err = repo.nextBatch(ctx, run.ID, proof)
		require.Error(t, err, rejected)
		var hot, items int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[2]).Scan(&hot))
		require.Equal(t, 1, hot)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_items WHERE run_id=$1`, run.ID).Scan(&items))
		require.Equal(t, 1, items, "rejected authority must not advance journal")
	}
	mode = "valid"
	revokeOnCall = calls + 2 // admission succeeds; pre-commit refresh revokes.
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.ErrorContains(t, err, "before commit")
	var hot, items int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[2]).Scan(&hot))
	require.Equal(t, 1, hot)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_items WHERE run_id=$1`, run.ID).Scan(&items))
	require.Equal(t, 1, items, "pre-commit revocation must roll back journal and hot deletion")
	revokeOnCall = 0
	mode = "unavailable"
	_, err = repo.nextBatch(ctx, run.ID, proof)
	require.Error(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM runtime_observations WHERE id=$1`, ids[2]).Scan(&hot))
	require.Equal(t, 1, hot)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM f74a_confirmed_deletion_items WHERE run_id=$1`, run.ID).Scan(&items))
	require.Equal(t, 1, items)
	mode = "valid"
	// The new provider has no cached grant; it must re-check the same signed
	// receipt against committed deletion provenance and request a fresh nonce.
	restarted, err := newF74aLiveAttestorProof(pool, pin, receipt, server.URL, server.Client())
	require.NoError(t, err)
	second, err := newPgF74aConfirmedDeletionRepository(pool).nextBatch(ctx, run.ID, restarted)
	require.NoError(t, err)
	require.Equal(t, 1, second.Deleted)
	require.GreaterOrEqual(t, calls, 7)
}
