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

// PgSecretRepository is a PostgreSQL implementation of SecretRepository.
type PgSecretRepository struct {
	pool pgQueryer
}

// NewPgSecretRepository creates a new PgSecretRepository.
func NewPgSecretRepository(pool *pgxpool.Pool) *PgSecretRepository {
	return newPgSecretRepositoryWithDB(pool)
}

func newPgSecretRepositoryWithDB(db pgQueryer) *PgSecretRepository {
	return &PgSecretRepository{pool: db}
}

func (r *PgSecretRepository) Create(ctx context.Context, s *domain.ServiceSecret) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.Version == 0 {
		s.Version = 1
	}

	_, err := r.pool.Exec(ctx, `
		WITH inserted AS (
			INSERT INTO service_secrets (id, service_id, environment_id, name, encrypted_value,
				encryption_method, version, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
			RETURNING id, version, encrypted_value, encryption_method, created_by, created_at
		)
		INSERT INTO secret_versions (secret_id, version, encrypted_value, encryption_method, created_by, created_at)
		SELECT id, version, encrypted_value, encryption_method, created_by, created_at FROM inserted
	`, s.ID, s.ServiceID, s.EnvironmentID, s.Name, s.EncryptedValue,
		string(s.EncryptionMethod), s.Version, s.CreatedBy)
	if err != nil {
		return fmt.Errorf("creating secret: %w", err)
	}
	return nil
}

func (r *PgSecretRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	s := &domain.ServiceSecret{}
	var method string
	err := r.pool.QueryRow(ctx, `
		SELECT id, service_id, environment_id, name, encrypted_value,
			encryption_method, version, created_by, created_at, updated_at
		FROM service_secrets WHERE id = $1
	`, id).Scan(&s.ID, &s.ServiceID, &s.EnvironmentID, &s.Name, &s.EncryptedValue,
		&method, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting secret: %w", err)
	}
	s.EncryptionMethod = domain.EncryptionMethod(method)
	return s, nil
}

func (r *PgSecretRepository) GetCurrentVersion(ctx context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	v := &domain.SecretVersion{}
	var method string
	err := r.pool.QueryRow(ctx, `
		SELECT id, secret_id, version, encrypted_value, encryption_method, created_by, created_at
		FROM secret_versions
		WHERE secret_id = $1
		ORDER BY version DESC
		LIMIT 1
	`, secretID).Scan(&v.ID, &v.SecretID, &v.Version, &v.EncryptedValue, &method, &v.CreatedBy, &v.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting current secret version: %w", err)
	}
	v.EncryptionMethod = domain.EncryptionMethod(method)
	return v, nil
}

// ListVersions returns every retained value for a secret so security-sensitive
// consumers such as stored log redaction remain safe after secret rotation.
func (r *PgSecretRepository) ListVersions(ctx context.Context, secretID uuid.UUID) ([]domain.SecretVersion, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, secret_id, version, encrypted_value, encryption_method, created_by, created_at
		FROM secret_versions
		WHERE secret_id = $1
		ORDER BY version DESC
	`, secretID)
	if err != nil {
		return nil, fmt.Errorf("listing secret versions: %w", err)
	}
	defer rows.Close()
	versions := make([]domain.SecretVersion, 0)
	for rows.Next() {
		var version domain.SecretVersion
		var method string
		if err := rows.Scan(&version.ID, &version.SecretID, &version.Version, &version.EncryptedValue, &method, &version.CreatedBy, &version.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning secret version: %w", err)
		}
		version.EncryptionMethod = domain.EncryptionMethod(method)
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating secret versions: %w", err)
	}
	return versions, nil
}

func (r *PgSecretRepository) ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, service_id, environment_id, name, encrypted_value,
			encryption_method, version, created_by, created_at, updated_at
		FROM service_secrets WHERE service_id = $1
		ORDER BY name, environment_id NULLS FIRST
	`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	defer rows.Close()
	return scanSecrets(rows)
}

