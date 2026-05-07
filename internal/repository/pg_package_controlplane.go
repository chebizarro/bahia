package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgPackageControlPlaneRepository stores package read models derived from Nostr events.
// Tables written by this repository are projections/caches only; Nostr events remain
// the source of truth for desired package control-plane state.
type PgPackageControlPlaneRepository struct {
	pool pgQueryer
}

func NewPgPackageControlPlaneRepository(pool *pgxpool.Pool) *PgPackageControlPlaneRepository {
	return newPgPackageControlPlaneRepositoryWithDB(pool)
}

func newPgPackageControlPlaneRepositoryWithDB(db pgQueryer) *PgPackageControlPlaneRepository {
	return &PgPackageControlPlaneRepository{pool: db}
}

const packageRepositoryColumns = `id, name, format, backend_ref, backend_type, external_repository_name, description, namespace_prefix, policy, metadata, public_url, status, last_error, deleted, created_at, updated_at, last_event_id, last_event_created_at`
const packageArtifactColumns = `id, repository_id, repository_name, format, namespace, package_name, version, filename, source_url, sha256, size_bytes, content_type, metadata, download_url, backend_path, status, last_error, deleted, created_at, updated_at, last_event_id, last_event_created_at`
const packagePublicationColumns = `id, repository_id, artifact_id, environment, channel, target_repository_id, status, policy_decision, policy_ref, approved_by, approved_at, published_at, promoted_at, last_error, metadata, created_at, updated_at, last_event_id, last_event_created_at`
const packageIntentColumns = `id, request_event_id, operation, repository_id, repository_name, artifact_id, namespace, package_name, version, filename, requester_pubkey, request_payload, result_payload, status, error_message, created_at, updated_at, completed_at, last_status_event_id, last_result_event_id`

