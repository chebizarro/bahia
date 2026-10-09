package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// F74aCensus separates historical observations from the current linked
// observations that the canonical backfill actually publishes.
type F74aCensus struct {
	Cutoff                   time.Time
	Observations             int64
	LinkedObservations       int64
	UnlinkedObservations     int64
	MaterialRuns             int64
	SuppressibleObservations int64
	PackageRows              int64
	SemanticPackages         int64
	DuplicatePackages        int64
	LegacyPackageCoordinates int64
	Releases                 int64
	Signatures               int64
	SBOMs                    int64
	EstimatedPublications    int64
}

const f74aObservationSelect = `SELECT o.id, o.service_id, o.environment_id,
	o.deployment_unit_id, o.observed_image_digest,
	COALESCE(o.observed_image_repo, ''), COALESCE(o.observed_container_id, ''),
	COALESCE(o.observed_host, ''), COALESCE(o.observed_version, ''),
	o.health_status, o.source, o.metadata, o.normalized_state,
	COALESCE(o.normalized_hash, ''), o.observed_at,
	EXISTS(SELECT 1 FROM environment_service_state s WHERE s.current_observation_id = o.id)
	FROM runtime_observations o`

const f74aObservationOrder = ` ORDER BY o.service_id, o.environment_id, o.observed_at, o.id`
const f74aObservationScan = f74aObservationSelect + f74aObservationOrder

func scanF74aObservation(rows pgx.Rows) (domain.RuntimeObservation, bool, error) {
	var obs domain.RuntimeObservation
	var linked bool
	var metadata, normalized []byte
	err := rows.Scan(&obs.ID, &obs.ServiceID, &obs.EnvironmentID, &obs.DeploymentUnitID,
		&obs.ObservedImageDigest, &obs.ObservedImageRepo, &obs.ObservedContainerID,
		&obs.ObservedHost, &obs.ObservedVersion, &obs.HealthStatus, &obs.Source,
		&metadata, &normalized, &obs.NormalizedHash, &obs.ObservedAt, &linked)
	if err != nil {
		return obs, false, err
	}
	if err := unmarshalJSON(metadata, &obs.Metadata, "observation metadata"); err != nil {
		return obs, false, err
	}
	if len(normalized) > 0 && string(normalized) != "null" {
		obs.NormalizedState = &domain.NormalizedObservation{}
		if err := unmarshalJSON(normalized, obs.NormalizedState, "normalized state"); err != nil {
			return obs, false, err
		}
	}
	return obs, linked, nil
}

func scanF74aRuns(ctx context.Context, q pgQueryer, cutoff time.Time, candidate func(uuid.UUID) error) (total, linked, material, suppressible int64, err error) {
	rows, err := q.Query(ctx, f74aObservationScan)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("scanning F74a observations: %w", err)
	}
	defer rows.Close()
	var previous *domain.RuntimeObservation
	for rows.Next() {
		obs, isLinked, err := scanF74aObservation(rows)
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("reading F74a observation: %w", err)
		}
		total++
		if isLinked {
			linked++
		}
		if previous == nil || previous.ServiceID != obs.ServiceID || previous.EnvironmentID != obs.EnvironmentID || domain.RuntimeObservationMateriallyChanged(previous, &obs) {
			material++
		} else if !isLinked && obs.ObservedAt.Before(cutoff) {
			suppressible++
			if candidate != nil {
				if err := candidate(obs.ID); err != nil {
					return 0, 0, 0, 0, err
				}
			}
		}
		previous = &obs
	}
	return total, linked, material, suppressible, rows.Err()
}

// CensusF74a uses one repeatable-read, read-only snapshot. It never reads
// nostr_events; that table mixes Bahia-authored and received events.
func CensusF74a(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) (F74aCensus, error) {
	var c F74aCensus
	if cutoff.IsZero() {
		cutoff = time.Now().UTC()
	}
	c.Cutoff = cutoff.UTC()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, item := range []struct {
		query string
		out   *int64
	}{
		{`SELECT COUNT(*) FROM sbom_packages`, &c.PackageRows},
		{`SELECT COUNT(*) FROM (SELECT 1 FROM sbom_packages GROUP BY sbom_id, name, version,
			COALESCE(ecosystem, ''), COALESCE(license, ''), COALESCE(purl, ''), COALESCE(cpe, '')) semantic`, &c.SemanticPackages},
		{`SELECT COUNT(*) FROM llm_releases`, &c.Releases},
		{`SELECT COUNT(*) FROM artifact_signatures`, &c.Signatures},
		{`SELECT COUNT(*) FROM artifact_sboms`, &c.SBOMs},
	} {
		if err := tx.QueryRow(ctx, item.query).Scan(item.out); err != nil {
			return c, fmt.Errorf("counting F74a family: %w", err)
		}
	}
	c.Observations, c.LinkedObservations, c.MaterialRuns, c.SuppressibleObservations, err = scanF74aRuns(ctx, tx, c.Cutoff, nil)
	if err != nil {
		return c, err
	}
	c.UnlinkedObservations = c.Observations - c.LinkedObservations
	c.DuplicatePackages = c.PackageRows - c.SemanticPackages
	c.LegacyPackageCoordinates = c.PackageRows
	c.EstimatedPublications = c.Releases + c.Signatures + c.SBOMs + c.SemanticPackages + c.LegacyPackageCoordinates + c.LinkedObservations
	if err := tx.Commit(ctx); err != nil {
		return c, err
	}
	return c, nil
}