func (r *PgSecretRepository) ListByServiceAndEnv(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, service_id, environment_id, name, encrypted_value,
			encryption_method, version, created_by, created_at, updated_at
		FROM service_secrets
		WHERE service_id = $1 AND environment_id = $2
		ORDER BY name
	`, serviceID, envID)
	if err != nil {
		return nil, fmt.Errorf("listing secrets by env: %w", err)
	}
	defer rows.Close()
	return scanSecrets(rows)
}

// ListEffective returns the merged set of secrets for a service+environment:
// environment-specific secrets override service-wide ones with the same name.
func (r *PgSecretRepository) ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (name) id, service_id, environment_id, name, encrypted_value,
			encryption_method, version, created_by, created_at, updated_at
		FROM service_secrets
		WHERE service_id = $1 AND (environment_id IS NULL OR environment_id = $2)
		ORDER BY name, environment_id DESC NULLS LAST
	`, serviceID, envID)
	if err != nil {
		return nil, fmt.Errorf("listing effective secrets: %w", err)
	}
	defer rows.Close()
	return scanSecrets(rows)
}

func (r *PgSecretRepository) Update(ctx context.Context, s *domain.ServiceSecret) error {
	if s == nil {
		return fmt.Errorf("updating secret: secret is nil")
	}
	if s.Version <= 0 {
		return fmt.Errorf("updating secret %s: expected version must be positive", s.ID)
	}

	var exists bool
	var updatedVersion int
	err := r.pool.QueryRow(ctx, `
		WITH target AS (
			SELECT 1 FROM service_secrets WHERE id = $3
		), updated AS (
			UPDATE service_secrets
			SET encrypted_value = $1, encryption_method = $2, version = version + 1, updated_at = now()
			WHERE id = $3 AND version = $4
			RETURNING id, version, encrypted_value, encryption_method, created_by, updated_at
		), history AS (
			INSERT INTO secret_versions (secret_id, version, encrypted_value, encryption_method, created_by, created_at)
			SELECT id, version, encrypted_value, encryption_method, created_by, updated_at FROM updated
			RETURNING version
		)
		SELECT EXISTS(SELECT 1 FROM target), COALESCE((SELECT version FROM history), 0)
	`, s.EncryptedValue, string(s.EncryptionMethod), s.ID, s.Version).Scan(&exists, &updatedVersion)
	if err != nil {
		return fmt.Errorf("updating secret: %w", err)
	}
	if !exists {
		return fmt.Errorf("updating secret %s: %w", s.ID, ErrNotFound)
	}
	if updatedVersion == 0 {
		return fmt.Errorf("updating secret %s at version %d: %w", s.ID, s.Version, ErrConflict)
	}
	s.Version = updatedVersion
	return nil
}

func (r *PgSecretRepository) RecordSecretAccessAudit(ctx context.Context, audit *domain.SecretAccessAudit) error {
	if audit == nil {
		return fmt.Errorf("secret access audit is nil")
	}
	if audit.ID == uuid.Nil {
		audit.ID = uuid.New()
	}
	if audit.AccessedAt.IsZero() {
		audit.AccessedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO secret_access_audit (id, secret_id, secret_version_id, version, service_id,
			environment_id, operation, outcome, actor, reason, request_id, error, accessed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, audit.ID, audit.SecretID, audit.VersionID, audit.Version, audit.ServiceID,
		audit.EnvironmentID, string(audit.Operation), string(audit.Outcome), audit.Actor,
		audit.Reason, audit.RequestID, audit.Error, audit.AccessedAt)
	if err != nil {
		return fmt.Errorf("recording secret access audit: %w", err)
	}
	return nil
}

func (r *PgSecretRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM service_secrets WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting secret: %w", err)
	}
	return nil
}

func (r *PgSecretRepository) DeleteByName(ctx context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error {
	var err error
	if envID != nil {
		_, err = r.pool.Exec(ctx, `
			DELETE FROM service_secrets WHERE service_id = $1 AND environment_id = $2 AND name = $3
		`, serviceID, envID, name)
	} else {
		_, err = r.pool.Exec(ctx, `
			DELETE FROM service_secrets WHERE service_id = $1 AND environment_id IS NULL AND name = $2
		`, serviceID, name)
	}
	if err != nil {
		return fmt.Errorf("deleting secret by name: %w", err)
	}
	return nil
}

func scanSecrets(rows pgx.Rows) ([]domain.ServiceSecret, error) {
	var secrets []domain.ServiceSecret
	for rows.Next() {
		var s domain.ServiceSecret
		var method string
		if err := rows.Scan(&s.ID, &s.ServiceID, &s.EnvironmentID, &s.Name, &s.EncryptedValue,
			&method, &s.Version, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning secret: %w", err)
		}
		s.EncryptionMethod = domain.EncryptionMethod(method)
		secrets = append(secrets, s)
	}
	return secrets, rows.Err()
}
