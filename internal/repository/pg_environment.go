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

// PgEnvironmentRepository is a PostgreSQL implementation of EnvironmentRepository.
type PgEnvironmentRepository struct {
	pool *pgxpool.Pool
}

func NewPgEnvironmentRepository(pool *pgxpool.Pool) *PgEnvironmentRepository {
	return &PgEnvironmentRepository{pool: pool}
}

func (r *PgEnvironmentRepository) Create(ctx context.Context, env *domain.Environment) error {
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	now := time.Now().UTC()
	env.CreatedAt = now
	env.UpdatedAt = now

	selectorJSON, err := marshalJSON(env.LoomWorkerSelector, "loom worker selector")
	if err != nil {
		return err
	}
	configJSON, err := marshalJSON(env.RuntimeConfig, "runtime config")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO environments (id, name, loom_worker_selector, runtime_config, deploy_strategy, protected, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, env.ID, env.Name, selectorJSON, configJSON, env.DeployStrategy, env.Protected, env.CreatedAt, env.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting environment: %w", err)
	}
	return nil
}

func (r *PgEnvironmentRepository) scanEnv(row pgx.Row) (*domain.Environment, error) {
	env := &domain.Environment{}
	var selectorJSON, configJSON []byte
	err := row.Scan(&env.ID, &env.Name, &selectorJSON, &configJSON, &env.DeployStrategy, &env.Protected, &env.CreatedAt, &env.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(selectorJSON, &env.LoomWorkerSelector, "loom worker selector"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(configJSON, &env.RuntimeConfig, "runtime config"); err != nil {
		return nil, err
	}
	return env, nil
}

func (r *PgEnvironmentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, loom_worker_selector, runtime_config, deploy_strategy, protected, created_at, updated_at
		FROM environments WHERE id = $1
	`, id)
	env, err := r.scanEnv(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying environment by id: %w", err)
	}
	return env, nil
}

func (r *PgEnvironmentRepository) GetByName(ctx context.Context, name string) (*domain.Environment, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, loom_worker_selector, runtime_config, deploy_strategy, protected, created_at, updated_at
		FROM environments WHERE name = $1
	`, name)
	env, err := r.scanEnv(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying environment by name: %w", err)
	}
	return env, nil
}

func (r *PgEnvironmentRepository) List(ctx context.Context) ([]domain.Environment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, loom_worker_selector, runtime_config, deploy_strategy, protected, created_at, updated_at
		FROM environments ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing environments: %w", err)
	}
	defer rows.Close()

	var envs []domain.Environment
	for rows.Next() {
		var selectorJSON, configJSON []byte
		var env domain.Environment
		if err := rows.Scan(&env.ID, &env.Name, &selectorJSON, &configJSON, &env.DeployStrategy, &env.Protected, &env.CreatedAt, &env.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning environment: %w", err)
		}
		if err := unmarshalJSON(selectorJSON, &env.LoomWorkerSelector, "loom worker selector"); err != nil {
			return nil, fmt.Errorf("reading environment %s: %w", env.ID, err)
		}
		if err := unmarshalJSON(configJSON, &env.RuntimeConfig, "runtime config"); err != nil {
			return nil, fmt.Errorf("reading environment %s: %w", env.ID, err)
		}
		envs = append(envs, env)
	}
	return envs, rows.Err()
}

func (r *PgEnvironmentRepository) Update(ctx context.Context, env *domain.Environment) error {
	env.UpdatedAt = time.Now().UTC()
	selectorJSON, err := marshalJSON(env.LoomWorkerSelector, "loom worker selector")
	if err != nil {
		return err
	}
	configJSON, err := marshalJSON(env.RuntimeConfig, "runtime config")
	if err != nil {
		return err
	}

	cmd, err := r.pool.Exec(ctx, `
		UPDATE environments SET name=$2, loom_worker_selector=$3, runtime_config=$4, deploy_strategy=$5, protected=$6, updated_at=$7
		WHERE id=$1
	`, env.ID, env.Name, selectorJSON, configJSON, env.DeployStrategy, env.Protected, env.UpdatedAt)
	if err != nil {
		return fmt.Errorf("updating environment: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating environment %s: %w", env.ID, ErrNotFound)
	}
	return nil
}

// CountDependents returns counts of dependent resources for an environment.
func (r *PgEnvironmentRepository) CountDependents(ctx context.Context, id uuid.UUID) (intents, states int, err error) {
	err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM deployment_intents WHERE environment_id = $1`, id).Scan(&intents)
	if err != nil {
		return 0, 0, fmt.Errorf("counting intents: %w", err)
	}
	err = r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM environment_service_state WHERE environment_id = $1`, id).Scan(&states)
	if err != nil {
		return 0, 0, fmt.Errorf("counting states: %w", err)
	}
	return intents, states, nil
}

func (r *PgEnvironmentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `DELETE FROM environments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting environment: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("deleting environment %s: %w", id, ErrNotFound)
	}
	return nil
}
