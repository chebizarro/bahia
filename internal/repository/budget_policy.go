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

type BudgetPolicyRepository interface {
	Create(ctx context.Context, p *domain.BudgetPolicy) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.BudgetPolicy, error)
	GetByIDVersion(ctx context.Context, id uuid.UUID, version int64) (*domain.BudgetPolicy, error)
	List(ctx context.Context, enabledOnly bool) ([]domain.BudgetPolicy, error)
	Resolve(ctx context.Context, agentPubkey, taskID string) ([]domain.BudgetPolicy, error)
	Update(ctx context.Context, p *domain.BudgetPolicy) error
	Delete(ctx context.Context, id uuid.UUID) error
}

type PgBudgetPolicyRepository struct {
	pool *pgxpool.Pool
}

func NewPgBudgetPolicyRepository(pool *pgxpool.Pool) *PgBudgetPolicyRepository {
	return &PgBudgetPolicyRepository{pool: pool}
}

const budgetPolicyColumns = `id, version, name, agent_pubkey, task_id, scope, limits, enabled, created_at, updated_at`

func (r *PgBudgetPolicyRepository) Create(ctx context.Context, p *domain.BudgetPolicy) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now

	limitsJSON, err := json.Marshal(p.Limits)
	if err != nil {
		return fmt.Errorf("marshaling budget policy limits: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO budget_policies
			(id, version, name, agent_pubkey, task_id, scope, limits, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		p.ID, p.Version, p.Name, p.AgentPubkey, p.TaskID,
		string(p.Scope), limitsJSON, p.Enabled, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting budget policy: %w", err)
	}
	return nil
}

func (r *PgBudgetPolicyRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.BudgetPolicy, error) {
	return r.getByIDAndVersion(ctx, id, 0)
}

func (r *PgBudgetPolicyRepository) GetByIDVersion(ctx context.Context, id uuid.UUID, version int64) (*domain.BudgetPolicy, error) {
	return r.getByIDAndVersion(ctx, id, version)
}

func (r *PgBudgetPolicyRepository) getByIDAndVersion(ctx context.Context, id uuid.UUID, version int64) (*domain.BudgetPolicy, error) {
	query := fmt.Sprintf("SELECT %s FROM budget_policies WHERE id = $1", budgetPolicyColumns)
	args := []any{id}
	if version > 0 {
		query += " AND version = $2"
		args = append(args, version)
	}
	row := r.pool.QueryRow(ctx, query, args...)
	return scanBudgetPolicy(row)
}

func (r *PgBudgetPolicyRepository) List(ctx context.Context, enabledOnly bool) ([]domain.BudgetPolicy, error) {
	query := fmt.Sprintf("SELECT %s FROM budget_policies", budgetPolicyColumns)
	if enabledOnly {
		query += " WHERE enabled = true"
	}
	query += " ORDER BY name, version DESC"

	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing budget policies: %w", err)
	}
	defer rows.Close()
	return scanBudgetPolicies(rows)
}

func (r *PgBudgetPolicyRepository) Resolve(ctx context.Context, agentPubkey, taskID string) ([]domain.BudgetPolicy, error) {
	query := fmt.Sprintf(`SELECT %s FROM budget_policies
WHERE enabled = true AND (
    (agent_pubkey = $1 AND task_id = $2) OR
    (agent_pubkey = $1 AND task_id = '') OR
    (agent_pubkey = ''  AND task_id = $2)
)
ORDER BY
    CASE
        WHEN agent_pubkey = $1 AND task_id = $2 THEN 0
        WHEN agent_pubkey = $1 AND task_id = ''  THEN 1
        WHEN agent_pubkey = ''  AND task_id = $2 THEN 2
    END`, budgetPolicyColumns)
	rows, err := r.pool.Query(ctx, query, agentPubkey, taskID)
	if err != nil {
		return nil, fmt.Errorf("resolving budget policies: %w", err)
	}
	defer rows.Close()
	policies, err := scanBudgetPolicies(rows)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, nil
	}
	bestOrder := policyOrder(policies[0], agentPubkey, taskID)
	result := []domain.BudgetPolicy{policies[0]}
	for i := 1; i < len(policies); i++ {
		o := policyOrder(policies[i], agentPubkey, taskID)
		if o < bestOrder {
			result = []domain.BudgetPolicy{policies[i]}
			bestOrder = o
		} else if o == bestOrder {
			result = append(result, policies[i])
		}
	}
	return result, nil
}

func policyOrder(p domain.BudgetPolicy, agentPubkey, taskID string) int {
	if p.AgentPubkey == agentPubkey && p.TaskID == taskID {
		return 0
	}
	if p.AgentPubkey == agentPubkey && p.TaskID == "" {
		return 1
	}
	if p.AgentPubkey == "" && p.TaskID == taskID {
		return 2
	}
	return 3
}

func (r *PgBudgetPolicyRepository) Update(ctx context.Context, p *domain.BudgetPolicy) error {
	p.UpdatedAt = time.Now().UTC()

	limitsJSON, err := json.Marshal(p.Limits)
	if err != nil {
		return fmt.Errorf("marshaling budget policy limits: %w", err)
	}

	tag, err := r.pool.Exec(ctx,
		`UPDATE budget_policies
		 SET version = $1, name = $2, agent_pubkey = $3, task_id = $4,
		     scope = $5, limits = $6, enabled = $7, updated_at = $8
		 WHERE id = $9`,
		p.Version, p.Name, p.AgentPubkey, p.TaskID,
		string(p.Scope), limitsJSON, p.Enabled, p.UpdatedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("updating budget policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PgBudgetPolicyRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM budget_policies WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("deleting budget policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanBudgetPolicy(row pgx.Row) (*domain.BudgetPolicy, error) {
	var p domain.BudgetPolicy
	var scope string
	var limitsJSON []byte
	err := row.Scan(
		&p.ID, &p.Version, &p.Name, &p.AgentPubkey, &p.TaskID,
		&scope, &limitsJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning budget policy: %w", err)
	}
	p.Scope = domain.BudgetPolicyScope(scope)
	if err := json.Unmarshal(limitsJSON, &p.Limits); err != nil {
		return nil, fmt.Errorf("unmarshaling budget policy limits: %w", err)
	}
	return &p, nil
}

func scanBudgetPolicies(rows pgx.Rows) ([]domain.BudgetPolicy, error) {
	var policies []domain.BudgetPolicy
	for rows.Next() {
		var p domain.BudgetPolicy
		var scope string
		var limitsJSON []byte
		err := rows.Scan(
			&p.ID, &p.Version, &p.Name, &p.AgentPubkey, &p.TaskID,
			&scope, &limitsJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning budget policy row: %w", err)
		}
		p.Scope = domain.BudgetPolicyScope(scope)
		if err := json.Unmarshal(limitsJSON, &p.Limits); err != nil {
			return nil, fmt.Errorf("unmarshaling budget policy limits row: %w", err)
		}
		policies = append(policies, p)
	}
	return policies, rows.Err()
}
