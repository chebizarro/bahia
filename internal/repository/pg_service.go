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

// PgServiceRepository is a PostgreSQL implementation of ServiceRepository.
type PgServiceRepository struct {
	pool *pgxpool.Pool
}

// NewPgServiceRepository creates a new PgServiceRepository.
func NewPgServiceRepository(pool *pgxpool.Pool) *PgServiceRepository {
	return &PgServiceRepository{pool: pool}
}

func (r *PgServiceRepository) Create(ctx context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	now := time.Now().UTC()
	svc.CreatedAt = now
	svc.UpdatedAt = now

	_, err := r.pool.Exec(ctx, `
		INSERT INTO services (id, name, repo_url, artifact_repo, default_branch, runtime_type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, svc.ID, svc.Name, svc.RepoURL, svc.ArtifactRepo, svc.DefaultBranch, svc.RuntimeType, svc.CreatedAt, svc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting service: %w", err)
	}
	return nil
}

func (r *PgServiceRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	svc := &domain.Service{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, repo_url, artifact_repo, default_branch, runtime_type, created_at, updated_at
		FROM services WHERE id = $1
	`, id).Scan(&svc.ID, &svc.Name, &svc.RepoURL, &svc.ArtifactRepo, &svc.DefaultBranch, &svc.RuntimeType, &svc.CreatedAt, &svc.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying service by id: %w", err)
	}
	return svc, nil
}

func (r *PgServiceRepository) GetByName(ctx context.Context, name string) (*domain.Service, error) {
	svc := &domain.Service{}
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, repo_url, artifact_repo, default_branch, runtime_type, created_at, updated_at
		FROM services WHERE name = $1
	`, name).Scan(&svc.ID, &svc.Name, &svc.RepoURL, &svc.ArtifactRepo, &svc.DefaultBranch, &svc.RuntimeType, &svc.CreatedAt, &svc.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying service by name: %w", err)
	}
	return svc, nil
}

func (r *PgServiceRepository) List(ctx context.Context) ([]domain.Service, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, repo_url, artifact_repo, default_branch, runtime_type, created_at, updated_at
		FROM services ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	defer rows.Close()

	var services []domain.Service
	for rows.Next() {
		var svc domain.Service
		if err := rows.Scan(&svc.ID, &svc.Name, &svc.RepoURL, &svc.ArtifactRepo, &svc.DefaultBranch, &svc.RuntimeType, &svc.CreatedAt, &svc.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning service: %w", err)
		}
		services = append(services, svc)
	}
	return services, rows.Err()
}

func (r *PgServiceRepository) Update(ctx context.Context, svc *domain.Service) error {
	svc.UpdatedAt = time.Now().UTC()
	cmd, err := r.pool.Exec(ctx, `
		UPDATE services SET name=$2, repo_url=$3, artifact_repo=$4, default_branch=$5, runtime_type=$6, updated_at=$7
		WHERE id=$1
	`, svc.ID, svc.Name, svc.RepoURL, svc.ArtifactRepo, svc.DefaultBranch, svc.RuntimeType, svc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating service: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating service %s: %w", svc.ID, ErrNotFound)
	}
	return nil
}

// CountDependents returns counts of dependent resources for a service.
func (r *PgServiceRepository) CountDependents(ctx context.Context, id uuid.UUID) (builds, artifacts, intents int, err error) {
	err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM builds WHERE service_id = $1`, id).Scan(&builds)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("counting builds: %w", err)
	}
	err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM artifacts WHERE service_id = $1`, id).Scan(&artifacts)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("counting artifacts: %w", err)
	}
	err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM deployment_intents WHERE service_id = $1`, id).Scan(&intents)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("counting intents: %w", err)
	}
	return builds, artifacts, intents, nil
}

func (r *PgServiceRepository) Delete(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM services WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting service: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting service %s: %w", id, ErrNotFound)
	}
	return nil
}
