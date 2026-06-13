package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

const sbomColumns = `id, artifact_id, format, source_url, package_count,
	vulnerability_count, critical_count, high_count, raw_hash, metadata, created_at`

const pkgColumns = `id, sbom_id, name, version, ecosystem, license, purl, cpe`

const sbomManifestColumns = `id, subject_type, subject_id, subject_name, subject_digest,
	format, media_type, storage_type, storage_uri, payload_sha256,
	generator_id, generator_version, generator_pubkey, package_count,
	vulnerability_count, critical_count, high_count, ntia_status, ntia_metadata,
	reference_event_id, reference_d_tag, availability_event_id, availability_d_tag,
	publish_state, publish_error, source_kind, metadata, created_at, updated_at, published_at`

const manifestPkgColumns = `id, manifest_id, name, version, ecosystem, license, purl, cpe`

type sbomDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Begin(ctx context.Context) (pgx.Tx, error)
}

type sbomQuerier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// nilIfEmpty returns nil if the string is empty, otherwise returns the pointer.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// PgSBOMRepository implements SBOMRepository using PostgreSQL.
type PgSBOMRepository struct {
	pool sbomDB
}

// NewPgSBOMRepository creates a new PostgreSQL SBOM repository.
func NewPgSBOMRepository(pool *pgxpool.Pool) *PgSBOMRepository {
	return &PgSBOMRepository{pool: pool}
}

// CreateSBOM inserts a new SBOM record.
func (r *PgSBOMRepository) CreateSBOM(ctx context.Context, sbom *domain.ArtifactSBOM) error {
	return createArtifactSBOM(ctx, r.pool, sbom)
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
	return createArtifactPackages(ctx, r.pool, packages)
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

// CreateManifest inserts or updates a subject-neutral SBOM manifest projection.
func (r *PgSBOMRepository) CreateManifest(ctx context.Context, manifest *domain.SBOMManifest) error {
	return createManifest(ctx, r.pool, manifest)
}

// ProjectManifest stores a manifest, its packages, and artifact compatibility rows when applicable.
func (r *PgSBOMRepository) ProjectManifest(ctx context.Context, manifest *domain.SBOMManifest, packages []domain.SBOMManifestPackage) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning SBOM manifest projection: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := createManifest(ctx, tx, manifest); err != nil {
		return err
	}
	for i := range packages {
		packages[i].ManifestID = manifest.ID
	}
	if err := createManifestPackages(ctx, tx, packages); err != nil {
		return err
	}
	if manifest.Subject.Type == domain.SBOMSubjectArtifact {
		artifactID, err := uuid.Parse(manifest.Subject.ID)
		if err != nil {
			return fmt.Errorf("artifact SBOM subject ID %q is not a UUID: %w", manifest.Subject.ID, err)
		}
		artifactSBOM := artifactSBOMFromManifest(manifest, artifactID)
		if err := createArtifactCompatibilitySBOM(ctx, tx, artifactSBOM); err != nil {
			return err
		}
		artifactPackages := make([]domain.SBOMPackage, len(packages))
		for i, pkg := range packages {
			artifactPackages[i] = domain.SBOMPackage{
				SBOMID:    artifactSBOM.ID,
				Name:      pkg.Name,
				Version:   pkg.Version,
				Ecosystem: pkg.Ecosystem,
				License:   pkg.License,
				PURL:      pkg.PURL,
				CPE:       pkg.CPE,
			}
		}
		if err := createArtifactPackages(ctx, tx, artifactPackages); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing SBOM manifest projection: %w", err)
	}
	return nil
}

// GetManifestByID retrieves a subject-neutral SBOM manifest by ID.
func (r *PgSBOMRepository) GetManifestByID(ctx context.Context, id uuid.UUID) (*domain.SBOMManifest, error) {
	row := r.pool.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM sbom_manifests WHERE id = $1", sbomManifestColumns), id)
	return r.scanManifest(row)
}

// ListManifestsBySubject lists manifests for a subject version.
func (r *PgSBOMRepository) ListManifestsBySubject(ctx context.Context, subject domain.SBOMSubject, limit int) ([]domain.SBOMManifest, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf(`SELECT %s FROM sbom_manifests
			WHERE subject_type = $1 AND subject_id = $2 AND subject_digest = $3
			ORDER BY created_at DESC LIMIT $4`, sbomManifestColumns),
		string(subject.Type), subject.ID, subject.Digest, limit)
	if err != nil {
		return nil, fmt.Errorf("listing SBOM manifests: %w", err)
	}
	defer rows.Close()
	var manifests []domain.SBOMManifest
	for rows.Next() {
		manifest, err := r.scanManifest(rows)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, *manifest)
	}
	return manifests, rows.Err()
}

