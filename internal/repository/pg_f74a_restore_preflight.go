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
)

// F74aRestorePreflight is an unauthenticated, read-only inventory of one SQL
// snapshot. Equal source/isolated-restore inventories detect copy divergence;
// they do not establish backup provenance, isolation, retention, or custody.
// No deletion path accepts this value as an authorization receipt.
type F74aRestorePreflight struct {
	DatabaseName       string
	Cutoff             time.Time
	SchemaVersions     int64
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
	var cursor *uuid.UUID
	for {
		rows, err := tx.Query(ctx, `SELECT o.id,
			f74a_observation_digest(to_jsonb(o),o.observed_at), a.row_digest,
			h.id IS NOT NULL, a.archive_batch_id, a.archived_at,
			EXISTS (SELECT 1 FROM environment_service_state s WHERE s.current_observation_id=o.id),
			o.deployment_unit_id, u.id IS NOT NULL, u.retired_at
			FROM runtime_observation_history o
			LEFT JOIN runtime_observation_archive a ON a.id=o.id
			LEFT JOIN runtime_observations h ON h.id=o.id
			LEFT JOIN deployment_units u ON u.id=o.deployment_unit_id
			WHERE ($1::uuid IS NULL OR o.id > $1) ORDER BY o.id LIMIT $2`, cursor, f74aPreflightPageSize)
		if err != nil {
			return out, fmt.Errorf("reading F74a restore inventory: %w", err)
		}
		read := 0
		for rows.Next() {
			var id uuid.UUID
			var actual, archived []byte
			var hot, linked, unitExists bool
			var unitID, archiveBatchID *uuid.UUID
			var archivedAt, retiredAt *time.Time
			if err := rows.Scan(&id, &actual, &archived, &hot, &archiveBatchID, &archivedAt, &linked, &unitID, &unitExists, &retiredAt); err != nil {
				rows.Close()
				return out, err
			}
			if len(actual) != sha256.Size || (archived != nil && (len(archived) != sha256.Size || !bytes.Equal(actual, archived))) {
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
			if linked && !hot {
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
			if hot {
				flags |= 2
			}
			if linked {
				flags |= 4
				out.LinkedObservations++
			}
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
	total, linked, _, _, hotCandidates, err := scanF74aRuns(ctx, tx, cutoff)
	if err != nil {
		return out, err
	}
	if total != out.Observations || linked != out.LinkedObservations {
		return out, fmt.Errorf("F74a inventory state-link counts disagree")
	}
	out.HotCandidates = hotCandidates
	writeF74aPreflightField(h, []byte(fmt.Sprintf("%d", hotCandidates)))
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
