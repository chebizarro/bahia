package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// F74aArchiveRun is the durable, fixed-cutoff cursor for bounded archive work.
type F74aArchiveRun struct {
	ID            uuid.UUID
	Cutoff        time.Time
	BatchSize     int
	ExaminedCount int64
	ArchivedCount int64
	Complete      bool
}

type F74aArchiveBatch struct {
	ID       uuid.UUID
	Examined int
	Archived int
	Complete bool
}

type F74aArchivedObservation struct {
	Observation domain.RuntimeObservation
	BatchID     uuid.UUID
	ArchivedAt  time.Time
	Digest      []byte
}

type PgF74aObservationArchiveRepository struct{ pool *pgxpool.Pool }

func NewPgF74aObservationArchiveRepository(pool *pgxpool.Pool) *PgF74aObservationArchiveRepository {
	return &PgF74aObservationArchiveRepository{pool: pool}
}

func (r *PgF74aObservationArchiveRepository) StartRun(ctx context.Context, cutoff time.Time, batchSize int) (F74aArchiveRun, error) {
	var run F74aArchiveRun
	if cutoff.IsZero() || !cutoff.Before(time.Now().UTC()) || batchSize < 1 || batchSize > 1000 {
		return run, fmt.Errorf("archive run requires a past cutoff and batch size between 1 and 1000")
	}
	run.Cutoff, run.BatchSize = cutoff.UTC(), batchSize
	err := r.pool.QueryRow(ctx, `INSERT INTO f74a_observation_compaction_runs (cutoff,batch_size)
		VALUES ($1,$2) RETURNING id`, run.Cutoff, batchSize).Scan(&run.ID)
	if err != nil {
		return run, fmt.Errorf("creating F74a archive run: %w", err)
	}
	return run, nil
}

// ArchiveNextBatch commits the archive copy, equality check, hot deletion, and
// journal cursor together. Per-ID advisory locks precede hot row locks; the
// state-link trigger takes the same lock before restoring/FK validation.
func (r *PgF74aObservationArchiveRepository) ArchiveNextBatch(ctx context.Context, runID uuid.UUID) (F74aArchiveBatch, error) {
	var batch F74aArchiveBatch
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var cutoff time.Time
	var size int
	var service, env, cursorID *uuid.UUID
	var observed *time.Time
	var complete bool
	err = tx.QueryRow(ctx, `SELECT cutoff,batch_size,cursor_service_id,cursor_environment_id,cursor_observed_at,cursor_id,complete
		FROM f74a_observation_compaction_runs WHERE id=$1 FOR UPDATE`, runID).
		Scan(&cutoff, &size, &service, &env, &observed, &cursorID, &complete)
	if err != nil {
		return batch, fmt.Errorf("locking F74a archive run: %w", err)
	}
	if complete {
		batch.Complete = true
		return batch, tx.Commit(ctx)
	}

	type key struct {
		id, service, env uuid.UUID
		at               time.Time
	}
	keys := make([]key, 0, size)
	rows, err := tx.Query(ctx, `SELECT id,service_id,environment_id,observed_at
		FROM runtime_observations WHERE observed_at < $1
		AND ($2::uuid IS NULL OR (service_id,environment_id,observed_at,id) > ($2,$3,$4,$5))
		ORDER BY service_id,environment_id,observed_at,id LIMIT $6`, cutoff, service, env, observed, cursorID, size)
	if err != nil {
		return batch, fmt.Errorf("selecting F74a archive batch: %w", err)
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.service, &k.env, &k.at); err != nil {
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
	batch.ID = uuid.New()
	batch.Examined = len(keys)
	batch.Complete = len(keys) < size
	if len(keys) == 0 {
		_, err = tx.Exec(ctx, `UPDATE f74a_observation_compaction_runs SET complete=true,updated_at=now() WHERE id=$1`, runID)
		if err != nil {
			return batch, err
		}
		return batch, tx.Commit(ctx)
	}
	obsRepo := newPgRuntimeObservationRepositoryWithDB(tx)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
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
			return batch, fmt.Errorf("locking observation %s: %w", k.id, err)
		}
		if !obs.ObservedAt.Before(cutoff) {
			continue
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
			ORDER BY observed_at DESC,id DESC LIMIT 1`, obs.ServiceID, obs.EnvironmentID, obs.ObservedAt, obs.ID))
		if err != nil && err != pgx.ErrNoRows {
			return batch, fmt.Errorf("finding material predecessor: %w", err)
		}
		if err == pgx.ErrNoRows || domain.RuntimeObservationMateriallyChanged(previous, obs) {
			continue
		}
		var archivedDigest, currentDigest []byte
		err = tx.QueryRow(ctx, `SELECT row_digest FROM runtime_observation_archive WHERE id=$1`, k.id).Scan(&archivedDigest)
		if err != nil && err != pgx.ErrNoRows {
			return batch, err
		}
		if err == pgx.ErrNoRows {
			_, err = tx.Exec(ctx, `INSERT INTO runtime_observation_archive (`+obsColumns+`,archive_batch_id,row_digest)
				SELECT `+obsColumns+`,$2,f74a_observation_digest(to_jsonb(o),o.observed_at)
				FROM runtime_observations o WHERE o.id=$1`, k.id, batch.ID)
			if err != nil {
				return batch, fmt.Errorf("archiving observation %s: %w", k.id, err)
			}
			if err := tx.QueryRow(ctx, `SELECT row_digest FROM runtime_observation_archive WHERE id=$1`, k.id).Scan(&archivedDigest); err != nil {
				return batch, err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT f74a_observation_digest(to_jsonb(o),o.observed_at)
			FROM runtime_observations o WHERE id=$1`, k.id).Scan(&currentDigest); err != nil {
			return batch, err
		}
		if string(archivedDigest) != string(currentDigest) {
			return batch, fmt.Errorf("archive digest mismatch for observation %s", k.id)
		}
		command, err := tx.Exec(ctx, `DELETE FROM runtime_observations WHERE id=$1
			AND NOT EXISTS (SELECT 1 FROM environment_service_state WHERE current_observation_id=$1)`, k.id)
		if err != nil {
			return batch, fmt.Errorf("removing archived observation %s: %w", k.id, err)
		}
		if command.RowsAffected() != 1 {
			return batch, fmt.Errorf("observation %s gained a state link during archive move", k.id)
		}
		batch.Archived++
	}
	last := keys[len(keys)-1]
	_, err = tx.Exec(ctx, `INSERT INTO f74a_observation_archive_batches
		(id,run_id,examined_count,archived_count,cursor_service_id,cursor_environment_id,cursor_observed_at,cursor_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, batch.ID, runID, batch.Examined, batch.Archived,
		last.service, last.env, last.at, last.id)
	if err != nil {
		return batch, fmt.Errorf("committing F74a archive batch journal: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE f74a_observation_compaction_runs SET
		cursor_service_id=$2,cursor_environment_id=$3,cursor_observed_at=$4,cursor_id=$5,
		examined_count=examined_count+$6,archived_count=archived_count+$7,
		complete=$8,updated_at=now() WHERE id=$1`, runID, last.service, last.env, last.at, last.id,
		batch.Examined, batch.Archived, batch.Complete)
	if err != nil {
		return batch, err
	}
	if err := tx.Commit(ctx); err != nil {
		return batch, fmt.Errorf("committing F74a archive batch: %w", err)
	}
	return batch, nil
}