// UpdateManifestPublishState updates Nostr publication state for a manifest projection.
func (r *PgSBOMRepository) UpdateManifestPublishState(ctx context.Context, id uuid.UUID, state domain.SBOMPublishState, referenceEventID, availabilityEventID, publishError string) error {
	var publishedAt any
	if state == domain.SBOMPublishPublished {
		publishedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE sbom_manifests
			SET publish_state = $2, reference_event_id = $3, availability_event_id = $4,
				publish_error = $5, published_at = COALESCE($6, published_at), updated_at = $7
			WHERE id = $1`,
		id, string(state), nilIfEmpty(referenceEventID), nilIfEmpty(availabilityEventID), nilIfEmpty(publishError), publishedAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("updating SBOM manifest publish state: %w", err)
	}
	return nil
}

// CreateManifestPackages batch-inserts subject-neutral package records.
func (r *PgSBOMRepository) CreateManifestPackages(ctx context.Context, packages []domain.SBOMManifestPackage) error {
	return createManifestPackages(ctx, r.pool, packages)
}

// ListPackagesByManifest returns packages for a subject-neutral manifest.
func (r *PgSBOMRepository) ListPackagesByManifest(ctx context.Context, manifestID uuid.UUID) ([]domain.SBOMManifestPackage, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM sbom_manifest_packages WHERE manifest_id = $1 ORDER BY name", manifestPkgColumns), manifestID)
	if err != nil {
		return nil, fmt.Errorf("querying SBOM manifest packages: %w", err)
	}
	defer rows.Close()
	return r.scanManifestPackages(rows)
}

// SearchManifestPackagesByName searches packages across subject-neutral SBOM manifests.
func (r *PgSBOMRepository) SearchManifestPackagesByName(ctx context.Context, name string, limit int) ([]domain.SBOMManifestPackage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM sbom_manifest_packages WHERE name ILIKE $1 ORDER BY name LIMIT $2", manifestPkgColumns),
		"%"+name+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("searching SBOM manifest packages: %w", err)
	}
	defer rows.Close()
	return r.scanManifestPackages(rows)
}

func createArtifactSBOM(ctx context.Context, db sbomQuerier, sbom *domain.ArtifactSBOM) error {
	if sbom.ID == uuid.Nil {
		sbom.ID = uuid.New()
	}
	sbom.CreatedAt = time.Now().UTC()

	metaJSON, err := marshalJSON(sbom.Metadata, "metadata")
	if err != nil {
		return err
	}

	_, err = db.Exec(ctx,
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

func createArtifactCompatibilitySBOM(ctx context.Context, db sbomQuerier, sbom *domain.ArtifactSBOM) error {
	if sbom.ID == uuid.Nil {
		sbom.ID = uuid.New()
	}
	sbom.CreatedAt = time.Now().UTC()
	metaJSON, err := marshalJSON(sbom.Metadata, "metadata")
	if err != nil {
		return err
	}
	row := db.QueryRow(ctx,
		`INSERT INTO artifact_sboms
			(id, artifact_id, format, source_url, package_count,
			 vulnerability_count, critical_count, high_count, raw_hash, metadata, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (raw_hash) WHERE raw_hash IS NOT NULL
		DO UPDATE SET format = EXCLUDED.format,
			source_url = EXCLUDED.source_url,
			package_count = EXCLUDED.package_count,
			vulnerability_count = EXCLUDED.vulnerability_count,
			critical_count = EXCLUDED.critical_count,
			high_count = EXCLUDED.high_count,
			metadata = EXCLUDED.metadata
		 RETURNING id`,
		sbom.ID, sbom.ArtifactID, string(sbom.Format), nilIfEmpty(sbom.SourceURL),
		sbom.PackageCount, sbom.VulnerabilityCount, sbom.CriticalCount, sbom.HighCount,
		nilIfEmpty(sbom.RawHash), metaJSON, sbom.CreatedAt)
	if err := row.Scan(&sbom.ID); err != nil {
		return fmt.Errorf("upserting artifact SBOM compatibility projection: %w", err)
	}
	return nil
}

func createArtifactPackages(ctx context.Context, db sbomQuerier, packages []domain.SBOMPackage) error {
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

	br := db.SendBatch(ctx, batch)
	defer br.Close()

	for range packages {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("inserting SBOM package: %w", err)
		}
	}
	return nil
}

func createManifest(ctx context.Context, db sbomQuerier, manifest *domain.SBOMManifest) error {
	if manifest.ID == uuid.Nil {
		manifest.ID = uuid.New()
	}
	now := time.Now().UTC()
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = now
	}
	manifest.UpdatedAt = now
	if manifest.PublishState == "" {
		manifest.PublishState = domain.SBOMPublishDraft
	}
	if manifest.SourceKind == "" {
		manifest.SourceKind = domain.SBOMSourceImported
	}
	if manifest.StorageType == "" {
		manifest.StorageType = domain.SBOMStorageBlossom
	}

	ntiaJSON, err := marshalJSON(manifest.NTIA, "ntia metadata")
	if err != nil {
		return err
	}
	metaJSON, err := marshalJSON(manifest.Metadata, "metadata")
	if err != nil {
		return err
	}

	row := db.QueryRow(ctx,
		fmt.Sprintf(`INSERT INTO sbom_manifests
			(id, subject_type, subject_id, subject_name, subject_digest, format, media_type,
				storage_type, storage_uri, payload_sha256, generator_id, generator_version,
				generator_pubkey, package_count, vulnerability_count, critical_count, high_count,
				ntia_status, ntia_metadata, reference_event_id, reference_d_tag,
				availability_event_id, availability_d_tag, publish_state, publish_error,
				source_kind, metadata, created_at, updated_at, published_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
				$21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
			ON CONFLICT (subject_type, subject_id, subject_digest, format, generator_id, payload_sha256)
			DO UPDATE SET subject_name = EXCLUDED.subject_name,
			media_type = EXCLUDED.media_type,
			storage_type = EXCLUDED.storage_type,
			storage_uri = EXCLUDED.storage_uri,
			generator_version = EXCLUDED.generator_version,
			generator_pubkey = EXCLUDED.generator_pubkey,
			package_count = EXCLUDED.package_count,
			vulnerability_count = EXCLUDED.vulnerability_count,
			critical_count = EXCLUDED.critical_count,
			high_count = EXCLUDED.high_count,
			ntia_status = EXCLUDED.ntia_status,
			ntia_metadata = EXCLUDED.ntia_metadata,
			reference_event_id = COALESCE(EXCLUDED.reference_event_id, sbom_manifests.reference_event_id),
			reference_d_tag = COALESCE(EXCLUDED.reference_d_tag, sbom_manifests.reference_d_tag),
			availability_event_id = COALESCE(EXCLUDED.availability_event_id, sbom_manifests.availability_event_id),
			availability_d_tag = COALESCE(EXCLUDED.availability_d_tag, sbom_manifests.availability_d_tag),
			publish_state = EXCLUDED.publish_state,
			publish_error = EXCLUDED.publish_error,
			source_kind = EXCLUDED.source_kind,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at,
			published_at = COALESCE(EXCLUDED.published_at, sbom_manifests.published_at)
			RETURNING %s`, sbomManifestColumns),
		manifest.ID, string(manifest.Subject.Type), manifest.Subject.ID, nilIfEmpty(manifest.Subject.DisplayName), manifest.Subject.Digest,
		string(manifest.Format), nilIfEmpty(manifest.MediaType), string(manifest.StorageType), manifest.StorageURI, manifest.PayloadSHA256,
		manifest.Generator.ID, nilIfEmpty(manifest.Generator.Version), nilIfEmpty(manifest.Generator.Pubkey), manifest.PackageCount,
		manifest.VulnerabilityCount, manifest.CriticalCount, manifest.HighCount, nilIfEmpty(manifest.NTIAStatus), ntiaJSON,
		nilIfEmpty(manifest.ReferenceEventID), nilIfEmpty(manifest.ReferenceDTag), nilIfEmpty(manifest.AvailabilityEventID), nilIfEmpty(manifest.AvailabilityDTag),
		string(manifest.PublishState), nilIfEmpty(manifest.PublishError), string(manifest.SourceKind), metaJSON, manifest.CreatedAt, manifest.UpdatedAt, manifest.PublishedAt)
	stored, err := scanManifestRow(row)
	if err != nil {
		return fmt.Errorf("inserting SBOM manifest: %w", err)
	}
	*manifest = *stored
	return nil
}

func createManifestPackages(ctx context.Context, db sbomQuerier, packages []domain.SBOMManifestPackage) error {
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
			`INSERT INTO sbom_manifest_packages (id, manifest_id, name, version, ecosystem, license, purl, cpe)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (manifest_id, name, version, COALESCE(ecosystem, ''), COALESCE(purl, ''), COALESCE(cpe, '')) DO NOTHING`,
			pkg.ID, pkg.ManifestID, pkg.Name, pkg.Version, nilIfEmpty(pkg.Ecosystem), nilIfEmpty(pkg.License), nilIfEmpty(pkg.PURL), nilIfEmpty(pkg.CPE))
	}
	br := db.SendBatch(ctx, batch)
	defer br.Close()
	for range packages {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("inserting SBOM manifest package: %w", err)
		}
	}
	return nil
}

func artifactSBOMFromManifest(manifest *domain.SBOMManifest, artifactID uuid.UUID) *domain.ArtifactSBOM {
	return &domain.ArtifactSBOM{
		ID:                 uuid.New(),
		ArtifactID:         artifactID,
		Format:             manifest.Format,
		SourceURL:          manifest.StorageURI,
		PackageCount:       manifest.PackageCount,
		VulnerabilityCount: manifest.VulnerabilityCount,
		CriticalCount:      manifest.CriticalCount,
		HighCount:          manifest.HighCount,
		RawHash:            manifest.PayloadSHA256,
		Metadata: map[string]any{
			"sbom_manifest_id": manifest.ID.String(),
			"subject_digest":   manifest.Subject.Digest,
			"source_kind":      string(manifest.SourceKind),
		},
	}
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

func (r *PgSBOMRepository) scanManifest(row pgx.Row) (*domain.SBOMManifest, error) {
	manifest, err := scanManifestRow(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return manifest, nil
}

func scanManifestRow(row pgx.Row) (*domain.SBOMManifest, error) {
	var m domain.SBOMManifest
	var subjectType, format, storageType, publishState, sourceKind string
	var subjectName, mediaType, generatorVersion, generatorPubkey, ntiaStatus *string
	var referenceEventID, referenceDTag, availabilityEventID, availabilityDTag, publishError *string
	var ntiaJSON, metaJSON []byte
	err := row.Scan(
		&m.ID, &subjectType, &m.Subject.ID, &subjectName, &m.Subject.Digest,
		&format, &mediaType, &storageType, &m.StorageURI, &m.PayloadSHA256,
		&m.Generator.ID, &generatorVersion, &generatorPubkey, &m.PackageCount,
		&m.VulnerabilityCount, &m.CriticalCount, &m.HighCount, &ntiaStatus, &ntiaJSON,
		&referenceEventID, &referenceDTag, &availabilityEventID, &availabilityDTag,
		&publishState, &publishError, &sourceKind, &metaJSON, &m.CreatedAt, &m.UpdatedAt, &m.PublishedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("scanning SBOM manifest: %w", err)
	}
	m.Subject.Type = domain.SBOMSubjectType(subjectType)
	m.Format = domain.SBOMFormat(format)
	m.StorageType = domain.SBOMStorageType(storageType)
	m.PublishState = domain.SBOMPublishState(publishState)
	m.SourceKind = domain.SBOMSourceKind(sourceKind)
	if subjectName != nil {
		m.Subject.DisplayName = *subjectName
	}
	if mediaType != nil {
		m.MediaType = *mediaType
	}
	if generatorVersion != nil {
		m.Generator.Version = *generatorVersion
	}
	if generatorPubkey != nil {
		m.Generator.Pubkey = *generatorPubkey
	}
	if ntiaStatus != nil {
		m.NTIAStatus = *ntiaStatus
	}
	if referenceEventID != nil {
		m.ReferenceEventID = *referenceEventID
	}
	if referenceDTag != nil {
		m.ReferenceDTag = *referenceDTag
	}
	if availabilityEventID != nil {
		m.AvailabilityEventID = *availabilityEventID
	}
	if availabilityDTag != nil {
		m.AvailabilityDTag = *availabilityDTag
	}
	if publishError != nil {
		m.PublishError = *publishError
	}
	if err := unmarshalJSON(ntiaJSON, &m.NTIA, "ntia metadata"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metaJSON, &m.Metadata, "metadata"); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *PgSBOMRepository) scanManifestPackages(rows pgx.Rows) ([]domain.SBOMManifestPackage, error) {
	var pkgs []domain.SBOMManifestPackage
	for rows.Next() {
		var p domain.SBOMManifestPackage
		var ecosystem, license, purl, cpe *string
		err := rows.Scan(&p.ID, &p.ManifestID, &p.Name, &p.Version, &ecosystem, &license, &purl, &cpe)
		if err != nil {
			return nil, fmt.Errorf("scanning SBOM manifest package: %w", err)
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
