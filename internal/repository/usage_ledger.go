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

type UsageLedgerRepository interface {
	Insert(ctx context.Context, record *domain.UsageLedgerRecord) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.UsageLedgerRecord, error)
	List(ctx context.Context, filter domain.UsageLedgerFilter) ([]domain.UsageLedgerRecord, error)
	GetCorrections(ctx context.Context, originalID uuid.UUID) ([]domain.UsageLedgerRecord, error)
	SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error)
}

type PgUsageLedgerRepository struct {
	pool pgQueryer
}

func NewPgUsageLedgerRepository(pool *pgxpool.Pool) *PgUsageLedgerRepository {
	return &PgUsageLedgerRepository{pool: pool}
}

const usageLedgerColumns = `id, agent_pubkey, task_id, resource_type, amount, recorded_at, recorded_by, signature, correction_of, created_at`

func (r *PgUsageLedgerRepository) Insert(ctx context.Context, record *domain.UsageLedgerRecord) error {
	if err := domain.ValidateUsageLedgerRecord(record); err != nil {
		return err
	}
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO usage_ledger_records (`+usageLedgerColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (agent_pubkey, task_id, resource_type, recorded_at, recorded_by) DO NOTHING
	`, record.ID, record.AgentPubkey, record.TaskID, record.ResourceType, record.Amount,
		record.RecordedAt, record.RecordedBy, record.Signature, record.CorrectionOf, record.CreatedAt)
	if err != nil {
		return fmt.Errorf("inserting usage ledger record: %w", err)
	}
	return nil
}

func (r *PgUsageLedgerRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.UsageLedgerRecord, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+usageLedgerColumns+` FROM usage_ledger_records WHERE id = $1`, id)
	rec, err := scanUsageLedgerRecord(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("getting usage ledger record: %w", err)
	}
	return rec, nil
}

func (r *PgUsageLedgerRepository) List(ctx context.Context, filter domain.UsageLedgerFilter) ([]domain.UsageLedgerRecord, error) {
	query := `SELECT ` + usageLedgerColumns + ` FROM usage_ledger_records WHERE 1=1`
	args := []any{}
	argIdx := 1

	if filter.AgentPubkey != "" {
		query += fmt.Sprintf(` AND agent_pubkey = $%d`, argIdx)
		args = append(args, filter.AgentPubkey)
		argIdx++
	}
	if filter.TaskID != "" {
		query += fmt.Sprintf(` AND task_id = $%d`, argIdx)
		args = append(args, filter.TaskID)
		argIdx++
	}
	if filter.ResourceType != "" {
		query += fmt.Sprintf(` AND resource_type = $%d`, argIdx)
		args = append(args, filter.ResourceType)
		argIdx++
	}
	if !filter.Since.IsZero() {
		query += fmt.Sprintf(` AND recorded_at >= $%d`, argIdx)
		args = append(args, filter.Since)
		argIdx++
	}
	if !filter.Until.IsZero() {
		query += fmt.Sprintf(` AND recorded_at <= $%d`, argIdx)
		args = append(args, filter.Until)
		argIdx++
	}
	query += ` ORDER BY recorded_at ASC`
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	query += fmt.Sprintf(` LIMIT $%d`, argIdx)
	args = append(args, filter.Limit)
	argIdx++
	if filter.Offset > 0 {
		query += fmt.Sprintf(` OFFSET $%d`, argIdx)
		args = append(args, filter.Offset)
		argIdx++
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing usage ledger records: %w", err)
	}
	defer rows.Close()
	return scanUsageLedgerRecords(rows)
}

func (r *PgUsageLedgerRepository) GetCorrections(ctx context.Context, originalID uuid.UUID) ([]domain.UsageLedgerRecord, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+usageLedgerColumns+` FROM usage_ledger_records WHERE correction_of = $1 ORDER BY created_at ASC`, originalID)
	if err != nil {
		return nil, fmt.Errorf("getting usage ledger corrections: %w", err)
	}
	defer rows.Close()
	return scanUsageLedgerRecords(rows)
}

func (r *PgUsageLedgerRepository) SumByAgent(ctx context.Context, agentPubkey string, resourceType domain.UsageResourceType, since, until time.Time) (int64, error) {
	query := `SELECT COALESCE(SUM(amount), 0) FROM usage_ledger_records WHERE agent_pubkey = $1`
	args := []any{agentPubkey}
	argIdx := 2

	if resourceType != "" {
		query += fmt.Sprintf(` AND resource_type = $%d`, argIdx)
		args = append(args, resourceType)
		argIdx++
	}
	if !since.IsZero() {
		query += fmt.Sprintf(` AND recorded_at >= $%d`, argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(` AND recorded_at <= $%d`, argIdx)
		args = append(args, until)
	}
	// Exclude corrections in the sum (corrections are adjustments applied separately).
	query += ` AND correction_of IS NULL`

	var total int64
	err := r.pool.QueryRow(ctx, query, args...).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("summing usage ledger by agent: %w", err)
	}
	return total, nil
}

func scanUsageLedgerRecord(row pgx.Row) (*domain.UsageLedgerRecord, error) {
	r := &domain.UsageLedgerRecord{}
	err := row.Scan(&r.ID, &r.AgentPubkey, &r.TaskID, &r.ResourceType, &r.Amount,
		&r.RecordedAt, &r.RecordedBy, &r.Signature, &r.CorrectionOf, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func scanUsageLedgerRecords(rows pgx.Rows) ([]domain.UsageLedgerRecord, error) {
	var records []domain.UsageLedgerRecord
	for rows.Next() {
		var r domain.UsageLedgerRecord
		err := rows.Scan(&r.ID, &r.AgentPubkey, &r.TaskID, &r.ResourceType, &r.Amount,
			&r.RecordedAt, &r.RecordedBy, &r.Signature, &r.CorrectionOf, &r.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("scanning usage ledger record: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}
