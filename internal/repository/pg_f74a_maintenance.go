package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// F74aCensus separates historical observations from the current linked
// observations that the canonical backfill actually publishes.
type F74aCensus struct {
	Cutoff                      time.Time
	Observations                int64
	LinkedObservations          int64
	UnlinkedObservations        int64
	MaterialRuns                int64
	SuppressibleObservations    int64
	HotSuppressibleObservations int64
	PackageRows                 int64
	SemanticPackages            int64
	DuplicatePackages           int64
	LegacyPackageCoordinates    int64
	Releases                    int64
	Signatures                  int64
	SBOMs                       int64
	EstimatedPublications       int64
}

const f74aObservationSelect = `SELECT o.id, o.service_id, o.environment_id,
	o.deployment_unit_id, o.observed_image_digest,
	COALESCE(o.observed_image_repo, ''), COALESCE(o.observed_container_id, ''),
	COALESCE(o.observed_host, ''), COALESCE(o.observed_version, ''),
	o.health_status, o.source, o.metadata, o.normalized_state,
	COALESCE(o.normalized_hash, ''), o.observed_at,
	EXISTS(SELECT 1 FROM environment_service_state s WHERE s.current_observation_id = o.id),
	EXISTS(SELECT 1 FROM runtime_observations h WHERE h.id = o.id)
	FROM runtime_observation_history o`

const f74aObservationScan = f74aObservationSelect + ` ORDER BY o.service_id, o.environment_id, o.observed_at, o.id`

func scanF74aObservation(rows pgx.Rows) (domain.RuntimeObservation, bool, bool, error) {
	var obs domain.RuntimeObservation
	var linked, hot bool
	var metadata, normalized []byte
	err := rows.Scan(&obs.ID, &obs.ServiceID, &obs.EnvironmentID, &obs.DeploymentUnitID,
		&obs.ObservedImageDigest, &obs.ObservedImageRepo, &obs.ObservedContainerID,
		&obs.ObservedHost, &obs.ObservedVersion, &obs.HealthStatus, &obs.Source,
		&metadata, &normalized, &obs.NormalizedHash, &obs.ObservedAt, &linked, &hot)
	if err != nil {
		return obs, false, false, err
	}
	if err := unmarshalJSON(metadata, &obs.Metadata, "observation metadata"); err != nil {
		return obs, false, false, err
	}
	if len(normalized) > 0 && string(normalized) != "null" {
		obs.NormalizedState = &domain.NormalizedObservation{}
		if err := unmarshalJSON(normalized, obs.NormalizedState, "normalized state"); err != nil {
			return obs, false, false, err
		}
	}
	return obs, linked, hot, nil
}

func scanF74aRuns(ctx context.Context, q pgQueryer, cutoff time.Time) (total, linked, material, suppressible, hotSuppressible int64, err error) {
	rows, err := q.Query(ctx, f74aObservationScan)
	if err != nil {
		return 0, 0, 0, 0, 0, fmt.Errorf("scanning F74a observations: %w", err)
	}
	defer rows.Close()
	var previous *domain.RuntimeObservation
	for rows.Next() {
		obs, isLinked, isHot, err := scanF74aObservation(rows)
		if err != nil {
			return 0, 0, 0, 0, 0, fmt.Errorf("reading F74a observation: %w", err)
		}
		total++
		if isLinked {
			linked++
		}
		if previous == nil || previous.ServiceID != obs.ServiceID || previous.EnvironmentID != obs.EnvironmentID || domain.RuntimeObservationMateriallyChanged(previous, &obs) {
			material++
		} else if !isLinked && obs.ObservedAt.Before(cutoff) {
			suppressible++
			if isHot {
				hotSuppressible++
			}
		}
		previous = &obs
	}
	return total, linked, material, suppressible, hotSuppressible, rows.Err()
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
	c.Observations, c.LinkedObservations, c.MaterialRuns, c.SuppressibleObservations, c.HotSuppressibleObservations, err = scanF74aRuns(ctx, tx, c.Cutoff)
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
