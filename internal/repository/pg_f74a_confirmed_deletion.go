package repository

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// f74aIndependentBatchProof has deliberately no production implementer. A
// future implementer must independently reauthenticate the signed receipt,
// current retained backup-object version, live non-revocation, and credential
// recovery on every invocation. It must not rely on Bahia's service key,
// process-local state, operator-supplied flags, or a local file hash alone.
// The private method prevents external callers from providing a self-asserted
// adapter to the deletion seam while the operational provider is absent.
type f74aIndependentBatchProof interface {
	proveCurrentF74aBackup(context.Context, uuid.UUID) (F74aReceiptVerification, error)
}

type f74aConfirmedDeletionRun struct {
	ID                 uuid.UUID
	ReceiptID          uuid.UUID
	ReceiptSHA256      string
	InventorySHA256    string
	SourceDatabase     F74aDatabaseIdentity
	BackupObjectSHA256 string
	Cutoff             time.Time
	BatchSize          int
	ExaminedCount      int64
	DeletedCount       int64
	Complete           bool
}

type f74aConfirmedDeletionBatch struct {
	ID       uuid.UUID
	Examined int
	Deleted  int
	Complete bool
}

type pgF74aConfirmedDeletionRepository struct{ pool *pgxpool.Pool }

// This constructor and both mutation methods remain package-private until a
// real independent proof provider is implemented and reviewed. No CLI path
// reaches them; f74a-compact --confirm still rejects before connecting to SQL.
func newPgF74aConfirmedDeletionRepository(pool *pgxpool.Pool) *pgF74aConfirmedDeletionRepository {
	return &pgF74aConfirmedDeletionRepository{pool: pool}
}

func validF74aBatchProof(p F74aReceiptVerification) bool {
	return p.ReceiptID != uuid.Nil && validF74aDigest(p.ReceiptSHA256) && validF74aIdentity(p.SourceDatabase) &&
		validF74aDigest(p.InventorySHA256) && validF74aDigest(p.BackupObjectSHA256) &&
		!p.Cutoff.IsZero() && p.Cutoff.Before(time.Now().UTC()) && time.Now().UTC().Before(p.ExpiresAt)
}

func matchesF74aDeletionRun(p F74aReceiptVerification, run f74aConfirmedDeletionRun) bool {
	return validF74aBatchProof(p) && p.ReceiptID == run.ReceiptID &&
		p.ReceiptSHA256 == run.ReceiptSHA256 && p.InventorySHA256 == run.InventorySHA256 && p.SourceDatabase == run.SourceDatabase &&
		p.BackupObjectSHA256 == run.BackupObjectSHA256 && p.Cutoff.Equal(run.Cutoff)
}

