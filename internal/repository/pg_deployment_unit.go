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

const deploymentUnitColumns = `id, environment_id, unit_key, display_name, runtime_type, endpoint_ref, compose_dir, namespace, network_profile, reconcile_mode, ownership_mode, runtime_config, created_at, updated_at`

// PgDeploymentUnitRepository is a PostgreSQL implementation for deployment units.
type PgDeploymentUnitRepository struct {
	pool pgQueryer
}

func NewPgDeploymentUnitRepository(pool *pgxpool.Pool) *PgDeploymentUnitRepository {
	return newPgDeploymentUnitRepositoryWithDB(pool)
}

func newPgDeploymentUnitRepositoryWithDB(db pgQueryer) *PgDeploymentUnitRepository {
	return &PgDeploymentUnitRepository{pool: db}
}

func (r *PgDeploymentUnitRepository) Create(ctx context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	domain.NormalizeDeploymentUnitTargeting(unit)
	if err := domain.ValidateDeploymentUnit(unit); err != nil {
		return err
	}
	now := time.Now().UTC()
	unit.CreatedAt = now
	unit.UpdatedAt = now
	unit.Implicit = false

	runtimeConfigJSON, err := marshalJSON(unit.RuntimeConfig, "deployment unit runtime config")
	if err != nil {
		return err
	}
	networkProfileJSON, err := marshalJSON(unit.NetworkProfile, "deployment unit network profile")
	if err != nil {
		return err
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO deployment_units (`+deploymentUnitColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, unit.ID, unit.EnvironmentID, unit.Key, unit.DisplayName, unit.RuntimeType, unit.EndpointRef, unit.ComposeDir, unit.Namespace,
		networkProfileJSON, unit.ReconcileMode, unit.OwnershipMode, runtimeConfigJSON, unit.CreatedAt, unit.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting deployment unit: %w", err)
	}
	return nil
}

func (r *PgDeploymentUnitRepository) scanUnit(row pgx.Row) (*domain.DeploymentUnit, error) {
	unit := &domain.DeploymentUnit{}
	var runtimeConfigJSON, networkProfileJSON []byte
	err := row.Scan(&unit.ID, &unit.EnvironmentID, &unit.Key, &unit.DisplayName, &unit.RuntimeType,
		&unit.EndpointRef, &unit.ComposeDir, &unit.Namespace, &networkProfileJSON, &unit.ReconcileMode, &unit.OwnershipMode,
		&runtimeConfigJSON, &unit.CreatedAt, &unit.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(networkProfileJSON) > 0 && string(networkProfileJSON) != "null" {
		if err := unmarshalJSON(networkProfileJSON, &unit.NetworkProfile, "deployment unit network profile"); err != nil {
			return nil, err
		}
	}
	if len(runtimeConfigJSON) > 0 && string(runtimeConfigJSON) != "null" {
		if err := unmarshalJSON(runtimeConfigJSON, &unit.RuntimeConfig, "deployment unit runtime config"); err != nil {
			return nil, err
		}
	}
	domain.NormalizeDeploymentUnitTargeting(unit)
	return unit, nil
}

func (r *PgDeploymentUnitRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	unit, err := r.scanUnit(r.pool.QueryRow(ctx, `SELECT `+deploymentUnitColumns+` FROM deployment_units WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying deployment unit by id: %w", err)
	}
	return unit, nil
}

func (r *PgDeploymentUnitRepository) GetByEnvironmentKey(ctx context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	unit, err := r.scanUnit(r.pool.QueryRow(ctx, `
		SELECT `+deploymentUnitColumns+` FROM deployment_units
		WHERE environment_id = $1 AND unit_key = $2
	`, environmentID, key))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying deployment unit by environment/key: %w", err)
	}
	return unit, nil
}

func (r *PgDeploymentUnitRepository) ListByEnvironment(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.listByEnvironment(ctx, environmentID, false)
}

// ListByEnvironmentForUpdate locks persisted units so references cannot race with unit-set reconciliation.
func (r *PgDeploymentUnitRepository) ListByEnvironmentForUpdate(ctx context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	return r.listByEnvironment(ctx, environmentID, true)
}

func (r *PgDeploymentUnitRepository) listByEnvironment(ctx context.Context, environmentID uuid.UUID, forUpdate bool) ([]domain.DeploymentUnit, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+deploymentUnitColumns+` FROM deployment_units
		WHERE environment_id = $1
		ORDER BY unit_key`+lockClause, environmentID)
	if err != nil {
		return nil, fmt.Errorf("listing deployment units: %w", err)
	}
	defer rows.Close()

	var units []domain.DeploymentUnit
	for rows.Next() {
		unit, err := r.scanUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning deployment unit: %w", err)
		}
		units = append(units, *unit)
	}
	return units, rows.Err()
}

