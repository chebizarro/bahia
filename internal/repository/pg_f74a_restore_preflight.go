package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// F74aRestorePreflight is an unauthenticated, read-only inventory of one SQL
// snapshot. Equal source/isolated-restore inventories detect copy divergence;
// they do not establish backup provenance, isolation, retention, or custody.
// No deletion path accepts this value as an authorization receipt.
type F74aRestorePreflight struct {
	DatabaseName       string
	Cutoff             time.Time
	SchemaVersions     int64
	ArchiveRuns        int64
	ArchiveBatches     int64
	Observations       int64
	ArchivedRows       int64
	LinkedObservations int64
	HotCandidates      int64
	InventorySHA256    string
}

const f74aPreflightPageSize = 500

// PreflightF74aRestore scans bounded keyset pages in one repeatable-read,
// read-only transaction. The hash covers schema versions, cutoff, every
// historical observation's immutable value, archive presence, hot presence,
// state link, and original deployment-unit retirement state. DatabaseName is
// diagnostic only: logical restores commonly change it.
func PreflightF74aRestore(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) (F74aRestorePreflight, error) {
	return preflightF74aRestore(ctx, pool, cutoff, nil)
}

// preflightF74aRestoreForRun reconstructs the original signed source inventory
// only from committed, immutable per-row deletion journal entries for this
// exact run. Other hot-row changes remain visible and invalidate the receipt.
func preflightF74aRestoreForRun(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, runID uuid.UUID) (F74aRestorePreflight, error) {
	if runID == uuid.Nil {
		return F74aRestorePreflight{}, fmt.Errorf("F74a deletion run ID is required")
	}
	return preflightF74aRestore(ctx, pool, cutoff, &runID)
}