func (r *PgPackageControlPlaneRepository) UpsertRepository(ctx context.Context, repo *domain.PackageRepository) error {
	if repo.ID == uuid.Nil {
		repo.ID = uuid.New()
	}
	setProjectionTimes(&repo.CreatedAt, &repo.UpdatedAt, &repo.LastEventCreatedAt)
	policyJSON, err := marshalJSON(repo.Policy, "package repository policy")
	if err != nil {
		return err
	}
	metadataJSON, err := marshalJSON(repo.Metadata, "package repository metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO package_repositories_projection (`+packageRepositoryColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10, '{}'::jsonb), $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			format = EXCLUDED.format,
			backend_ref = EXCLUDED.backend_ref,
			backend_type = EXCLUDED.backend_type,
			external_repository_name = EXCLUDED.external_repository_name,
			description = EXCLUDED.description,
			namespace_prefix = EXCLUDED.namespace_prefix,
			policy = EXCLUDED.policy,
			metadata = EXCLUDED.metadata,
			public_url = EXCLUDED.public_url,
			status = EXCLUDED.status,
			last_error = EXCLUDED.last_error,
			deleted = EXCLUDED.deleted,
			updated_at = EXCLUDED.updated_at,
			last_event_id = EXCLUDED.last_event_id,
			last_event_created_at = EXCLUDED.last_event_created_at
		WHERE EXCLUDED.last_event_created_at > package_repositories_projection.last_event_created_at
		   OR (EXCLUDED.last_event_created_at = package_repositories_projection.last_event_created_at
		       AND EXCLUDED.last_event_id > package_repositories_projection.last_event_id)
	`, repo.ID, repo.Name, repo.Format, repo.BackendRef, repo.BackendType, repo.ExternalRepositoryName,
		repo.Description, repo.NamespacePrefix, policyJSON, metadataJSON, repo.PublicURL, repo.Status,
		repo.LastError, repo.Deleted, repo.CreatedAt, repo.UpdatedAt, repo.LastEventID, repo.LastEventCreatedAt)
	if err != nil {
		return fmt.Errorf("upserting package repository projection: %w", err)
	}
	return nil
}

func (r *PgPackageControlPlaneRepository) GetRepository(ctx context.Context, id uuid.UUID) (*domain.PackageRepository, error) {
	return r.scanRepository(r.pool.QueryRow(ctx, `SELECT `+packageRepositoryColumns+` FROM package_repositories_projection WHERE id = $1`, id))
}

func (r *PgPackageControlPlaneRepository) GetRepositoryByName(ctx context.Context, name string) (*domain.PackageRepository, error) {
	return r.scanRepository(r.pool.QueryRow(ctx, `SELECT `+packageRepositoryColumns+` FROM package_repositories_projection WHERE name = $1`, name))
}

func (r *PgPackageControlPlaneRepository) ListRepositories(ctx context.Context, includeDeleted bool) ([]domain.PackageRepository, error) {
	query := `SELECT ` + packageRepositoryColumns + ` FROM package_repositories_projection`
	args := []any{}
	if !includeDeleted {
		query += ` WHERE deleted = false`
	}
	query += ` ORDER BY name ASC`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing package repository projections: %w", err)
	}
	defer rows.Close()
	return scanPackageRepositoryRows(rows)
}

func (r *PgPackageControlPlaneRepository) UpsertArtifact(ctx context.Context, artifact *domain.PackageArtifact) error {
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	setProjectionTimes(&artifact.CreatedAt, &artifact.UpdatedAt, &artifact.LastEventCreatedAt)
	metadataJSON, err := marshalJSON(artifact.Metadata, "package artifact metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO package_artifacts_projection (`+packageArtifactColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, COALESCE($13, '{}'::jsonb), $14, $15, $16, $17, $18, $19, $20, $21, $22)
		ON CONFLICT (repository_id, namespace, package_name, version, filename) DO UPDATE SET
			repository_name = EXCLUDED.repository_name,
			format = EXCLUDED.format,
			source_url = EXCLUDED.source_url,
			sha256 = EXCLUDED.sha256,
			size_bytes = EXCLUDED.size_bytes,
			content_type = EXCLUDED.content_type,
			metadata = EXCLUDED.metadata,
			download_url = EXCLUDED.download_url,
			backend_path = EXCLUDED.backend_path,
			status = EXCLUDED.status,
			last_error = EXCLUDED.last_error,
			deleted = EXCLUDED.deleted,
			updated_at = EXCLUDED.updated_at,
			last_event_id = EXCLUDED.last_event_id,
			last_event_created_at = EXCLUDED.last_event_created_at
		WHERE EXCLUDED.last_event_created_at > package_artifacts_projection.last_event_created_at
		   OR (EXCLUDED.last_event_created_at = package_artifacts_projection.last_event_created_at
		       AND EXCLUDED.last_event_id > package_artifacts_projection.last_event_id)
	`, artifact.ID, artifact.RepositoryID, artifact.RepositoryName, artifact.Format, artifact.Namespace,
		artifact.PackageName, artifact.Version, artifact.Filename, artifact.SourceURL, artifact.SHA256,
		artifact.SizeBytes, artifact.ContentType, metadataJSON, artifact.DownloadURL, artifact.BackendPath,
		artifact.Status, artifact.LastError, artifact.Deleted, artifact.CreatedAt, artifact.UpdatedAt,
		artifact.LastEventID, artifact.LastEventCreatedAt)
	if err != nil {
		return fmt.Errorf("upserting package artifact projection: %w", err)
	}
	return nil
}

func (r *PgPackageControlPlaneRepository) GetArtifact(ctx context.Context, repositoryID uuid.UUID, namespace, packageName, version, filename string) (*domain.PackageArtifact, error) {
	return r.scanArtifact(r.pool.QueryRow(ctx, `SELECT `+packageArtifactColumns+` FROM package_artifacts_projection WHERE repository_id = $1 AND namespace = $2 AND package_name = $3 AND version = $4 AND filename = $5`, repositoryID, namespace, packageName, version, filename))
}

func (r *PgPackageControlPlaneRepository) ListArtifacts(ctx context.Context, repositoryID uuid.UUID, limit, offset int) ([]domain.PackageArtifact, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+packageArtifactColumns+`
		FROM package_artifacts_projection
		WHERE repository_id = $1 AND deleted = false
		ORDER BY package_name ASC, version ASC, filename ASC
		LIMIT $2 OFFSET $3
	`, repositoryID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing package artifact projections: %w", err)
	}
	defer rows.Close()
	return scanPackageArtifactRows(rows)
}

func (r *PgPackageControlPlaneRepository) UpsertPublication(ctx context.Context, publication *domain.PackagePublication) error {
	if publication.ID == uuid.Nil {
		publication.ID = uuid.New()
	}
	setProjectionTimes(&publication.CreatedAt, &publication.UpdatedAt, &publication.LastEventCreatedAt)
	metadataJSON, err := marshalJSON(publication.Metadata, "package publication metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO package_publications_projection (`+packagePublicationColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, COALESCE($15, '{}'::jsonb), $16, $17, $18, $19)
		ON CONFLICT (id) DO UPDATE SET
			repository_id = EXCLUDED.repository_id,
			artifact_id = EXCLUDED.artifact_id,
			environment = EXCLUDED.environment,
			channel = EXCLUDED.channel,
			target_repository_id = EXCLUDED.target_repository_id,
			status = EXCLUDED.status,
			policy_decision = EXCLUDED.policy_decision,
			policy_ref = EXCLUDED.policy_ref,
			approved_by = EXCLUDED.approved_by,
			approved_at = EXCLUDED.approved_at,
			published_at = EXCLUDED.published_at,
			promoted_at = EXCLUDED.promoted_at,
			last_error = EXCLUDED.last_error,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at,
			last_event_id = EXCLUDED.last_event_id,
			last_event_created_at = EXCLUDED.last_event_created_at
		WHERE EXCLUDED.last_event_created_at > package_publications_projection.last_event_created_at
		   OR (EXCLUDED.last_event_created_at = package_publications_projection.last_event_created_at
		       AND EXCLUDED.last_event_id > package_publications_projection.last_event_id)
	`, publication.ID, publication.RepositoryID, publication.ArtifactID, publication.Environment, publication.Channel,
		uuidPtrArg(publication.TargetRepositoryID), publication.Status, publication.PolicyDecision, publication.PolicyRef,
		publication.ApprovedBy, publication.ApprovedAt, publication.PublishedAt, publication.PromotedAt,
		publication.LastError, metadataJSON, publication.CreatedAt, publication.UpdatedAt,
		publication.LastEventID, publication.LastEventCreatedAt)
	if err != nil {
		return fmt.Errorf("upserting package publication projection: %w", err)
	}
	return nil
}

func (r *PgPackageControlPlaneRepository) GetPublication(ctx context.Context, id uuid.UUID) (*domain.PackagePublication, error) {
	return r.scanPublication(r.pool.QueryRow(ctx, `SELECT `+packagePublicationColumns+` FROM package_publications_projection WHERE id = $1`, id))
}

func (r *PgPackageControlPlaneRepository) ListPublicationsByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.PackagePublication, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+packagePublicationColumns+` FROM package_publications_projection WHERE artifact_id = $1 ORDER BY updated_at DESC`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("listing package publications by artifact: %w", err)
	}
	defer rows.Close()
	return scanPackagePublicationRows(rows)
}

func (r *PgPackageControlPlaneRepository) ListPublicationsByRepository(ctx context.Context, repositoryID uuid.UUID, includeTerminal bool) ([]domain.PackagePublication, error) {
	query := `SELECT ` + packagePublicationColumns + ` FROM package_publications_projection WHERE repository_id = $1`
	args := []any{repositoryID}
	if !includeTerminal {
		query += ` AND status NOT IN ('succeeded', 'promoted', 'rejected', 'rolled_back', 'failed')`
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing package publications by repository: %w", err)
	}
	defer rows.Close()
	return scanPackagePublicationRows(rows)
}

func (r *PgPackageControlPlaneRepository) UpsertIntent(ctx context.Context, intent *domain.PackageIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	now := time.Now().UTC()
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = now
	}
	if intent.UpdatedAt.IsZero() {
		intent.UpdatedAt = now
	}
	var requestJSON any
	if intent.RequestPayload != nil {
		b, err := marshalJSON(intent.RequestPayload, "package intent request payload")
		if err != nil {
			return err
		}
		requestJSON = b
	}
	var resultJSON any
	if intent.ResultPayload != nil {
		b, err := marshalJSON(intent.ResultPayload, "package intent result payload")
		if err != nil {
			return err
		}
		resultJSON = b
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO package_intents_projection (`+packageIntentColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12, '{}'::jsonb), COALESCE($13, '{}'::jsonb), $14, $15, $16, $17, $18, $19, $20)
		ON CONFLICT (request_event_id) DO UPDATE SET
			operation = EXCLUDED.operation,
			repository_id = COALESCE(EXCLUDED.repository_id, package_intents_projection.repository_id),
			repository_name = COALESCE(NULLIF(EXCLUDED.repository_name, ''), package_intents_projection.repository_name),
			artifact_id = COALESCE(EXCLUDED.artifact_id, package_intents_projection.artifact_id),
			namespace = COALESCE(NULLIF(EXCLUDED.namespace, ''), package_intents_projection.namespace),
			package_name = COALESCE(NULLIF(EXCLUDED.package_name, ''), package_intents_projection.package_name),
			version = COALESCE(NULLIF(EXCLUDED.version, ''), package_intents_projection.version),
			filename = COALESCE(NULLIF(EXCLUDED.filename, ''), package_intents_projection.filename),
			requester_pubkey = COALESCE(NULLIF(EXCLUDED.requester_pubkey, ''), package_intents_projection.requester_pubkey),
			request_payload = CASE WHEN EXCLUDED.request_payload = '{}'::jsonb THEN package_intents_projection.request_payload ELSE EXCLUDED.request_payload END,
			result_payload = CASE WHEN EXCLUDED.result_payload = '{}'::jsonb THEN package_intents_projection.result_payload ELSE EXCLUDED.result_payload END,
			status = CASE
				WHEN package_intents_projection.status IN ('succeeded', 'failed', 'superseded')
				 AND EXCLUDED.status NOT IN ('succeeded', 'failed', 'superseded')
				THEN package_intents_projection.status
				ELSE EXCLUDED.status
			END,
			error_message = CASE
				WHEN package_intents_projection.status IN ('succeeded', 'failed', 'superseded')
				 AND EXCLUDED.status NOT IN ('succeeded', 'failed', 'superseded')
				THEN package_intents_projection.error_message
				ELSE EXCLUDED.error_message
			END,
			updated_at = EXCLUDED.updated_at,
			completed_at = COALESCE(EXCLUDED.completed_at, package_intents_projection.completed_at),
			last_status_event_id = COALESCE(NULLIF(EXCLUDED.last_status_event_id, ''), package_intents_projection.last_status_event_id),
			last_result_event_id = COALESCE(NULLIF(EXCLUDED.last_result_event_id, ''), package_intents_projection.last_result_event_id)
	`, intent.ID, intent.RequestEventID, intent.Operation, uuidPtrArg(intent.RepositoryID), intent.RepositoryName,
		uuidPtrArg(intent.ArtifactID), intent.Namespace, intent.PackageName, intent.Version, intent.Filename,
		intent.RequesterPubkey, requestJSON, resultJSON, intent.Status, intent.ErrorMessage, intent.CreatedAt,
		intent.UpdatedAt, intent.CompletedAt, intent.LastStatusEventID, intent.LastResultEventID)
	if err != nil {
		return fmt.Errorf("upserting package intent projection: %w", err)
	}
	return nil
}

func (r *PgPackageControlPlaneRepository) GetIntent(ctx context.Context, id uuid.UUID) (*domain.PackageIntent, error) {
	return r.scanIntent(r.pool.QueryRow(ctx, `SELECT `+packageIntentColumns+` FROM package_intents_projection WHERE id = $1`, id))
}

func (r *PgPackageControlPlaneRepository) GetIntentByRequestEventID(ctx context.Context, requestEventID string) (*domain.PackageIntent, error) {
	return r.scanIntent(r.pool.QueryRow(ctx, `SELECT `+packageIntentColumns+` FROM package_intents_projection WHERE request_event_id = $1`, requestEventID))
}

func (r *PgPackageControlPlaneRepository) ListNonTerminalIntents(ctx context.Context, limit int) ([]domain.PackageIntent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+packageIntentColumns+`
		FROM package_intents_projection
		WHERE status NOT IN ('succeeded', 'failed', 'superseded')
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing non-terminal package intents: %w", err)
	}
	defer rows.Close()
	var intents []domain.PackageIntent
	for rows.Next() {
		intent, err := scanPackageIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning package intent: %w", err)
		}
		intents = append(intents, *intent)
	}
	return intents, rows.Err()
}

