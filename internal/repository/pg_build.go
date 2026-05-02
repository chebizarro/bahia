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

// PgBuildRepository is a PostgreSQL implementation of BuildRepository.
type buildDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PgBuildRepository struct {
	pool buildDB
}

func NewPgBuildRepository(pool *pgxpool.Pool) *PgBuildRepository {
	return newPgBuildRepositoryWithDB(pool)
}

func newPgBuildRepositoryWithDB(db buildDB) *PgBuildRepository {
	return &PgBuildRepository{pool: db}
}

func (r *PgBuildRepository) Create(ctx context.Context, b *domain.Build) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	b.CreatedAt = time.Now().UTC()

	metaJSON, err := marshalJSON(b.Metadata, "build metadata")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO builds (id, service_id, git_sha, git_ref, ci_system, ci_run_id, loom_job_id, status, source_event_id, started_at, finished_at, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, b.ID, b.ServiceID, b.GitSHA, b.GitRef, b.CISystem, b.CIRunID, b.LoomJobID, b.Status, b.SourceEventID, b.StartedAt, b.FinishedAt, metaJSON, b.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting build: %w", err)
	}
	return nil
}

func (r *PgBuildRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Build, error) {
	b := &domain.Build{}
	var metaJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, service_id, git_sha, git_ref, ci_system, ci_run_id, loom_job_id, status, source_event_id, started_at, finished_at, metadata, created_at
		FROM builds WHERE id = $1
	`, id).Scan(&b.ID, &b.ServiceID, &b.GitSHA, &b.GitRef, &b.CISystem, &b.CIRunID, &b.LoomJobID, &b.Status, &b.SourceEventID, &b.StartedAt, &b.FinishedAt, &metaJSON, &b.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying build by id: %w", err)
	}
	if err := unmarshalJSON(metaJSON, &b.Metadata, "build metadata"); err != nil {
		return nil, fmt.Errorf("reading build %s: %w", id, err)
	}
	return b, nil
}

func (r *PgBuildRepository) GetByCISystemRunID(ctx context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	b := &domain.Build{}
	var metaJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, service_id, git_sha, git_ref, ci_system, ci_run_id, loom_job_id, status, source_event_id, started_at, finished_at, metadata, created_at
		FROM builds WHERE ci_system = $1 AND ci_run_id = $2
	`, ciSystem, ciRunID).Scan(&b.ID, &b.ServiceID, &b.GitSHA, &b.GitRef, &b.CISystem, &b.CIRunID, &b.LoomJobID, &b.Status, &b.SourceEventID, &b.StartedAt, &b.FinishedAt, &metaJSON, &b.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying build by ci system/run id: %w", err)
	}
	if err := unmarshalJSON(metaJSON, &b.Metadata, "build metadata"); err != nil {
		return nil, fmt.Errorf("reading build for ci system/run id %s/%s: %w", ciSystem, ciRunID, err)
	}
	return b, nil
}

func (r *PgBuildRepository) ListByService(ctx context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, service_id, git_sha, git_ref, ci_system, ci_run_id, loom_job_id, status, source_event_id, started_at, finished_at, metadata, created_at
		FROM builds WHERE service_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`, serviceID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing builds: %w", err)
	}
	defer rows.Close()

	var builds []domain.Build
	for rows.Next() {
		var b domain.Build
		var metaJSON []byte
		if err := rows.Scan(&b.ID, &b.ServiceID, &b.GitSHA, &b.GitRef, &b.CISystem, &b.CIRunID, &b.LoomJobID, &b.Status, &b.SourceEventID, &b.StartedAt, &b.FinishedAt, &metaJSON, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning build: %w", err)
		}
		if err := unmarshalJSON(metaJSON, &b.Metadata, "build metadata"); err != nil {
			return nil, fmt.Errorf("reading build %s: %w", b.ID, err)
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

func (r *PgBuildRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE builds SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("updating build status: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating build %s status: %w", id, ErrNotFound)
	}
	return nil
}
