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

// PgAdoptedRuntimeIdentityRepository persists adoption workload fingerprints.
type PgAdoptedRuntimeIdentityRepository struct {
	pool pgQueryer
}

// NewPgAdoptedRuntimeIdentityRepository creates a PostgreSQL adopted identity repository.
func NewPgAdoptedRuntimeIdentityRepository(pool *pgxpool.Pool) *PgAdoptedRuntimeIdentityRepository {
	return newPgAdoptedRuntimeIdentityRepositoryWithDB(pool)
}

func newPgAdoptedRuntimeIdentityRepositoryWithDB(db pgQueryer) *PgAdoptedRuntimeIdentityRepository {
	return &PgAdoptedRuntimeIdentityRepository{pool: db}
}

func (r *PgAdoptedRuntimeIdentityRepository) UpsertMany(ctx context.Context, identities []domain.AdoptedRuntimeIdentity) error {
	for i := range identities {
		identity := identities[i]
		if identity.ID == uuid.Nil {
			identity.ID = uuid.New()
		}
		now := time.Now().UTC()
		if identity.CreatedAt.IsZero() {
			identity.CreatedAt = now
		}
		identity.UpdatedAt = now
		composeJSON, err := marshalJSON(identity.Compose, "adopted runtime identity compose")
		if err != nil {
			return err
		}
		_, err = r.pool.Exec(ctx, `
			INSERT INTO adopted_runtime_identity (
				id, org_id, service_id, environment_id, fingerprint_kind, fingerprint,
				container_id, image_digest, endpoint_ref, host_alias, target_name, compose, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT (fingerprint) DO UPDATE SET
				org_id = EXCLUDED.org_id,
				service_id = EXCLUDED.service_id,
				environment_id = EXCLUDED.environment_id,
				fingerprint_kind = EXCLUDED.fingerprint_kind,
				container_id = EXCLUDED.container_id,
				image_digest = EXCLUDED.image_digest,
				endpoint_ref = EXCLUDED.endpoint_ref,
				host_alias = EXCLUDED.host_alias,
				target_name = EXCLUDED.target_name,
				compose = EXCLUDED.compose,
				updated_at = EXCLUDED.updated_at
		`, identity.ID, identity.OrgID, identity.ServiceID, identity.EnvironmentID, identity.FingerprintKind, identity.Fingerprint,
			identity.ContainerID, identity.ImageDigest, identity.EndpointRef, identity.HostAlias, identity.TargetName, composeJSON, identity.CreatedAt, identity.UpdatedAt)
		if err != nil {
			return fmt.Errorf("upserting adopted runtime identity %q: %w", identity.Fingerprint, err)
		}
		identities[i] = identity
	}
	return nil
}

func (r *PgAdoptedRuntimeIdentityRepository) FindByFingerprints(ctx context.Context, fingerprints []string) ([]domain.AdoptedRuntimeIdentity, error) {
	if len(fingerprints) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, org_id, service_id, environment_id, fingerprint_kind, fingerprint,
		       container_id, image_digest, endpoint_ref, host_alias, target_name, compose, created_at, updated_at
		FROM adopted_runtime_identity
		WHERE fingerprint = ANY($1)
		ORDER BY updated_at DESC
	`, fingerprints)
	if err != nil {
		return nil, fmt.Errorf("querying adopted runtime identities: %w", err)
	}
	defer rows.Close()

	var identities []domain.AdoptedRuntimeIdentity
	for rows.Next() {
		identity, err := scanAdoptedRuntimeIdentity(rows)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func scanAdoptedRuntimeIdentity(row pgx.Row) (domain.AdoptedRuntimeIdentity, error) {
	var identity domain.AdoptedRuntimeIdentity
	var composeJSON []byte
	if err := row.Scan(
		&identity.ID,
		&identity.OrgID,
		&identity.ServiceID,
		&identity.EnvironmentID,
		&identity.FingerprintKind,
		&identity.Fingerprint,
		&identity.ContainerID,
		&identity.ImageDigest,
		&identity.EndpointRef,
		&identity.HostAlias,
		&identity.TargetName,
		&composeJSON,
		&identity.CreatedAt,
		&identity.UpdatedAt,
	); err != nil {
		return identity, fmt.Errorf("scanning adopted runtime identity: %w", err)
	}
	if err := unmarshalJSON(composeJSON, &identity.Compose, "adopted runtime identity compose"); err != nil {
		return identity, err
	}
	return identity, nil
}