func (r *PgPackageControlPlaneRepository) scanRepository(row pgx.Row) (*domain.PackageRepository, error) {
	repo, err := scanPackageRepository(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning package repository projection: %w", err)
	}
	return repo, nil
}

func (r *PgPackageControlPlaneRepository) scanArtifact(row pgx.Row) (*domain.PackageArtifact, error) {
	artifact, err := scanPackageArtifact(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning package artifact projection: %w", err)
	}
	return artifact, nil
}

func (r *PgPackageControlPlaneRepository) scanPublication(row pgx.Row) (*domain.PackagePublication, error) {
	publication, err := scanPackagePublication(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning package publication projection: %w", err)
	}
	return publication, nil
}

func (r *PgPackageControlPlaneRepository) scanIntent(row pgx.Row) (*domain.PackageIntent, error) {
	intent, err := scanPackageIntent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning package intent projection: %w", err)
	}
	return intent, nil
}

func scanPackageRepository(row pgx.Row) (*domain.PackageRepository, error) {
	repo := &domain.PackageRepository{}
	var policyJSON, metadataJSON []byte
	var format, backendType, status string
	if err := row.Scan(&repo.ID, &repo.Name, &format, &repo.BackendRef, &backendType, &repo.ExternalRepositoryName,
		&repo.Description, &repo.NamespacePrefix, &policyJSON, &metadataJSON, &repo.PublicURL, &status,
		&repo.LastError, &repo.Deleted, &repo.CreatedAt, &repo.UpdatedAt, &repo.LastEventID, &repo.LastEventCreatedAt); err != nil {
		return nil, err
	}
	repo.Format = domain.PackageRepositoryFormat(format)
	repo.BackendType = domain.PackageBackendType(backendType)
	repo.Status = domain.PackageRepositoryStatus(status)
	if err := unmarshalJSON(policyJSON, &repo.Policy, "package repository policy"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &repo.Metadata, "package repository metadata"); err != nil {
		return nil, err
	}
	return repo, nil
}