func preflightF74aRestore(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, runID *uuid.UUID) (F74aRestorePreflight, error) {
	var out F74aRestorePreflight
	if cutoff.IsZero() || !cutoff.Before(time.Now().UTC()) {
		return out, fmt.Errorf("F74a restore preflight requires a past cutoff")
	}
	out.Cutoff = cutoff.UTC()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// PostgreSQL renders timestamptz values in JSON using the session zone.
	// Pin it for deterministic archive journal hashes across connections.
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return out, fmt.Errorf("setting F74a inventory timezone: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&out.DatabaseName); err != nil {
		return out, err
	}
	h := sha256.New()
	writeF74aPreflightField(h, []byte("bahia-f74a-restore-preflight-v1"))
	writeF74aPreflightField(h, []byte(out.Cutoff.Format(time.RFC3339Nano)))
	var version string
	for {
		rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations WHERE version > $1 ORDER BY version LIMIT $2`, version, f74aPreflightPageSize)
		if err != nil {
			return out, fmt.Errorf("reading F74a schema inventory: %w", err)
		}
		read := 0
		for rows.Next() {
			if err := rows.Scan(&version); err != nil {
				rows.Close()
				return out, err
			}
			writeF74aPreflightField(h, []byte(version))
			out.SchemaVersions++
			read++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if read < f74aPreflightPageSize {
			break
		}
	}
	for _, journal := range []struct {
		table string
		count *int64
	}{
		{"f74a_observation_compaction_runs", &out.ArchiveRuns},
		{"f74a_observation_archive_batches", &out.ArchiveBatches},
	} {
		if err := hashF74aJournal(ctx, tx, h, journal.table, journal.count); err != nil {
			return out, err
		}
	}
	var expectedItems int64
	if runID != nil {
		var runDeleted, batchDeleted int64
		err := tx.QueryRow(ctx, `SELECT r.deleted_count,
			(SELECT count(*) FROM f74a_confirmed_deletion_items i WHERE i.run_id=r.id),
			(SELECT COALESCE(sum(b.deleted_count),0) FROM f74a_confirmed_deletion_batches b WHERE b.run_id=r.id)
			FROM f74a_confirmed_deletion_runs r WHERE r.id=$1`, *runID).Scan(&runDeleted, &expectedItems, &batchDeleted)
		if err != nil || runDeleted != expectedItems || batchDeleted != expectedItems {
			return out, fmt.Errorf("F74a deletion run, batch and item provenance counts disagree")
		}
		var mismatchedBatch bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM f74a_confirmed_deletion_batches b WHERE b.run_id=$1
			AND b.deleted_count <> (SELECT count(*) FROM f74a_confirmed_deletion_items i
				WHERE i.run_id=b.run_id AND i.batch_id=b.id))`, *runID).Scan(&mismatchedBatch)
		if err != nil || mismatchedBatch {
			return out, fmt.Errorf("F74a deletion batch and item provenance counts disagree")
		}
	}
	var seenItems, missingHotItems int64
	var cursor *uuid.UUID
	for {
		itemColumns := `NULL::bytea, NULL::uuid`
		itemJoin := ``
		args := []any{cursor, f74aPreflightPageSize}
		if runID != nil {
			itemColumns = `di.row_digest, di.batch_id`
			itemJoin = ` LEFT JOIN f74a_confirmed_deletion_items di ON di.observation_id=o.id AND di.run_id=$3`
			args = append(args, *runID)
		}
		rows, err := tx.Query(ctx, `SELECT o.id,
			f74a_observation_digest(to_jsonb(o),o.observed_at), a.row_digest,
			CASE WHEN a.id IS NOT NULL THEN f74a_observation_digest(to_jsonb(a),a.observed_at) END,
			h.id IS NOT NULL, a.archive_batch_id, a.archived_at,
			(SELECT count(*) FROM environment_service_state s WHERE s.current_observation_id=o.id),
			(SELECT COALESCE(jsonb_agg(jsonb_build_array(s.service_id,s.environment_id)
			 ORDER BY s.service_id,s.environment_id),'[]'::jsonb)::text
			 FROM environment_service_state s WHERE s.current_observation_id=o.id),
			o.deployment_unit_id, u.id IS NOT NULL, u.retired_at, `+itemColumns+`
			FROM runtime_observation_history o
			LEFT JOIN runtime_observation_archive a ON a.id=o.id
			LEFT JOIN runtime_observations h ON h.id=o.id
			LEFT JOIN deployment_units u ON u.id=o.deployment_unit_id`+itemJoin+`
			WHERE ($1::uuid IS NULL OR o.id > $1) ORDER BY o.id LIMIT $2`, args...)
		if err != nil {
			return out, fmt.Errorf("reading F74a restore inventory: %w", err)
		}
		read := 0
		for rows.Next() {
			var id uuid.UUID
			var actual, archived, archivedActual, itemDigest []byte
			var hot, unitExists bool
			var linkCount int64
			var links string
			var unitID, archiveBatchID, itemBatchID *uuid.UUID
			var archivedAt, retiredAt *time.Time
			if err := rows.Scan(&id, &actual, &archived, &archivedActual, &hot, &archiveBatchID, &archivedAt, &linkCount, &links, &unitID, &unitExists, &retiredAt, &itemDigest, &itemBatchID); err != nil {
				rows.Close()
				return out, err
			}
			if len(actual) != sha256.Size || (archived != nil && (len(archived) != sha256.Size || !bytes.Equal(archivedActual, archived) || !bytes.Equal(actual, archivedActual))) {
				rows.Close()
				return out, fmt.Errorf("F74a observation %s differs from immutable archive digest", id)
			}
			if archived != nil && (archiveBatchID == nil || archivedAt == nil) {
				rows.Close()
				return out, fmt.Errorf("F74a observation %s has incomplete archive provenance", id)
			}
			if unitID != nil && !unitExists {
				rows.Close()
				return out, fmt.Errorf("F74a observation %s has an orphan deployment unit", id)
			}
			if itemDigest != nil {
				seenItems++
				if archived == nil || itemBatchID == nil || len(itemDigest) != sha256.Size ||
					!bytes.Equal(itemDigest, archived) || !bytes.Equal(itemDigest, actual) {
					rows.Close()
					return out, fmt.Errorf("F74a deletion provenance differs from immutable archive")
				}
				if !hot {
					missingHotItems++
				}
			}
			if linkCount > 0 && !hot {
				rows.Close()
				return out, fmt.Errorf("F74a state-linked observation %s is absent from hot storage", id)
			}
			writeF74aPreflightField(h, id[:])
			writeF74aPreflightField(h, actual)
			flags := byte(0)
			if archived != nil {
				flags |= 1
				out.ArchivedRows++
				writeF74aPreflightField(h, archiveBatchID[:])
				writeF74aPreflightField(h, []byte(archivedAt.UTC().Format(time.RFC3339Nano)))
			}
			if hot || itemDigest != nil {
				flags |= 2
			}
			if linkCount > 0 {
				flags |= 4
				out.LinkedObservations++
			}
			writeF74aPreflightField(h, []byte(links))
			if unitID != nil {
				flags |= 8
				writeF74aPreflightField(h, unitID[:])
			}
			if retiredAt != nil {
				flags |= 16
				writeF74aPreflightField(h, []byte(retiredAt.UTC().Format(time.RFC3339Nano)))
			}
			writeF74aPreflightField(h, []byte{flags})
			out.Observations++
			cursor = &id
			read++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if read < f74aPreflightPageSize {
			break
		}
	}
	if seenItems != expectedItems {
		return out, fmt.Errorf("F74a deletion provenance has missing observation rows")
	}
	total, linked, hotCandidates, err := scanF74aCandidatesPaged(ctx, tx, cutoff)
	if err != nil {
		return out, err
	}
	if total != out.Observations || linked != out.LinkedObservations {
		return out, fmt.Errorf("F74a inventory state-link counts disagree")
	}
	out.HotCandidates = hotCandidates + missingHotItems
	writeF74aPreflightField(h, []byte(fmt.Sprintf("%d", out.HotCandidates)))
	out.InventorySHA256 = hex.EncodeToString(h.Sum(nil))
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return out, nil
}