// CompactF74aObservations deletes only old, unlinked no-op samples. The backup
// reference is mandatory; the operator must independently prove it is restorable.
// Each page and delete uses one short transaction and a keyset cursor. Linkage
// and cutoff are checked again by the DELETE predicate. onBatch sees committed
// IDs and can stop the run; a retry is idempotent.
func CompactF74aObservations(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, batchSize int, backupID string, onBatch func([]uuid.UUID) error) (int64, error) {
	if strings.TrimSpace(backupID) == "" {
		return 0, fmt.Errorf("compaction requires a verified restorable backup reference")
	}
	if cutoff.IsZero() || !cutoff.Before(time.Now().UTC()) {
		return 0, fmt.Errorf("compaction requires a fixed past cutoff")
	}
	if batchSize < 1 || batchSize > F74aPageLimit {
		return 0, fmt.Errorf("compaction batch size must be between 1 and %d", F74aPageLimit)
	}
	type cursor struct {
		serviceID, envID uuid.UUID
		observedAt       time.Time
		id               uuid.UUID
	}
	var after cursor
	first := true
	var previous *domain.RuntimeObservation
	var deleted int64
	query := f74aObservationSelect + ` WHERE ($1::boolean OR (o.service_id, o.environment_id, o.observed_at, o.id) > ($2, $3, $4, $5))` + f74aObservationOrder + ` LIMIT $6`
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return deleted, err
		}
		rows, err := tx.Query(ctx, query, first, after.serviceID, after.envID, after.observedAt, after.id, batchSize)
		if err != nil {
			_ = tx.Rollback(context.Background())
			return deleted, err
		}
		candidates := make([]uuid.UUID, 0, batchSize)
		seen := 0
		for rows.Next() {
			obs, linked, err := scanF74aObservation(rows)
			if err != nil {
				rows.Close()
				_ = tx.Rollback(context.Background())
				return deleted, err
			}
			if previous != nil && previous.ServiceID == obs.ServiceID && previous.EnvironmentID == obs.EnvironmentID &&
				!domain.RuntimeObservationMateriallyChanged(previous, &obs) && !linked && obs.ObservedAt.Before(cutoff) {
				candidates = append(candidates, obs.ID)
			}
			previous = &obs
			after = cursor{obs.ServiceID, obs.EnvironmentID, obs.ObservedAt, obs.ID}
			seen++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback(context.Background())
			return deleted, err
		}
		rows.Close()
		var committed []uuid.UUID
		if len(candidates) != 0 {
			result, err := tx.Query(ctx, `DELETE FROM runtime_observations o
				WHERE o.id = ANY($1::uuid[]) AND o.observed_at < $2
				AND NOT EXISTS (SELECT 1 FROM environment_service_state s WHERE s.current_observation_id = o.id)
				RETURNING o.id`, candidates, cutoff)
			if err != nil {
				_ = tx.Rollback(context.Background())
				return deleted, err
			}
			committed = make([]uuid.UUID, 0, len(candidates))
			for result.Next() {
				var id uuid.UUID
				if err := result.Scan(&id); err != nil {
					result.Close()
					_ = tx.Rollback(context.Background())
					return deleted, err
				}
				committed = append(committed, id)
			}
			if err := result.Err(); err != nil {
				result.Close()
				_ = tx.Rollback(context.Background())
				return deleted, err
			}
			result.Close()
		}
		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(context.Background())
			return deleted, err
		}
		deleted += int64(len(committed))
		if onBatch != nil && len(committed) != 0 {
			if err := onBatch(committed); err != nil {
				return deleted, err
			}
		}
		if seen < batchSize {
			return deleted, nil
		}
		first = false
	}
}