func scanPackageRepositoryRows(rows pgx.Rows) ([]domain.PackageRepository, error) {
	repos := make([]domain.PackageRepository, 0)
	for rows.Next() {
		repo, err := scanPackageRepository(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning package repository row: %w", err)
		}
		repos = append(repos, *repo)
	}
	return repos, rows.Err()
}

func scanPackageArtifact(row pgx.Row) (*domain.PackageArtifact, error) {
	artifact := &domain.PackageArtifact{}
	var metadataJSON []byte
	var format, status string
	if err := row.Scan(&artifact.ID, &artifact.RepositoryID, &artifact.RepositoryName, &format, &artifact.Namespace,
		&artifact.PackageName, &artifact.Version, &artifact.Filename, &artifact.SourceURL, &artifact.SHA256,
		&artifact.SizeBytes, &artifact.ContentType, &metadataJSON, &artifact.DownloadURL, &artifact.BackendPath,
		&status, &artifact.LastError, &artifact.Deleted, &artifact.CreatedAt, &artifact.UpdatedAt,
		&artifact.LastEventID, &artifact.LastEventCreatedAt); err != nil {
		return nil, err
	}
	artifact.Format = domain.PackageRepositoryFormat(format)
	artifact.Status = domain.PackageArtifactStatus(status)
	if err := unmarshalJSON(metadataJSON, &artifact.Metadata, "package artifact metadata"); err != nil {
		return nil, err
	}
	return artifact, nil
}

