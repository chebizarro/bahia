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

const deploymentUnitColumns = `id, environment_id, unit_key, display_name, runtime_type, reconcile_mode, ownership_mode, runtime_config, created_at, updated_at`

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

	_, err = r.pool.Exec(ctx, `
		INSERT INTO deployment_units (`+deploymentUnitColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, unit.ID, unit.EnvironmentID, unit.Key, unit.DisplayName, unit.RuntimeType, unit.ReconcileMode,
		unit.OwnershipMode, runtimeConfigJSON, unit.CreatedAt, unit.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting deployment unit: %w", err)
	}
	return nil
}

func (r *PgDeploymentUnitRepository) scanUnit(row pgx.Row) (*domain.DeploymentUnit, error) {
	unit := &domain.DeploymentUnit{}
	var runtimeConfigJSON []byte
	err := row.Scan(&unit.ID, &unit.EnvironmentID, &unit.Key, &unit.DisplayName, &unit.RuntimeType,
		&unit.ReconcileMode, &unit.OwnershipMode, &runtimeConfigJSON, &unit.CreatedAt, &unit.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(runtimeConfigJSON) > 0 && string(runtimeConfigJSON) != "null" {
		if err := unmarshalJSON(runtimeConfigJSON, &unit.RuntimeConfig, "deployment unit runtime config"); err != nil {
			return nil, err
		}
	}
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
	rows, err := r.pool.Query(ctx, `
		SELECT `+deploymentUnitColumns+` FROM deployment_units
		WHERE environment_id = $1
		ORDER BY unit_key
	`, environmentID)
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

// ResolveDefault returns an explicitly persisted default unit when present;
// otherwise it synthesizes the implicit default in memory without writing it.
func (r *PgDeploymentUnitRepository) ResolveDefault(ctx context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if env == nil {
		return nil, fmt.Errorf("%w: environment must not be nil", domain.ErrInvalidValue)
	}
	explicit, err := r.GetByEnvironmentKey(ctx, env.ID, domain.DefaultDeploymentUnitKey)
	if err != nil {
		return nil, err
	}
	if explicit != nil {
		return explicit, nil
	}
	return domain.NewImplicitDefaultDeploymentUnit(env)
}
