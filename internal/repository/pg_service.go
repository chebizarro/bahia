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

	repositoryJSON, err := marshalJSON(svc.Repository, "repository")
	if err != nil {
		return err
	}
	runtimeConfigJSON, err := marshalJSON(svc.RuntimeConfig, "service runtime config")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO services (id, name, repo_url, repository, artifact_repo, default_branch, runtime_type, runtime_config, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, svc.ID, svc.Name, svc.RepoURL, repositoryJSON, svc.ArtifactRepo, svc.DefaultBranch, svc.RuntimeType, runtimeConfigJSON, svc.CreatedAt, svc.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting service: %w", err)
	}
	return nil
}

func (r *PgServiceRepository) scanService(row pgx.Row) (*domain.Service, error) {
	svc := &domain.Service{}
	var repositoryJSON, runtimeConfigJSON []byte
	if err := row.Scan(&svc.ID, &svc.Name, &svc.RepoURL, &repositoryJSON, &svc.ArtifactRepo, &svc.DefaultBranch, &svc.RuntimeType, &runtimeConfigJSON, &svc.CreatedAt, &svc.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(repositoryJSON, &svc.Repository, "repository"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(runtimeConfigJSON, &svc.RuntimeConfig, "service runtime config"); err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *PgServiceRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Service, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, repo_url, repository, artifact_repo, default_branch, runtime_type, runtime_config, created_at, updated_at
		FROM services WHERE id = $1
	`, id)
	svc, err := r.scanService(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying service by id: %w", err)
	}
	return svc, nil
}

func (r *PgServiceRepository) GetByName(ctx context.Context, name string) (*domain.Service, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, repo_url, repository, artifact_repo, default_branch, runtime_type, runtime_config, created_at, updated_at
		FROM services WHERE name = $1
	`, name)
	svc, err := r.scanService(row)
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
		SELECT id, name, repo_url, repository, artifact_repo, default_branch, runtime_type, runtime_config, created_at, updated_at
		FROM services ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	defer rows.Close()

	var services []domain.Service
	for rows.Next() {
		svc, err := r.scanService(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning service: %w", err)
		}
		services = append(services, *svc)
	}
	return services, rows.Err()
}

func (r *PgServiceRepository) Update(ctx context.Context, svc *domain.Service) error {
	svc.UpdatedAt = time.Now().UTC()
	repositoryJSON, err := marshalJSON(svc.Repository, "repository")
	if err != nil {
		return err
	}
	runtimeConfigJSON, err := marshalJSON(svc.RuntimeConfig, "service runtime config")
	if err != nil {
		return err
	}

	cmd, err := r.pool.Exec(ctx, `
		UPDATE services SET name=$2, repo_url=$3, repository=$4, artifact_repo=$5, default_branch=$6, runtime_type=$7, runtime_config=$8, updated_at=$9
		WHERE id=$1
	`, svc.ID, svc.Name, svc.RepoURL, repositoryJSON, svc.ArtifactRepo, svc.DefaultBranch, svc.RuntimeType, runtimeConfigJSON, svc.UpdatedAt)
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