func scanPackageArtifactRows(rows pgx.Rows) ([]domain.PackageArtifact, error) {
	artifacts := make([]domain.PackageArtifact, 0)
	for rows.Next() {
		artifact, err := scanPackageArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning package artifact row: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}
	return artifacts, rows.Err()
}

func scanPackagePublication(row pgx.Row) (*domain.PackagePublication, error) {
	publication := &domain.PackagePublication{}
	var metadataJSON []byte
	var targetRepo pgtype.UUID
	var status, policyDecision string
	if err := row.Scan(&publication.ID, &publication.RepositoryID, &publication.ArtifactID, &publication.Environment,
		&publication.Channel, &targetRepo, &status, &policyDecision, &publication.PolicyRef, &publication.ApprovedBy,
		&publication.ApprovedAt, &publication.PublishedAt, &publication.PromotedAt, &publication.LastError,
		&metadataJSON, &publication.CreatedAt, &publication.UpdatedAt, &publication.LastEventID,
		&publication.LastEventCreatedAt); err != nil {
		return nil, err
	}
	publication.TargetRepositoryID = uuidPtrFromPG(targetRepo)
	publication.Status = domain.PackagePublicationStatus(status)
	publication.PolicyDecision = domain.PackagePolicyDecision(policyDecision)
	if err := unmarshalJSON(metadataJSON, &publication.Metadata, "package publication metadata"); err != nil {
		return nil, err
	}
	return publication, nil
}