func (r *pgF74aConfirmedDeletionRepository) startRun(ctx context.Context, proof f74aIndependentBatchProof, batchSize int) (f74aConfirmedDeletionRun, error) {
	var run f74aConfirmedDeletionRun
	if proof == nil || batchSize < 1 || batchSize > F74aPageLimit {
		return run, fmt.Errorf("F74a confirmed deletion requires independent proof and batch size in 1..%d", F74aPageLimit)
	}
	attested, err := proof.proveCurrentF74aBackup(ctx, uuid.Nil)
	if err != nil || !validF74aBatchProof(attested) {
		return run, fmt.Errorf("F74a independent live backup proof is unavailable or invalid")
	}
	identity, err := ReadF74aDatabaseIdentity(ctx, r.pool)
	if err != nil {
		return run, err
	}
	if identity != attested.SourceDatabase {
		return run, fmt.Errorf("F74a backup proof is for another physical PostgreSQL database")
	}
	run = f74aConfirmedDeletionRun{
		ReceiptID: attested.ReceiptID, ReceiptSHA256: attested.ReceiptSHA256, InventorySHA256: attested.InventorySHA256,
		SourceDatabase: identity, BackupObjectSHA256: attested.BackupObjectSHA256,
		Cutoff: attested.Cutoff, BatchSize: batchSize,
	}
	err = r.pool.QueryRow(ctx, `INSERT INTO f74a_confirmed_deletion_runs
		(receipt_id,receipt_sha256,source_inventory_sha256,source_database_name,source_database_oid,source_system_identifier,backup_object_sha256,cutoff,batch_size)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, run.ReceiptID, run.ReceiptSHA256, run.InventorySHA256,
		identity.Name, identity.OID, identity.SystemIdentifier, run.BackupObjectSHA256, run.Cutoff, batchSize).Scan(&run.ID)
	if err != nil {
		return f74aConfirmedDeletionRun{}, fmt.Errorf("starting F74a confirmed deletion run: %w", err)
	}
	return run, nil
}

func (r *pgF74aConfirmedDeletionRepository) nextBatch(ctx context.Context, runID uuid.UUID, proof f74aIndependentBatchProof) (f74aConfirmedDeletionBatch, error) {
	var batch f74aConfirmedDeletionBatch
	if proof == nil || runID == uuid.Nil {
		return batch, fmt.Errorf("F74a confirmed deletion requires independent proof and run ID")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var run f74aConfirmedDeletionRun
	var cursorService, cursorEnvironment, cursorID *uuid.UUID
	var cursorObserved *time.Time
	err = tx.QueryRow(ctx, `SELECT receipt_id,receipt_sha256,source_inventory_sha256,source_database_name,source_database_oid,source_system_identifier,
		backup_object_sha256,cutoff,batch_size,cursor_service_id,cursor_environment_id,cursor_observed_at,cursor_id,complete
		FROM f74a_confirmed_deletion_runs WHERE id=$1 FOR UPDATE`, runID).Scan(
		&run.ReceiptID, &run.ReceiptSHA256, &run.InventorySHA256, &run.SourceDatabase.Name, &run.SourceDatabase.OID, &run.SourceDatabase.SystemIdentifier,
		&run.BackupObjectSHA256, &run.Cutoff, &run.BatchSize,
		&cursorService, &cursorEnvironment, &cursorObserved, &cursorID, &run.Complete)
	if err != nil {
		return batch, fmt.Errorf("locking F74a confirmed deletion run: %w", err)
	}
	if run.Complete {
		batch.Complete = true
		return batch, tx.Commit(ctx)
	}
	// Invoke the independent live authority while the batch transaction is
	// open. A prior successful receipt check cannot be replayed here.
	attested, err := proof.proveCurrentF74aBackup(ctx, runID)
	if err != nil || !matchesF74aDeletionRun(attested, run) {
		return batch, fmt.Errorf("F74a independent live backup proof changed or is unavailable")
	}
	var identity F74aDatabaseIdentity
	err = tx.QueryRow(ctx, `SELECT current_database(),d.oid::text,c.system_identifier::text
		FROM pg_database d CROSS JOIN pg_control_system() c WHERE d.datname=current_database()`).Scan(
		&identity.Name, &identity.OID, &identity.SystemIdentifier)
	if err != nil || identity != run.SourceDatabase {
		return batch, fmt.Errorf("F74a physical PostgreSQL database identity changed or is unavailable")
	}
	type key struct {
		id, service, environment uuid.UUID
		observed                 time.Time
	}
	keys := make([]key, 0, run.BatchSize)
	rows, err := tx.Query(ctx, `SELECT h.id,h.service_id,h.environment_id,h.observed_at
		FROM runtime_observations h JOIN runtime_observation_archive a ON a.id=h.id
		WHERE h.observed_at < $1
		AND ($2::uuid IS NULL OR (h.service_id,h.environment_id,h.observed_at,h.id)>($2,$3,$4,$5))
		ORDER BY h.service_id,h.environment_id,h.observed_at,h.id LIMIT $6`, run.Cutoff,
		cursorService, cursorEnvironment, cursorObserved, cursorID, run.BatchSize)
	if err != nil {
		return batch, fmt.Errorf("selecting F74a confirmed deletion batch: %w", err)
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.service, &k.environment, &k.observed); err != nil {
			rows.Close()
			return batch, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return batch, err
	}
	batch.Examined = len(keys)
	batch.Complete = len(keys) < run.BatchSize
	if len(keys) == 0 {
		_, err := tx.Exec(ctx, `UPDATE f74a_confirmed_deletion_runs SET complete=true,updated_at=now() WHERE id=$1`, runID)
		if err != nil {
			return batch, err
		}
		if err := reconcileF74aDeletionBatch(ctx, tx, run, runID); err != nil {
			return f74aConfirmedDeletionBatch{}, err
		}
		finalProof, err := proof.proveCurrentF74aBackup(ctx, runID)
		if err != nil || !matchesF74aDeletionRun(finalProof, run) {
			return f74aConfirmedDeletionBatch{}, fmt.Errorf("F74a independent live backup proof changed before commit")
		}
		return batch, tx.Commit(ctx)
	}
	batch.ID = uuid.New()
	obsRepo := newPgRuntimeObservationRepositoryWithDB(tx)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return batch, err
		}
		if _, err := tx.Exec(ctx, `SELECT f74a_lock_observation_coordinate($1,$2)`, k.service, k.environment); err != nil {
			return batch, err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,7401))`, k.id); err != nil {
			return batch, err
		}
		obs, err := obsRepo.scanObs(tx.QueryRow(ctx, `SELECT `+obsColumns+` FROM runtime_observations WHERE id=$1 FOR UPDATE`, k.id))
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return batch, fmt.Errorf("locking F74a hot observation: %w", err)
		}
		if obs.ServiceID != k.service || obs.EnvironmentID != k.environment || !obs.ObservedAt.Equal(k.observed) || !obs.ObservedAt.Before(run.Cutoff) {
			continue
		}
		var archiveDigest, recomputedArchiveDigest, hotDigest []byte
		var archivedService, archivedEnvironment uuid.UUID
		var archivedObserved time.Time
		err = tx.QueryRow(ctx, `SELECT a.row_digest,f74a_observation_digest(to_jsonb(a),a.observed_at),
			f74a_observation_digest(to_jsonb(h),h.observed_at),a.service_id,a.environment_id,a.observed_at
			FROM runtime_observation_archive a JOIN runtime_observations h ON h.id=a.id
			WHERE a.id=$1`, k.id).Scan(&archiveDigest, &recomputedArchiveDigest, &hotDigest,
			&archivedService, &archivedEnvironment, &archivedObserved)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return batch, err
		}
		if archivedService != k.service || archivedEnvironment != k.environment || !archivedObserved.Equal(k.observed) ||
			!bytes.Equal(archiveDigest, recomputedArchiveDigest) || !bytes.Equal(archiveDigest, hotDigest) {
			return batch, fmt.Errorf("F74a immutable archive and hot observation differ")
		}
		var linked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM environment_service_state WHERE current_observation_id=$1)`, k.id).Scan(&linked); err != nil {
			return batch, err
		}
		if linked {
			continue
		}
		previous, err := obsRepo.scanObs(tx.QueryRow(ctx, `SELECT `+obsColumns+` FROM runtime_observation_history
			WHERE service_id=$1 AND environment_id=$2 AND (observed_at,id)<($3,$4)
			ORDER BY observed_at DESC,id DESC LIMIT 1`, k.service, k.environment, k.observed, k.id))
		if err != nil && err != pgx.ErrNoRows {
			return batch, fmt.Errorf("finding F74a material predecessor: %w", err)
		}
		if err == pgx.ErrNoRows || domain.RuntimeObservationMateriallyChanged(previous, obs) {
			continue
		}
		command, err := tx.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1
			AND NOT EXISTS (SELECT 1 FROM environment_service_state WHERE current_observation_id=$1)`, k.id)
		if err != nil {
			return batch, fmt.Errorf("deleting proven redundant F74a hot observation: %w", err)
		}
		if command.RowsAffected() != 1 {
			return batch, fmt.Errorf("F74a hot observation changed during admitted deletion")
		}
		_, err = tx.Exec(ctx, `INSERT INTO f74a_confirmed_deletion_items
			(run_id,batch_id,observation_id,row_digest) VALUES ($1,$2,$3,$4)`,
			runID, batch.ID, k.id, archiveDigest)
		if err != nil {
			return batch, fmt.Errorf("journaling F74a confirmed deletion item: %w", err)
		}
		batch.Deleted++
	}
	last := keys[len(keys)-1]
	_, err = tx.Exec(ctx, `INSERT INTO f74a_confirmed_deletion_batches
		(id,run_id,examined_count,deleted_count,cursor_service_id,cursor_environment_id,cursor_observed_at,cursor_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, batch.ID, runID, batch.Examined, batch.Deleted,
		last.service, last.environment, last.observed, last.id)
	if err != nil {
		return batch, fmt.Errorf("journaling F74a confirmed deletion batch: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE f74a_confirmed_deletion_runs SET
		cursor_service_id=$2,cursor_environment_id=$3,cursor_observed_at=$4,cursor_id=$5,
		examined_count=examined_count+$6,deleted_count=deleted_count+$7,
		complete=$8,updated_at=now() WHERE id=$1`, runID, last.service, last.environment, last.observed, last.id,
		batch.Examined, batch.Deleted, batch.Complete)
	if err != nil {
		return batch, fmt.Errorf("updating F74a confirmed deletion cursor: %w", err)
	}
	// The independent authority queries committed state on another connection.
	// Reconcile inside this transaction as well: only this read can observe the
	// just-journaled deletes and any same-transaction trigger side effects.
	if err := reconcileF74aDeletionBatch(ctx, tx, run, runID); err != nil {
		return f74aConfirmedDeletionBatch{}, err
	}
	// A proof that was live at transaction start is not enough if it expires
	// or is revoked while rows are being examined. A failed final check rolls
	// back deletions and progress together. The eventual authority still needs
	// an independent fenced validity guarantee through commit.
	finalProof, err := proof.proveCurrentF74aBackup(ctx, runID)
	if err != nil || !matchesF74aDeletionRun(finalProof, run) {
		return f74aConfirmedDeletionBatch{}, fmt.Errorf("F74a independent live backup proof changed before commit")
	}
	if err := tx.Commit(ctx); err != nil {
		return batch, fmt.Errorf("committing F74a confirmed deletion batch: %w", err)
	}
	return batch, nil
}

// reconcileF74aDeletionBatch compares the pinned signed inventory against
// this exact transaction's post-mutation view, including trigger side effects.
func reconcileF74aDeletionBatch(ctx context.Context, tx pgx.Tx, run f74aConfirmedDeletionRun, runID uuid.UUID) error {
	reconciled, err := preflightF74aRestoreTx(ctx, tx, run.Cutoff, &runID)
	if err != nil || reconciled.InventorySHA256 != run.InventorySHA256 {
		return fmt.Errorf("F74a signed source inventory changed during deletion batch")
	}
	return nil
}
