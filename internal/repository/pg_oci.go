package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgOCIRepository is a PostgreSQL implementation for OCI registry and upload session persistence.
type ociDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type PgOCIRepository struct {
	pool ociDB
}

func NewPgOCIRepository(pool *pgxpool.Pool) *PgOCIRepository {
	return &PgOCIRepository{pool: pool}
}

func newPgOCIRepositoryForTests(pool ociDB) *PgOCIRepository {
	return &PgOCIRepository{pool: pool}
}

const ociRepoColumns = `id, name, namespace, description, created_at, updated_at`
const ociManifestColumns = `id, repository_id, digest, media_type, artifact_type, subject_digest, content, size_bytes, annotations, created_at, updated_at`
const ociBlobColumns = `id, digest, media_type, size_bytes, storage_ref, created_at, updated_at`
const ociUploadColumns = `upload_id, repository_id, spool_path, state, offset_bytes, digest, started_at, updated_at, expires_at`

func (r *PgOCIRepository) EnsureRepository(ctx context.Context, name string) (*domain.OCIRepository, error) {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO oci_repositories (name)
		VALUES ($1)
		ON CONFLICT (name) DO NOTHING
	`, name)
	if err != nil {
		return nil, fmt.Errorf("ensuring oci repository: %w", err)
	}
	return r.GetRepository(ctx, name)
}

func (r *PgOCIRepository) GetRepository(ctx context.Context, name string) (*domain.OCIRepository, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ociRepoColumns+` FROM oci_repositories WHERE name = $1`, name)
	repo := &domain.OCIRepository{}
	if err := row.Scan(&repo.ID, &repo.Name, &repo.Namespace, &repo.Description, &repo.CreatedAt, &repo.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying oci repository: %w", err)
	}
	return repo, nil
}

func (r *PgOCIRepository) GetManifestByDigest(ctx context.Context, repoName, digest string) (*domain.OCIManifest, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+ociManifestColumns+`
		FROM oci_manifests m
		JOIN oci_repositories r ON r.id = m.repository_id
		WHERE r.name = $1 AND m.digest = $2
	`, repoName, digest)
	return scanOCIManifest(row)
}

func (r *PgOCIRepository) GetManifestByTag(ctx context.Context, repoName, tag string) (*domain.OCIManifest, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+ociManifestColumns+`
		FROM oci_tags t
		JOIN oci_repositories r ON r.id = t.repository_id
		JOIN oci_manifests m ON m.id = t.manifest_id
		WHERE r.name = $1 AND t.tag = $2
	`, repoName, tag)
	return scanOCIManifest(row)
}

// GetManifest retrieves a manifest by digest or tag.
func (r *PgOCIRepository) GetManifest(ctx context.Context, repoName, reference string) (*domain.OCIManifest, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return r.GetManifestByDigest(ctx, repoName, reference)
	}
	return r.GetManifestByTag(ctx, repoName, reference)
}

func (r *PgOCIRepository) PutManifest(ctx context.Context, manifest domain.OCIManifest, tag string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting put manifest transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var manifestID string
	var annotationsJSON []byte
	if manifest.Annotations == nil {
		annotationsJSON = []byte(`{}`)
	} else {
		annotationsJSON, err = marshalJSON(manifest.Annotations, "oci manifest annotations")
		if err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO oci_manifests (repository_id, digest, media_type, artifact_type, subject_digest, content, size_bytes, annotations)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8)
		ON CONFLICT (repository_id, digest)
		DO UPDATE SET
			media_type = EXCLUDED.media_type,
			artifact_type = EXCLUDED.artifact_type,
			subject_digest = EXCLUDED.subject_digest,
			content = EXCLUDED.content,
			size_bytes = EXCLUDED.size_bytes,
			annotations = EXCLUDED.annotations,
			updated_at = now()
		RETURNING id
	`, manifest.RepositoryID, manifest.Digest, manifest.MediaType, manifest.ArtifactType, manifest.SubjectDigest, manifest.Content, manifest.SizeBytes, annotationsJSON).Scan(&manifestID)
	if err != nil {
		return fmt.Errorf("upserting oci manifest: %w", err)
	}

	if tag != "" {
		_, err = tx.Exec(ctx, `
			INSERT INTO oci_tags (repository_id, tag, manifest_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (repository_id, tag)
			DO UPDATE SET manifest_id = EXCLUDED.manifest_id, updated_at = now()
		`, manifest.RepositoryID, tag, manifestID)
		if err != nil {
			return fmt.Errorf("upserting oci tag: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing put manifest transaction: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) GetBlob(ctx context.Context, digest string) (*domain.OCIBlob, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ociBlobColumns+` FROM oci_blobs WHERE digest = $1`, digest)
	blob := &domain.OCIBlob{}
	if err := row.Scan(&blob.ID, &blob.Digest, &blob.MediaType, &blob.SizeBytes, &blob.StorageRef, &blob.CreatedAt, &blob.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying oci blob: %w", err)
	}
	return blob, nil
}

func (r *PgOCIRepository) BlobExistsInRepo(ctx context.Context, repoName, digest string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM oci_repo_blobs rb
			JOIN oci_repositories r ON r.id = rb.repository_id
			JOIN oci_blobs b ON b.id = rb.blob_id
			WHERE r.name = $1 AND b.digest = $2
		)
	`, repoName, digest).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking oci blob in repository: %w", err)
	}
	return exists, nil
}

func (r *PgOCIRepository) LinkBlobToRepo(ctx context.Context, repoName, digest string) error {
	repo, err := r.EnsureRepository(ctx, repoName)
	if err != nil {
		return err
	}
	blob, err := r.GetBlob(ctx, digest)
	if err != nil {
		return fmt.Errorf("get blob for linking: %w", err)
	}
	if blob == nil {
		return fmt.Errorf("blob %s not found", digest)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO oci_repo_blobs (repository_id, blob_id)
		VALUES ($1, $2)
		ON CONFLICT (repository_id, blob_id) DO NOTHING
	`, repo.ID, blob.ID)
	if err != nil {
		return fmt.Errorf("linking blob to repository: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) UpsertBlob(ctx context.Context, repoName, digest, mediaType, storageRef string, sizeBytes int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting upsert blob transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	repo, err := r.EnsureRepository(ctx, repoName)
	if err != nil {
		return err
	}

	var blobID string
	err = tx.QueryRow(ctx, `
		INSERT INTO oci_blobs (digest, media_type, size_bytes, storage_ref)
		VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''))
		ON CONFLICT (digest)
		DO UPDATE SET
			media_type = COALESCE(EXCLUDED.media_type, oci_blobs.media_type),
			size_bytes = EXCLUDED.size_bytes,
			storage_ref = COALESCE(EXCLUDED.storage_ref, oci_blobs.storage_ref),
			updated_at = now()
		RETURNING id
	`, digest, mediaType, sizeBytes, storageRef).Scan(&blobID)
	if err != nil {
		return fmt.Errorf("upserting oci blob: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO oci_repo_blobs (repository_id, blob_id)
		VALUES ($1, $2)
		ON CONFLICT (repository_id, blob_id) DO NOTHING
	`, repo.ID, blobID)
	if err != nil {
		return fmt.Errorf("linking blob to repository: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing upsert blob transaction: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) FinalizeBlob(ctx context.Context, upload domain.OCIBlobUpload) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("starting finalize blob transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var blobID string
	err = tx.QueryRow(ctx, `
		INSERT INTO oci_blobs (digest, media_type, size_bytes, storage_ref)
		VALUES ($1, NULLIF($2, ''), $3, NULLIF($4, ''))
		ON CONFLICT (digest)
		DO UPDATE SET
			media_type = COALESCE(EXCLUDED.media_type, oci_blobs.media_type),
			size_bytes = EXCLUDED.size_bytes,
			storage_ref = COALESCE(EXCLUDED.storage_ref, oci_blobs.storage_ref),
			updated_at = now()
		RETURNING id
	`, upload.Digest, "", upload.OffsetBytes, upload.SpoolPath).Scan(&blobID)
	if err != nil {
		return fmt.Errorf("upserting oci blob: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO oci_repo_blobs (repository_id, blob_id)
		VALUES ($1, $2)
		ON CONFLICT (repository_id, blob_id) DO NOTHING
	`, upload.RepositoryID, blobID)
	if err != nil {
		return fmt.Errorf("linking blob to repository: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE oci_blob_uploads
		SET state = $2, digest = NULLIF($3, ''), offset_bytes = $4, updated_at = now()
		WHERE upload_id = $1
	`, upload.UploadID, string(domain.OCIBlobUploadStateCompleted), upload.Digest, upload.OffsetBytes)
	if err != nil {
		return fmt.Errorf("marking upload finalized: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing finalize blob transaction: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) ListTags(ctx context.Context, repoName, lastTag string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.pool.Query(ctx, `
		SELECT t.tag
		FROM oci_tags t
		JOIN oci_repositories r ON r.id = t.repository_id
		WHERE r.name = $1 AND ($2 = '' OR t.tag > $2)
		ORDER BY t.tag ASC
		LIMIT $3
	`, repoName, lastTag, limit)
	if err != nil {
		return nil, fmt.Errorf("listing oci tags: %w", err)
	}
	defer rows.Close()

	tags := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("scanning oci tag: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

func (r *PgOCIRepository) ListReferrers(ctx context.Context, repoName, subjectDigest, artifactType string) ([]domain.OCIReferrerDescriptor, error) {
	query := `
		SELECT m.digest, m.media_type, COALESCE(m.artifact_type, ''), m.size_bytes, m.annotations
		FROM oci_manifests m
		JOIN oci_repositories r ON r.id = m.repository_id
		WHERE r.name = $1 AND m.subject_digest = $2
	`
	args := []any{repoName, subjectDigest}
	if strings.TrimSpace(artifactType) != "" {
		query += ` AND m.artifact_type = $3`
		args = append(args, artifactType)
	}
	query += ` ORDER BY m.created_at ASC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing oci referrers: %w", err)
	}
	defer rows.Close()

	referrers := make([]domain.OCIReferrerDescriptor, 0)
	for rows.Next() {
		var rd domain.OCIReferrerDescriptor
		var annotationsJSON []byte
		if err := rows.Scan(&rd.Digest, &rd.MediaType, &rd.ArtifactType, &rd.Size, &annotationsJSON); err != nil {
			return nil, fmt.Errorf("scanning oci referrer: %w", err)
		}
		if len(annotationsJSON) > 0 {
			if err := unmarshalJSON(annotationsJSON, &rd.Annotations, "oci referrer annotations"); err != nil {
				return nil, err
			}
		}
		referrers = append(referrers, rd)
	}
	return referrers, rows.Err()
}

func (r *PgOCIRepository) Create(ctx context.Context, uploadID, repoName, spoolPath string, expiresAt time.Time) error {
	repo, err := r.EnsureRepository(ctx, repoName)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO oci_blob_uploads (upload_id, repository_id, spool_path, state, offset_bytes, started_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, 0, now(), now(), $5)
	`, uploadID, repo.ID, spoolPath, string(domain.OCIBlobUploadStatePending), expiresAt)
	if err != nil {
		return fmt.Errorf("creating oci upload session: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) CreateUpload(ctx context.Context, upload domain.OCIBlobUpload) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO oci_blob_uploads (upload_id, repository_id, spool_path, state, offset_bytes, digest, started_at, updated_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9)
	`, upload.UploadID, upload.RepositoryID, upload.SpoolPath, string(upload.State), upload.OffsetBytes, upload.Digest, upload.StartedAt, upload.UpdatedAt, upload.ExpiresAt)
	if err != nil {
		return fmt.Errorf("creating oci upload session: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) Get(ctx context.Context, uploadID string) (repoName, spoolPath, state string, offsetBytes int64, expiresAt time.Time, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT r.name, u.spool_path, u.state, u.offset_bytes, u.expires_at
		FROM oci_blob_uploads u
		JOIN oci_repositories r ON r.id = u.repository_id
		WHERE u.upload_id = $1
	`, uploadID).Scan(&repoName, &spoolPath, &state, &offsetBytes, &expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", "", "", 0, time.Time{}, nil
		}
		return "", "", "", 0, time.Time{}, fmt.Errorf("querying oci upload session: %w", err)
	}
	return repoName, spoolPath, state, offsetBytes, expiresAt, nil
}

func (r *PgOCIRepository) GetUpload(ctx context.Context, uploadID string) (*domain.OCIBlobUpload, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ociUploadColumns+` FROM oci_blob_uploads WHERE upload_id = $1`, uploadID)
	upload := &domain.OCIBlobUpload{}
	var state string
	if err := row.Scan(&upload.UploadID, &upload.RepositoryID, &upload.SpoolPath, &state, &upload.OffsetBytes, &upload.Digest, &upload.StartedAt, &upload.UpdatedAt, &upload.ExpiresAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying oci upload session: %w", err)
	}
	upload.State = domain.OCIBlobUploadState(state)
	return upload, nil
}

func (r *PgOCIRepository) UpdateOffset(ctx context.Context, uploadID string, offsetBytes int64, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE oci_blob_uploads
		SET offset_bytes = $2, expires_at = $3, updated_at = now()
		WHERE upload_id = $1
	`, uploadID, offsetBytes, expiresAt)
	if err != nil {
		return fmt.Errorf("updating oci upload offset: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) UpdateUploadOffset(ctx context.Context, uploadID string, newOffset int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE oci_blob_uploads
		SET offset_bytes = $2, updated_at = now()
		WHERE upload_id = $1
	`, uploadID, newOffset)
	if err != nil {
		return fmt.Errorf("updating oci upload offset: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) UpdateState(ctx context.Context, uploadID, state string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE oci_blob_uploads
		SET state = $2, updated_at = now()
		WHERE upload_id = $1
	`, uploadID, state)
	if err != nil {
		return fmt.Errorf("updating oci upload state: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) MarkUploadFailed(ctx context.Context, uploadID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE oci_blob_uploads
		SET state = $2, updated_at = now()
		WHERE upload_id = $1
	`, uploadID, string(domain.OCIBlobUploadStateFailed))
	if err != nil {
		return fmt.Errorf("marking oci upload failed: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) ListExpiredUploads(ctx context.Context, olderThan time.Time) ([]domain.OCIBlobUpload, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+ociUploadColumns+`
		FROM oci_blob_uploads
		WHERE expires_at < $1
		ORDER BY expires_at ASC
	`, olderThan)
	if err != nil {
		return nil, fmt.Errorf("listing expired oci uploads: %w", err)
	}
	defer rows.Close()

	expired := make([]domain.OCIBlobUpload, 0)
	for rows.Next() {
		var upload domain.OCIBlobUpload
		var state string
		if err := rows.Scan(&upload.UploadID, &upload.RepositoryID, &upload.SpoolPath, &state, &upload.OffsetBytes, &upload.Digest, &upload.StartedAt, &upload.UpdatedAt, &upload.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scanning expired oci upload: %w", err)
		}
		upload.State = domain.OCIBlobUploadState(state)
		expired = append(expired, upload)
	}
	return expired, rows.Err()
}

func (r *PgOCIRepository) Delete(ctx context.Context, uploadID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM oci_blob_uploads WHERE upload_id = $1`, uploadID)
	if err != nil {
		return fmt.Errorf("deleting oci upload session: %w", err)
	}
	return nil
}

func (r *PgOCIRepository) DeleteUpload(ctx context.Context, uploadID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM oci_blob_uploads WHERE upload_id = $1`, uploadID)
	if err != nil {
		return fmt.Errorf("deleting oci upload session: %w", err)
	}
	return nil
}

func scanOCIManifest(row pgx.Row) (*domain.OCIManifest, error) {
	m := &domain.OCIManifest{}
	var annotationsJSON []byte
	if err := row.Scan(&m.ID, &m.RepositoryID, &m.Digest, &m.MediaType, &m.ArtifactType, &m.SubjectDigest, &m.Content, &m.SizeBytes, &annotationsJSON, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning oci manifest: %w", err)
	}
	if len(annotationsJSON) > 0 {
		if err := unmarshalJSON(annotationsJSON, &m.Annotations, "oci manifest annotations"); err != nil {
			return nil, err
		}
	}
	return m, nil
}
