package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// F74aPageLimit bounds every source read, independent of caller input.
const F74aPageLimit = 250

// F74aStateCursor orders current observations by their owning state coordinate.
type F74aStateCursor struct {
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
}

// PgF74aBackfillSource reads only the derived PostgreSQL families needed by
// the canonical backfill. Cursors are exclusive; a nil UUID starts a family.
// Pages are not a consistent snapshot: live inserts with IDs behind a cursor
// require the caller's durable dirty-generation/restart pass before completion.
type PgF74aBackfillSource struct{ pool pgQueryer }

func NewPgF74aBackfillSource(pool *pgxpool.Pool) *PgF74aBackfillSource {
	return &PgF74aBackfillSource{pool: pool}
}

func newPgF74aBackfillSourceWithDB(db pgQueryer) *PgF74aBackfillSource {
	return &PgF74aBackfillSource{pool: db}
}

func f74aLimit(limit int) (int, error) {
	if limit < 1 || limit > F74aPageLimit {
		return 0, fmt.Errorf("F74a page limit must be between 1 and %d", F74aPageLimit)
	}
	return limit, nil
}

func (r *PgF74aBackfillSource) ListReleasesAfter(ctx context.Context, after uuid.UUID, limit int) ([]domain.LLMRelease, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT `+llmReleaseColumns+` FROM llm_releases WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("querying F74a releases: %w", err)
	}
	defer rows.Close()
	reader := &PgLLMReleaseRepository{pool: r.pool}
	out := make([]domain.LLMRelease, 0, limit)
	for rows.Next() {
		item, err := reader.scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (r *PgF74aBackfillSource) ListSignaturesAfter(ctx context.Context, after uuid.UUID, limit int) ([]domain.ArtifactSignature, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT `+sigColumns+` FROM artifact_signatures WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("querying F74a signatures: %w", err)
	}
	defer rows.Close()
	return (&PgArtifactSignatureRepository{}).scanSigs(rows)
}

func (r *PgF74aBackfillSource) ListSBOMsAfter(ctx context.Context, after uuid.UUID, limit int) ([]domain.ArtifactSBOM, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT `+sbomColumns+` FROM artifact_sboms WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("querying F74a SBOMs: %w", err)
	}
	defer rows.Close()
	reader := &PgSBOMRepository{}
	out := make([]domain.ArtifactSBOM, 0, limit)
	for rows.Next() {
		item, err := reader.scanSBOM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

// ListSemanticPackagesAfter returns the lowest UUID for each exact package
// coordinate. SQL NULL and Go empty string have the same coordinate, matching
// the canonical publisher. The existing SBOM index narrows the anti-join;
// production-scale query plans remain unverified without an online index path.
func (r *PgF74aBackfillSource) ListSemanticPackagesAfter(ctx context.Context, after uuid.UUID, limit int) ([]domain.SBOMPackage, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.sbom_id, p.name, p.version, p.ecosystem, p.license, p.purl, p.cpe
		FROM sbom_packages p
		WHERE p.id > $1 AND NOT EXISTS (
			SELECT 1 FROM sbom_packages older
			WHERE older.sbom_id = p.sbom_id AND older.name = p.name AND older.version = p.version
			AND COALESCE(older.ecosystem, '') = COALESCE(p.ecosystem, '')
			AND COALESCE(older.license, '') = COALESCE(p.license, '')
			AND COALESCE(older.purl, '') = COALESCE(p.purl, '')
			AND COALESCE(older.cpe, '') = COALESCE(p.cpe, '') AND older.id < p.id)
		ORDER BY p.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("querying semantic F74a packages: %w", err)
	}
	defer rows.Close()
	return (&PgSBOMRepository{}).scanPackages(rows)
}

const f74aSourceObservationColumns = `o.id, o.service_id, o.environment_id, o.deployment_unit_id,
	o.observed_image_digest, COALESCE(o.observed_image_repo, ''),
	COALESCE(o.observed_container_id, ''), COALESCE(o.observed_host, ''),
	COALESCE(o.observed_version, ''), o.health_status, o.source,
	o.metadata, o.normalized_state, COALESCE(o.normalized_hash, ''), o.observed_at`

// ListLinkedObservationsAfter walks state coordinates, not observation history.
// A bad cross-coordinate link is rejected rather than published to the wrong
// canonical address.
func (r *PgF74aBackfillSource) ListLinkedObservationsAfter(ctx context.Context, after F74aStateCursor, limit int) ([]domain.RuntimeObservation, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	columns := f74aSourceObservationColumns
	rows, err := r.pool.Query(ctx, `SELECT s.service_id, s.environment_id, `+columns+`
		FROM environment_service_state s JOIN runtime_observations o ON o.id = s.current_observation_id
		WHERE (s.service_id, s.environment_id) > ($1, $2)
		ORDER BY s.service_id, s.environment_id LIMIT $3`, after.ServiceID, after.EnvironmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying linked F74a observations: %w", err)
	}
	defer rows.Close()
	out := make([]domain.RuntimeObservation, 0, limit)
	for rows.Next() {
		var serviceID, envID uuid.UUID
		var obs domain.RuntimeObservation
		var metaJSON, normalizedJSON []byte
		if err := rows.Scan(&serviceID, &envID, &obs.ID, &obs.ServiceID, &obs.EnvironmentID,
			&obs.DeploymentUnitID, &obs.ObservedImageDigest, &obs.ObservedImageRepo,
			&obs.ObservedContainerID, &obs.ObservedHost, &obs.ObservedVersion,
			&obs.HealthStatus, &obs.Source, &metaJSON, &normalizedJSON,
			&obs.NormalizedHash, &obs.ObservedAt); err != nil {
			return nil, fmt.Errorf("scanning linked F74a observation: %w", err)
		}
		if obs.ServiceID != serviceID || obs.EnvironmentID != envID {
			return nil, fmt.Errorf("state-linked runtime observation %s has invalid coordinate", obs.ID)
		}
		if err := unmarshalJSON(metaJSON, &obs.Metadata, "observation metadata"); err != nil {
			return nil, err
		}
		if len(normalizedJSON) > 0 && string(normalizedJSON) != "null" {
			obs.NormalizedState = &domain.NormalizedObservation{}
			if err := unmarshalJSON(normalizedJSON, obs.NormalizedState, "normalized state"); err != nil {
				return nil, err
			}
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

// ListLegacyPackagesAfter includes every historical row so the scheduler can
// tombstone every old UUID address after staging the semantic representative.
func (r *PgF74aBackfillSource) ListLegacyPackagesAfter(ctx context.Context, after uuid.UUID, limit int) ([]domain.SBOMPackage, error) {
	limit, err := f74aLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT `+pkgColumns+` FROM sbom_packages WHERE id > $1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("querying legacy F74a packages: %w", err)
	}
	defer rows.Close()
	return (&PgSBOMRepository{}).scanPackages(rows)
}
