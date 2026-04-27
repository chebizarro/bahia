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

// PgArtifactRepository is a PostgreSQL implementation of ArtifactRepository.
type PgArtifactRepository struct {
	pool *pgxpool.Pool
}

func NewPgArtifactRepository(pool *pgxpool.Pool) *PgArtifactRepository {
	return &PgArtifactRepository{pool: pool}
}

func (r *PgArtifactRepository) Create(ctx context.Context, a *domain.Artifact) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.CreatedAt = time.Now().UTC()

	metaJSON, err := marshalJSON(a.Metadata, "artifact metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO artifacts (id, build_id, service_id, image_repo, image_tag, image_digest, manifest_media_type, size_bytes, sbom_url, signature_ref, scan_status, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, a.ID, a.BuildID, a.ServiceID, a.ImageRepo, a.ImageTag, a.ImageDigest, a.ManifestMediaType, a.SizeBytes, a.SBOMURL, a.SignatureRef, a.ScanStatus, metaJSON, a.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting artifact: %w", err)
	}
	return nil
}

func (r *PgArtifactRepository) scanArtifact(row pgx.Row) (*domain.Artifact, error) {
	a := &domain.Artifact{}
	var metaJSON []byte
	err := row.Scan(&a.ID, &a.BuildID, &a.ServiceID, &a.ImageRepo, &a.ImageTag, &a.ImageDigest, &a.ManifestMediaType, &a.SizeBytes, &a.SBOMURL, &a.SignatureRef, &a.ScanStatus, &metaJSON, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &a.Metadata, "artifact metadata"); err != nil {
		return nil, err
	}
	return a, nil
}

const artifactColumns = `id, build_id, service_id, image_repo, image_tag, image_digest, manifest_media_type, size_bytes, sbom_url, signature_ref, scan_status, metadata, created_at`

func (r *PgArtifactRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Artifact, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE id = $1`, id)
	a, err := r.scanArtifact(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying artifact by id: %w", err)
	}
	return a, nil
}

func (r *PgArtifactRepository) GetByDigest(ctx context.Context, repo, digest string) (*domain.Artifact, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE image_repo = $1 AND image_digest = $2`, repo, digest)
	a, err := r.scanArtifact(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying artifact by digest: %w", err)
	}
	return a, nil
}

func (r *PgArtifactRepository) ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE service_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, serviceID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []domain.Artifact
	for rows.Next() {
		var a domain.Artifact
		var metaJSON []byte
		if err := rows.Scan(&a.ID, &a.BuildID, &a.ServiceID, &a.ImageRepo, &a.ImageTag, &a.ImageDigest, &a.ManifestMediaType, &a.SizeBytes, &a.SBOMURL, &a.SignatureRef, &a.ScanStatus, &metaJSON, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning artifact: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &a.Metadata, "artifact metadata"); err != nil {
			return nil, fmt.Errorf("reading artifact %s: %w", a.ID, err)
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

func (r *PgArtifactRepository) ListByBuild(ctx context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+artifactColumns+` FROM artifacts WHERE build_id = $1 ORDER BY created_at DESC`, buildID)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts by build: %w", err)
	}
	defer rows.Close()

	var artifacts []domain.Artifact
	for rows.Next() {
		var a domain.Artifact
		var metaJSON []byte
		if err := rows.Scan(&a.ID, &a.BuildID, &a.ServiceID, &a.ImageRepo, &a.ImageTag, &a.ImageDigest, &a.ManifestMediaType, &a.SizeBytes, &a.SBOMURL, &a.SignatureRef, &a.ScanStatus, &metaJSON, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning artifact: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &a.Metadata, "artifact metadata"); err != nil {
			return nil, fmt.Errorf("reading artifact %s: %w", a.ID, err)
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}