func scanPackagePublicationRows(rows pgx.Rows) ([]domain.PackagePublication, error) {
	publications := make([]domain.PackagePublication, 0)
	for rows.Next() {
		publication, err := scanPackagePublication(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning package publication row: %w", err)
		}
		publications = append(publications, *publication)
	}
	return publications, rows.Err()
}

func scanPackageIntent(row pgx.Row) (*domain.PackageIntent, error) {
	intent := &domain.PackageIntent{}
	var repositoryID, artifactID pgtype.UUID
	var operation, status string
	var requestJSON, resultJSON []byte
	if err := row.Scan(&intent.ID, &intent.RequestEventID, &operation, &repositoryID, &intent.RepositoryName,
		&artifactID, &intent.Namespace, &intent.PackageName, &intent.Version, &intent.Filename,
		&intent.RequesterPubkey, &requestJSON, &resultJSON, &status, &intent.ErrorMessage,
		&intent.CreatedAt, &intent.UpdatedAt, &intent.CompletedAt, &intent.LastStatusEventID,
		&intent.LastResultEventID); err != nil {
		return nil, err
	}
	intent.RepositoryID = uuidPtrFromPG(repositoryID)
	intent.ArtifactID = uuidPtrFromPG(artifactID)
	intent.Operation = domain.PackageOperation(operation)
	intent.Status = domain.PackageIntentStatus(status)
	if err := unmarshalJSON(requestJSON, &intent.RequestPayload, "package intent request payload"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(resultJSON, &intent.ResultPayload, "package intent result payload"); err != nil {
		return nil, err
	}
	return intent, nil
}

func setProjectionTimes(createdAt, updatedAt, eventCreatedAt *time.Time) {
	now := time.Now().UTC()
	if createdAt.IsZero() {
		*createdAt = now
	}
	if updatedAt.IsZero() {
		*updatedAt = now
	}
	if eventCreatedAt.IsZero() {
		*eventCreatedAt = time.Unix(0, 0).UTC()
	}
}

func uuidPtrArg(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}

func uuidPtrFromPG(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := uuid.UUID(value.Bytes)
	return &id
}