// Update persists mutable deployment-unit targeting and ownership fields while preserving identity.
func (r *PgDeploymentUnitRepository) Update(ctx context.Context, unit *domain.DeploymentUnit) error {
	if unit == nil || unit.ID == uuid.Nil {
		return fmt.Errorf("%w: deployment unit id", domain.ErrNilUUID)
	}
	domain.NormalizeDeploymentUnitTargeting(unit)
	if err := domain.ValidateDeploymentUnit(unit); err != nil {
		return err
	}
	unit.UpdatedAt = time.Now().UTC()

	runtimeConfigJSON, err := marshalJSON(unit.RuntimeConfig, "deployment unit runtime config")
	if err != nil {
		return err
	}
	networkProfileJSON, err := marshalJSON(unit.NetworkProfile, "deployment unit network profile")
	if err != nil {
		return err
	}

	cmd, err := r.pool.Exec(ctx, `
		UPDATE deployment_units
		SET unit_key=$2, display_name=$3, runtime_type=$4, endpoint_ref=$5, compose_dir=$6,
		    namespace=$7, network_profile=$8, reconcile_mode=$9, ownership_mode=$10,
		    runtime_config=$11, updated_at=$12
		WHERE id=$1 AND environment_id=$13
	`, unit.ID, unit.Key, unit.DisplayName, unit.RuntimeType, unit.EndpointRef, unit.ComposeDir,
		unit.Namespace, networkProfileJSON, unit.ReconcileMode, unit.OwnershipMode, runtimeConfigJSON,
		unit.UpdatedAt, unit.EnvironmentID)
	if err != nil {
		return fmt.Errorf("updating deployment unit: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("updating deployment unit %s: %w", unit.ID, ErrNotFound)
	}
	return nil
}

// DeleteIfUnreferenced removes a unit only when no durable state, run, intent, or observation refers to it.
func (r *PgDeploymentUnitRepository) DeleteIfUnreferenced(ctx context.Context, id uuid.UUID) error {
	cmd, err := r.pool.Exec(ctx, `
		DELETE FROM deployment_units du
		WHERE du.id = $1
		  AND NOT EXISTS (SELECT 1 FROM environment_service_state WHERE deployment_unit_id = du.id)
		  AND NOT EXISTS (SELECT 1 FROM deployment_runs WHERE deployment_unit_id = du.id)
		  AND NOT EXISTS (SELECT 1 FROM deployment_intents WHERE deployment_unit_id = du.id)
		  AND NOT EXISTS (SELECT 1 FROM runtime_observations WHERE deployment_unit_id = du.id)
	`, id)
	if err != nil {
		return fmt.Errorf("deleting deployment unit: %w", err)
	}
	if cmd.RowsAffected() > 0 {
		return nil
	}

	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployment_units WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("checking deployment unit after protected delete: %w", err)
	}
	if !exists {
		return fmt.Errorf("deleting deployment unit %s: %w", id, ErrNotFound)
	}
	return fmt.Errorf("deployment unit %s is referenced by durable deployment state: %w", id, ErrConflict)
}

// ResolveDefault returns the configured explicit default unit when present.
// It synthesizes the legacy implicit default only when the normalized default key
// is "default" and the environment has no explicit units.
func (r *PgDeploymentUnitRepository) ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if env == nil {
		return nil, fmt.Errorf("%w: environment must not be nil", domain.ErrInvalidValue)
	}
	if env.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: environment_id", domain.ErrNilUUID)
	}

	envCopy := *env
	domain.NormalizeEnvironmentTargeting(&envCopy)
	units, err := r.ListByEnvironment(ctx, envCopy.ID)
	if err != nil {
		return nil, err
	}
	for i := range units {
		if units[i].Key == envCopy.Targeting.DefaultUnitKey {
			unit := units[i]
			return &unit, nil
		}
	}
	if len(units) > 0 {
		return nil, fmt.Errorf("environment %s default deployment unit %q is not present in the explicit unit set: %w", envCopy.ID, envCopy.Targeting.DefaultUnitKey, ErrConflict)
	}
	if envCopy.Targeting.DefaultUnitKey != domain.DefaultDeploymentUnitKey {
		return nil, fmt.Errorf("environment %s configured default deployment unit %q was not found: %w", envCopy.ID, envCopy.Targeting.DefaultUnitKey, ErrConflict)
	}
	return domain.NewImplicitDefaultDeploymentUnit(&envCopy)
}
