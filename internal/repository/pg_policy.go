package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

const policyColumns = `id, name, environment_id, rules, enforcement, enabled, created_at, updated_at`

// PgDeploymentPolicyRepository implements DeploymentPolicyRepository using PostgreSQL.
type PgDeploymentPolicyRepository struct {
	pool *pgxpool.Pool
}

// NewPgDeploymentPolicyRepository creates a new PostgreSQL policy repository.
func NewPgDeploymentPolicyRepository(pool *pgxpool.Pool) *PgDeploymentPolicyRepository {
	return &PgDeploymentPolicyRepository{pool: pool}
}

// Create inserts a new deployment policy.
func (r *PgDeploymentPolicyRepository) Create(ctx context.Context, p *domain.DeploymentPolicy) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now

	rulesJSON, err := json.Marshal(p.Rules)
	if err != nil {
		return fmt.Errorf("marshaling rules: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO deployment_policies
			(id, name, environment_id, rules, enforcement, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.ID, p.Name, p.EnvironmentID, rulesJSON,
		string(p.Enforcement), p.Enabled, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting policy: %w", err)
	}
	return nil
}

// GetByID retrieves a policy by ID.
func (r *PgDeploymentPolicyRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM deployment_policies WHERE id = $1", policyColumns), id)
	return r.scanPolicy(row)
}

// GetByName retrieves a policy by name.
func (r *PgDeploymentPolicyRepository) GetByName(ctx context.Context, name string) (*domain.DeploymentPolicy, error) {
	row := r.pool.QueryRow(ctx,
		fmt.Sprintf("SELECT %s FROM deployment_policies WHERE name = $1", policyColumns), name)
	return r.scanPolicy(row)
}

// List returns all policies, optionally filtered to enabled only.
func (r *PgDeploymentPolicyRepository) List(ctx context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	query := fmt.Sprintf("SELECT %s FROM deployment_policies", policyColumns)
	if enabledOnly {
		query += " WHERE enabled = true"
	}
	query += " ORDER BY name"

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing policies: %w", err)
	}
	defer rows.Close()
	return r.scanPolicies(rows)
}

// ListByEnvironment returns enabled policies for a specific environment.
func (r *PgDeploymentPolicyRepository) ListByEnvironment(ctx context.Context, envID uuid.UUID) ([]domain.DeploymentPolicy, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM deployment_policies WHERE environment_id = $1 AND enabled = true ORDER BY name", policyColumns),
		envID)
	if err != nil {
		return nil, fmt.Errorf("listing env policies: %w", err)
	}
	defer rows.Close()
	return r.scanPolicies(rows)
}

// ListGlobal returns enabled policies that apply globally (no environment_id).
func (r *PgDeploymentPolicyRepository) ListGlobal(ctx context.Context) ([]domain.DeploymentPolicy, error) {
	rows, err := r.pool.Query(ctx,
		fmt.Sprintf("SELECT %s FROM deployment_policies WHERE environment_id IS NULL AND enabled = true ORDER BY name", policyColumns))
	if err != nil {
		return nil, fmt.Errorf("listing global policies: %w", err)
	}
	defer rows.Close()
	return r.scanPolicies(rows)
}

// Update modifies an existing policy.
func (r *PgDeploymentPolicyRepository) Update(ctx context.Context, p *domain.DeploymentPolicy) error {
	p.UpdatedAt = time.Now().UTC()

	rulesJSON, err := json.Marshal(p.Rules)
	if err != nil {
		return fmt.Errorf("marshaling rules: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`UPDATE deployment_policies
		 SET name = $1, environment_id = $2, rules = $3, enforcement = $4,
		     enabled = $5, updated_at = $6
		 WHERE id = $7`,
		p.Name, p.EnvironmentID, rulesJSON,
		string(p.Enforcement), p.Enabled, p.UpdatedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("updating policy: %w", err)
	}
	return nil
}

// Delete removes a policy by ID.
func (r *PgDeploymentPolicyRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM deployment_policies WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting policy: %w", err)
	}
	return nil
}

func (r *PgDeploymentPolicyRepository) scanPolicy(row pgx.Row) (*domain.DeploymentPolicy, error) {
	var p domain.DeploymentPolicy
	var enforcement string
	var rulesJSON []byte
	err := row.Scan(
		&p.ID, &p.Name, &p.EnvironmentID, &rulesJSON,
		&enforcement, &p.Enabled, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning policy: %w", err)
	}
	p.Enforcement = domain.PolicyEnforcement(enforcement)
	if err := json.Unmarshal(rulesJSON, &p.Rules); err != nil {
		return nil, fmt.Errorf("unmarshaling rules: %w", err)
	}
	return &p, nil
}

func (r *PgDeploymentPolicyRepository) scanPolicies(rows pgx.Rows) ([]domain.DeploymentPolicy, error) {
	var policies []domain.DeploymentPolicy
	for rows.Next() {
		var p domain.DeploymentPolicy
		var enforcement string
		var rulesJSON []byte
		err := rows.Scan(
			&p.ID, &p.Name, &p.EnvironmentID, &rulesJSON,
			&enforcement, &p.Enabled, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning policy row: %w", err)
		}
		p.Enforcement = domain.PolicyEnforcement(enforcement)
		if err := json.Unmarshal(rulesJSON, &p.Rules); err != nil {
			return nil, fmt.Errorf("unmarshaling rules: %w", err)
		}
		policies = append(policies, p)
	}
	return policies, rows.Err()
}
