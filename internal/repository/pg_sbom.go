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

const sbomColumns = `id, artifact_id, format, source_url, package_count,
	vulnerability_count, critical_count, high_count, raw_hash, metadata, created_at`

const pkgColumns = `id, sbom_id, name, version, ecosystem, license, purl, cpe`

// nilIfEmpty returns nil if the string is empty, otherwise returns the pointer.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// PgSBOMRepository implements SBOMRepository using PostgreSQL.
type PgSBOMRepository struct {
	pool *pgxpool.Pool
}

// NewPgSBOMRepository creates a new PostgreSQL SBOM repository.
func NewPgSBOMRepository(pool *pgxpool.Pool) *PgSBOMRepository {
	return &PgSBOMRepository{pool: pool}
}

// CreateSBOM inserts a new SBOM record.
func (r *PgSBOMRepository) CreateSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error {
	if sbom.ID == uuid.Nil {
		sbom.ID = uuid.New()
	}
	sbom.CreatedAt = time.Now().UTC()

	metaJSON, err := marshalJSON(sbom.Metadata, "metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO artifact_sboms
			(id, artifact_id, format, source_url, package_count,
			 vulnerability_count, critical_count, high_count, raw_hash, metadata, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		sbom.ID, sbom.ArtifactID, string(sbom.Format), nilIfEmpty(sbom.SourceURL),
		sbom.PackageCount, sbom.VulnerabilityCount, sbom.CriticalCount, sbom.HighCount,
		nilIfEmpty(sbom.RawHash), metaJSON, sbom.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting SBOM: %w", err)
	}
	return nil
}

// GetSBOMByID retrieves an SBOM by its ID.
func (r *PgSBOMRepository) GetSBOMByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSBOM, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_sboms WHERE id = $1", sbomColumns), id)
	return r.scanSBOM(row)
}

// GetSBOMByArtifact retrieves the SBOM for an artifact.
func (r *PgSBOMRepository) GetSBOMByArtifact(ctx context.Context, artifactID uuid.UUID) (*domain.ArtifactSBOM, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_sboms WHERE artifact_id = $1 ORDER BY created_at DESC LIMIT 1", sbomColumns),
		artifactID)
	return r.scanSBOM(row)
}

// GetSBOMByHash finds an SBOM by its raw content hash (idempotency).
func (r *PgSBOMRepository) GetSBOMByHash(ctx context.Context, rawHash string) (*domain.ArtifactSBOM, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_sboms WHERE raw_hash = $1", sbomColumns),
		rawHash)
	return r.scanSBOM(row)
}

// CreatePackages batch-inserts SBOM package records.
func (r *PgSBOMRepository) CreatePackages(ctx context.Context, packages []domain.SBOMPackage) error {
	if len(packages) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for i := range packages {
		pkg := &packages[i]
		if pkg.ID == uuid.Nil {
			pkg.ID = uuid.New()
		}
		batch.Queue(
			`INSERT INTO sbom_packages (id, sbom_id, name, version, ecosystem, license, purl, cpe)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			pkg.ID, pkg.SBOMID, pkg.Name, pkg.Version,
			nilIfEmpty(pkg.Ecosystem), nilIfEmpty(pkg.License),
			nilIfEmpty(pkg.PURL), nilIfEmpty(pkg.CPE),
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range packages {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("inserting SBOM package: %w", err)
		}
	}
	return nil
}

// ListPackagesBySBOM returns all packages in an SBOM.
func (r *PgSBOMRepository) ListPackagesBySBOM(ctx context.Context, sbomID uuid.UUID) ([]domain.SBOMPackage, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM sbom_packages WHERE sbom_id = $1 ORDER BY name", pkgColumns),
		sbomID)
	if err != nil {
		return nil, fmt.Errorf("querying packages: %w", err)
	}
	defer rows.Close()
	return r.scanPackages(rows)
}

// SearchPackagesByName searches for packages by name across all SBOMs.
func (r *PgSBOMRepository) SearchPackagesByName(ctx context.Context, name string, limit int) ([]domain.SBOMPackage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM sbom_packages WHERE name ILIKE $1 ORDER BY name LIMIT $2", pkgColumns),
		"%"+name+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("searching packages: %w", err)
	}
	defer rows.Close()
	return r.scanPackages(rows)
}

func (r *PgSBOMRepository) scanSBOM(row pgx.Row) (*domain.ArtifactSBOM, error) {
	var s domain.ArtifactSBOM
	var format string
	var sourceURL, rawHash *string
	var metaJSON []byte
	err := row.Scan(
		&s.ID, &s.ArtifactID, &format, &sourceURL,
		&s.PackageCount, &s.VulnerabilityCount, &s.CriticalCount, &s.HighCount,
		&rawHash, &metaJSON, &s.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning SBOM: %w", err)
	}
	s.Format = domain.SBOMFormat(format)
	if sourceURL != nil {
		s.SourceURL = *sourceURL
	}
	if rawHash != nil {
		s.RawHash = *rawHash
	}
	if err := unmarshalJSON(metaJSON, &s.Metadata, "metadata"); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *PgSBOMRepository) scanPackages(rows pgx.Rows) ([]domain.SBOMPackage, error) {
	var pkgs []domain.SBOMPackage
	for rows.Next() {
		var p domain.SBOMPackage
		var ecosystem, license, purl, cpe *string
		err := rows.Scan(&p.ID, &p.SBOMID, &p.Name, &p.Version, &ecosystem, &license, &purl, &cpe)
		if err != nil {
			return nil, fmt.Errorf("scanning package: %w", err)
		}
		if ecosystem != nil {
			p.Ecosystem = *ecosystem
		}
		if license != nil {
			p.License = *license
		}
		if purl != nil {
			p.PURL = *purl
		}
		if cpe != nil {
			p.CPE = *cpe
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, rows.Err()
}