// GetArchivedByID is the explicit audit route, including immutable provenance.
func (r *PgF74aObservationArchiveRepository) GetArchivedByID(ctx context.Context, id uuid.UUID) (*F74aArchivedObservation, error) {
	var out F74aArchivedObservation
	err := r.pool.QueryRow(ctx, `SELECT archive_batch_id,archived_at,row_digest FROM runtime_observation_archive WHERE id=$1`, id).
		Scan(&out.BatchID, &out.ArchivedAt, &out.Digest)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	obs, err := newPgRuntimeObservationRepositoryWithDB(r.pool).scanObs(r.pool.QueryRow(ctx,
		`SELECT `+obsColumns+` FROM runtime_observation_archive WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	out.Observation = *obs
	return &out, nil
}

// ListArchivedAfter pages immutable audit history in observation order. The
// provenance for each result is returned with its original observation value.
func (r *PgF74aObservationArchiveRepository) ListArchivedAfter(ctx context.Context,
	serviceID, environmentID uuid.UUID, after time.Time, afterID uuid.UUID, limit int) ([]F74aArchivedObservation, error) {
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("archived observation page limit must be between 1 and 1000")
	}
	var cursor any
	if !after.IsZero() {
		cursor = after
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM runtime_observation_archive
		WHERE service_id=$1 AND environment_id=$2
		AND ($3::timestamptz IS NULL OR (observed_at,id)>($3,$4))
		ORDER BY observed_at,id LIMIT $5`, serviceID, environmentID, cursor, afterID, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, limit)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]F74aArchivedObservation, 0, len(ids))
	for _, id := range ids {
		item, err := r.GetArchivedByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if item == nil {
			return nil, fmt.Errorf("archived observation %s disappeared during audit read", id)
		}
		out = append(out, *item)
	}
	return out, nil
}

// RestoreByID retains the immutable archive while recreating the hot FK target.
func (r *PgF74aObservationArchiveRepository) RestoreByID(ctx context.Context, id uuid.UUID) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,7401))`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime_observations (`+obsColumns+`)
		SELECT `+obsColumns+` FROM runtime_observation_archive WHERE id=$1 ON CONFLICT (id) DO NOTHING`, id)
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT f74a_observation_digest(to_jsonb(h),h.observed_at)=a.row_digest
		FROM runtime_observations h JOIN runtime_observation_archive a ON a.id=h.id WHERE h.id=$1`, id).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("restored observation %s differs from archive", id)
	}
	return tx.Commit(ctx)
}
