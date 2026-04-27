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

const sigColumns = `id, artifact_id, signer_identity, signature_type, signature_ref,
	verified, verified_at, verification_error, metadata, created_at`

// PgArtifactSignatureRepository implements ArtifactSignatureRepository using PostgreSQL.
type PgArtifactSignatureRepository struct {
	pool *pgxpool.Pool
}

// NewPgArtifactSignatureRepository creates a new PostgreSQL signature repository.
func NewPgArtifactSignatureRepository(pool *pgxpool.Pool) *PgArtifactSignatureRepository {
	return &PgArtifactSignatureRepository{pool: pool}
}

// Create inserts a new artifact signature record.
func (r *PgArtifactSignatureRepository) Create(ctx context.Context, sig *domain.ArtifactSignature) error {
	if sig.ID == uuid.Nil {
		sig.ID = uuid.New()
	}
	sig.CreatedAt = time.Now().UTC()

	metaJSON, err := marshalJSON(sig.Metadata, "metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO artifact_signatures
			(id, artifact_id, signer_identity, signature_type, signature_ref,
			 verified, verified_at, verification_error, metadata, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		sig.ID, sig.ArtifactID, sig.SignerIdentity, string(sig.SignatureType),
		sig.SignatureRef, sig.Verified, sig.VerifiedAt, sig.VerificationError,
		metaJSON, sig.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting artifact signature: %w", err)
	}
	return nil
}

// GetByID retrieves a signature by its ID.
func (r *PgArtifactSignatureRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.ArtifactSignature, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_signatures WHERE id = $1", sigColumns), id)
	return r.scanSig(row)
}

// ListByArtifact returns all signatures for an artifact.
func (r *PgArtifactSignatureRepository) ListByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_signatures WHERE artifact_id = $1 ORDER BY created_at DESC", sigColumns),
		artifactID)
	if err != nil {
		return nil, fmt.Errorf("querying signatures: %w", err)
	}
	defer rows.Close()
	return r.scanSigs(rows)
}

// ListVerifiedByArtifact returns only verified signatures for an artifact.
func (r *PgArtifactSignatureRepository) ListVerifiedByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.ArtifactSignature, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM artifact_signatures WHERE artifact_id = $1 AND verified = true ORDER BY created_at DESC", sigColumns),
		artifactID)
	if err != nil {
		return nil, fmt.Errorf("querying verified signatures: %w", err)
	}
	defer rows.Close()
	return r.scanSigs(rows)
}

// HasVerifiedSignature checks whether an artifact has at least one verified signature.
func (r *PgArtifactSignatureRepository) HasVerifiedSignature(ctx context.Context, artifactID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM artifact_signatures WHERE artifact_id = $1 AND verified = true)",
		artifactID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking verified signature: %w", err)
	}
	return exists, nil
}

func (r *PgArtifactSignatureRepository) scanSig(row pgx.Row) (*domain.ArtifactSignature, error) {
	var sig domain.ArtifactSignature
	var sigType string
	var metaJSON []byte
	err := row.Scan(
		&sig.ID, &sig.ArtifactID, &sig.SignerIdentity, &sigType,
		&sig.SignatureRef, &sig.Verified, &sig.VerifiedAt,
		&sig.VerificationError, &metaJSON, &sig.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning signature: %w", err)
	}
	sig.SignatureType = domain.SignatureType(sigType)
	if err := unmarshalJSON(metaJSON, &sig.Metadata, "metadata"); err != nil {
		return nil, err
	}
	return &sig, nil
}

func (r *PgArtifactSignatureRepository) scanSigs(rows pgx.Rows) ([]domain.ArtifactSignature, error) {
	var sigs []domain.ArtifactSignature
	for rows.Next() {
		var sig domain.ArtifactSignature
		var sigType string
		var metaJSON []byte
		err := rows.Scan(
			&sig.ID, &sig.ArtifactID, &sig.SignerIdentity, &sigType,
			&sig.SignatureRef, &sig.Verified, &sig.VerifiedAt,
			&sig.VerificationError, &metaJSON, &sig.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning signature row: %w", err)
		}
		sig.SignatureType = domain.SignatureType(sigType)
		if err := unmarshalJSON(metaJSON, &sig.Metadata, "metadata"); err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	return sigs, rows.Err()
}