func writeF74aPreflightField(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

// Journal rows are hashed independently of the archive references so a
// missing or changed run/batch cannot be hidden by unchanged observation IDs.
func hashF74aJournal(ctx context.Context, tx pgx.Tx, h hash.Hash, table string, count *int64) error {
	writeF74aPreflightField(h, []byte(table))
	var cursor *uuid.UUID
	for {
		query := fmt.Sprintf(`SELECT id,to_jsonb(j)::text FROM %s j
			WHERE ($1::uuid IS NULL OR id > $1) ORDER BY id LIMIT $2`, table)
		rows, err := tx.Query(ctx, query, cursor, f74aPreflightPageSize)
		if err != nil {
			return fmt.Errorf("reading F74a %s inventory: %w", table, err)
		}
		read := 0
		for rows.Next() {
			var id uuid.UUID
			var payload string
			if err := rows.Scan(&id, &payload); err != nil {
				rows.Close()
				return err
			}
			writeF74aPreflightField(h, id[:])
			writeF74aPreflightField(h, []byte(payload))
			*count++
			cursor = &id
			read++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if read < f74aPreflightPageSize {
			return nil
		}
	}
}

// scanF74aCandidatesPaged preserves predecessor state across bounded pages.
// It intentionally uses the same material-change predicate as CensusF74a.
func scanF74aCandidatesPaged(ctx context.Context, tx pgx.Tx, cutoff time.Time) (total, linked, hotCandidates int64, err error) {
	var cursorService, cursorEnvironment, cursorID *uuid.UUID
	var cursorObserved *time.Time
	var previous *domain.RuntimeObservation
	for {
		rows, queryErr := tx.Query(ctx, f74aObservationSelect+`
			WHERE ($1::uuid IS NULL OR
			 (o.service_id,o.environment_id,o.observed_at,o.id) >
			 ($1::uuid,$2::uuid,$3::timestamptz,$4::uuid))
			ORDER BY o.service_id,o.environment_id,o.observed_at,o.id LIMIT $5`,
			cursorService, cursorEnvironment, cursorObserved, cursorID, f74aPreflightPageSize)
		if queryErr != nil {
			return 0, 0, 0, fmt.Errorf("reading bounded F74a candidate page: %w", queryErr)
		}
		read := 0
		for rows.Next() {
			obs, isLinked, isHot, scanErr := scanF74aObservation(rows)
			if scanErr != nil {
				rows.Close()
				return 0, 0, 0, fmt.Errorf("reading F74a candidate: %w", scanErr)
			}
			total++
			if isLinked {
				linked++
			}
			if previous != nil && previous.ServiceID == obs.ServiceID && previous.EnvironmentID == obs.EnvironmentID &&
				!domain.RuntimeObservationMateriallyChanged(previous, &obs) && !isLinked && obs.ObservedAt.Before(cutoff) && isHot {
				hotCandidates++
			}
			previous = &obs
			cursorService, cursorEnvironment, cursorObserved, cursorID = &obs.ServiceID, &obs.EnvironmentID, &obs.ObservedAt, &obs.ID
			read++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, 0, 0, err
		}
		if read < f74aPreflightPageSize {
			return total, linked, hotCandidates, nil
		}
	}
}
