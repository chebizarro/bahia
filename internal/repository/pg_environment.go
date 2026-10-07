package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgEnvironmentRepository is a PostgreSQL implementation of EnvironmentRepository.
type PgEnvironmentRepository struct {
	pool pgQueryer
}

func NewPgEnvironmentRepository(pool *pgxpool.Pool) *PgEnvironmentRepository {
	return newPgEnvironmentRepositoryWithDB(pool)
}

func newPgEnvironmentRepositoryWithDB(db pgQueryer) *PgEnvironmentRepository {
	return &PgEnvironmentRepository{pool: db}
}

const environmentColumns = `id, COALESCE(org_id, '00000000-0000-0000-0000-000000000000'::uuid), name, loom_worker_selector, runtime_config, targeting, deploy_strategy, protected, created_at, updated_at`

func (r *PgEnvironmentRepository) Create(ctx context.Context, env *domain.Environment) error {
	// The id is normally client-minted; the database never
	// generates it. Mint only for internal callers that supply none.
	if env.ID == uuid.Nil {
		env.ID = domain.NewEntityID()
	}
	domain.StampCreateRevision(&env.CreatedAt, &env.UpdatedAt)

	domain.NormalizeEnvironmentTargeting(env)
	selectorJSON, err := marshalJSON(env.LoomWorkerSelector, "loom worker selector")
	if err != nil {
		return err
	}
	configJSON, err := marshalJSON(env.RuntimeConfig, "runtime config")
	if err != nil {
		return err
	}
	targetingJSON, err := marshalJSON(env.Targeting, "environment targeting")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO environments (id, org_id, name, loom_worker_selector, runtime_config, targeting, deploy_strategy, protected, created_at, updated_at)
		VALUES ($1, NULLIF($2, '00000000-0000-0000-0000-000000000000'::uuid), $3, $4, $5, $6, $7, $8, $9, $10)
	`, env.ID, env.OrgID, env.Name, selectorJSON, configJSON, targetingJSON, env.DeployStrategy, env.Protected, env.CreatedAt, env.UpdatedAt)
	if err != nil {
		return classifyEnvironmentInsertError(env, err)
	}
	return nil
}

func (r *PgEnvironmentRepository) scanEnv(row scanner) (*domain.Environment, error) {
	env := &domain.Environment{}
	var selectorJSON, configJSON, targetingJSON []byte
	err := row.Scan(&env.ID, &env.OrgID, &env.Name, &selectorJSON, &configJSON, &targetingJSON, &env.DeployStrategy, &env.Protected, &env.CreatedAt, &env.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := unmarshalJSON(selectorJSON, &env.LoomWorkerSelector, "loom worker selector"); err != nil {
		return env, err
	}
	if err := unmarshalJSON(configJSON, &env.RuntimeConfig, "runtime config"); err != nil {
		return env, err
	}
	if len(targetingJSON) > 0 && string(targetingJSON) != "null" {
		if err := unmarshalJSON(targetingJSON, &env.Targeting, "environment targeting"); err != nil {
			return env, err
		}
	}
	domain.NormalizeEnvironmentTargeting(env)
	return env, nil
}

func (r *PgEnvironmentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.getByID(ctx, id, false)
}

// GetByIDForUpdate loads and locks an environment row for a complete-set
// deployment-unit reconciliation transaction.
func (r *PgEnvironmentRepository) GetByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Environment, error) {
	return r.getByID(ctx, id, true)
}

func (r *PgEnvironmentRepository) getByID(ctx context.Context, id uuid.UUID, forUpdate bool) (*domain.Environment, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	row := r.pool.QueryRow(ctx, `
		SELECT `+environmentColumns+`
		FROM environments WHERE id = $1`+lockClause, id)
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
		SELECT `+environmentColumns+`
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
		SELECT `+environmentColumns+`
		FROM environments ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing environments: %w", err)
	}
	defer rows.Close()

	var envs []domain.Environment
	for rows.Next() {
		env, err := r.scanEnv(rows)
		if err != nil {
			if env != nil {
				return nil, fmt.Errorf("reading environment %s: %w", env.ID, err)
			}
			return nil, fmt.Errorf("scanning environment: %w", err)
		}
		envs = append(envs, *env)
	}
	return envs, rows.Err()
}

func (r *PgEnvironmentRepository) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+environmentColumns+`
		FROM environments WHERE org_id = $1 ORDER BY name
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing environments by org: %w", err)
	}
	defer rows.Close()

	var envs []domain.Environment
	for rows.Next() {
		env, err := r.scanEnv(rows)
		if err != nil {
			if env != nil {
				return nil, fmt.Errorf("reading environment %s: %w", env.ID, err)
			}
			return nil, fmt.Errorf("scanning environment: %w", err)
		}
		envs = append(envs, *env)
	}
	return envs, rows.Err()
}

// Update stores env with the revision semantics of PgServiceRepository.Update.
func (r *PgEnvironmentRepository) Update(ctx context.Context, env *domain.Environment) error {
	requested, fallback := updateRevisionArgs(env.UpdatedAt)
	domain.NormalizeEnvironmentTargeting(env)
	selectorJSON, err := marshalJSON(env.LoomWorkerSelector, "loom worker selector")
	if err != nil {
		return err
	}
	configJSON, err := marshalJSON(env.RuntimeConfig, "runtime config")
	if err != nil {
		return err
	}
	targetingJSON, err := marshalJSON(env.Targeting, "environment targeting")
	if err != nil {
		return err
	}

	var stored time.Time
	err = r.pool.QueryRow(ctx, `
		UPDATE environments SET org_id=NULLIF($2, '00000000-0000-0000-0000-000000000000'::uuid), name=$3, loom_worker_selector=$4, runtime_config=$5, targeting=$6, deploy_strategy=$7, protected=$8, `+revisionAssignment("$9", "$10")+`
		WHERE id=$1
		RETURNING updated_at
	`, env.ID, env.OrgID, env.Name, selectorJSON, configJSON, targetingJSON, env.DeployStrategy, env.Protected, requested, fallback).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("updating environment %s: %w", env.ID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("updating environment: %w", err)
	}
	env.UpdatedAt = stored.UTC()
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

// classifyEnvironmentInsertError maps unique violations on insert to typed
// errors: a primary-key hit is ErrAlreadyExists (the caller decides between an
// idempotent retry and an id conflict), a name hit is ErrConflict.
func classifyEnvironmentInsertError(env *domain.Environment, err error) error {
	switch constraint := uniqueViolationConstraint(err); constraint {
	case "":
		return fmt.Errorf("inserting environment: %w", err)
	case "environments_pkey":
		return fmt.Errorf("inserting environment %s: %w", env.ID, ErrAlreadyExists)
	case "environments_name_key":
		return fmt.Errorf("inserting environment: environment name %q is already in use: %w", env.Name, ErrConflict)
	default:
		return fmt.Errorf("inserting environment: unique constraint %s violated: %w", constraint, ErrConflict)
	}
}
