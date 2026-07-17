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

// Note: This implements repository.PaymentRecordRepository from interfaces.go

// PgPaymentRepository is a PostgreSQL implementation of PaymentRepository.
type PgPaymentRepository struct {
	pool *pgxpool.Pool
}

// NewPgPaymentRepository creates a new PgPaymentRepository.
func NewPgPaymentRepository(pool *pgxpool.Pool) *PgPaymentRepository {
	return &PgPaymentRepository{pool: pool}
}

// NewPgPaymentRecordRepository is an alias for NewPgPaymentRepository.
// Used for consistency with other repository naming patterns.
func NewPgPaymentRecordRepository(pool *pgxpool.Pool) *PgPaymentRepository {
	return NewPgPaymentRepository(pool)
}

// Create inserts a new payment record.
func (r *PgPaymentRepository) Create(ctx context.Context, p *domain.PaymentRecord) error {
	if p == nil {
		return fmt.Errorf("creating payment: payment is nil")
	}
	metadataJSON, err := json.Marshal(p.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling payment metadata: %w", err)
	}

	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	now := time.Now()
	p.CreatedAt = now
	p.UpdatedAt = now

	_, err = r.pool.Exec(ctx, `
		INSERT INTO payments (
			id, deployment_run_id, worker_pubkey, mint_url, amount_sats,
			token_hash, direction, status, error_message, metadata,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, p.ID, p.DeploymentRunID, p.WorkerPubkey, p.MintURL, p.AmountSats,
		p.TokenHash, string(p.Direction), string(p.Status), p.ErrorMessage, metadataJSON,
		p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating payment: %w", err)
	}
	return nil
}

// GetByID retrieves a payment record by ID.
func (r *PgPaymentRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.PaymentRecord, error) {
	p := &domain.PaymentRecord{}
	var metadataJSON []byte
	var direction, status string

	err := r.pool.QueryRow(ctx, `
		SELECT id, deployment_run_id, worker_pubkey, mint_url, amount_sats,
			token_hash, direction, status, error_message, metadata,
			created_at, updated_at
		FROM payments WHERE id = $1
	`, id).Scan(
		&p.ID, &p.DeploymentRunID, &p.WorkerPubkey, &p.MintURL, &p.AmountSats,
		&p.TokenHash, &direction, &status, &p.ErrorMessage, &metadataJSON,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying payment: %w", err)
	}

	p.Direction = domain.PaymentDirection(direction)
	p.Status = domain.PaymentStatus(status)
	_ = json.Unmarshal(metadataJSON, &p.Metadata)

	return p, nil
}

// UpdateStatus updates the status and optional error message of a payment.
func (r *PgPaymentRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.PaymentStatus, errorMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE payments SET status = $1, error_message = $2, updated_at = now()
		WHERE id = $3
	`, string(status), errorMsg, id)
	if err != nil {
		return fmt.Errorf("updating payment status: %w", err)
	}
	return nil
}

// ListByRun returns all payments for a deployment run.
func (r *PgPaymentRepository) ListByRun(ctx context.Context, runID uuid.UUID) ([]domain.PaymentRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, deployment_run_id, worker_pubkey, mint_url, amount_sats,
			token_hash, direction, status, error_message, metadata,
			created_at, updated_at
		FROM payments WHERE deployment_run_id = $1
		ORDER BY created_at ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("listing payments: %w", err)
	}
	defer rows.Close()

	return scanPayments(rows)
}

// GetByTokenHash retrieves a payment by its token hash (for idempotency).
func (r *PgPaymentRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.PaymentRecord, error) {
	p := &domain.PaymentRecord{}
	var metadataJSON []byte
	var direction, status string

	err := r.pool.QueryRow(ctx, `
		SELECT id, deployment_run_id, worker_pubkey, mint_url, amount_sats,
			token_hash, direction, status, error_message, metadata,
			created_at, updated_at
		FROM payments WHERE token_hash = $1
	`, tokenHash).Scan(
		&p.ID, &p.DeploymentRunID, &p.WorkerPubkey, &p.MintURL, &p.AmountSats,
		&p.TokenHash, &direction, &status, &p.ErrorMessage, &metadataJSON,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying payment by token hash: %w", err)
	}

	p.Direction = domain.PaymentDirection(direction)
	p.Status = domain.PaymentStatus(status)
	_ = json.Unmarshal(metadataJSON, &p.Metadata)

	return p, nil
}

// ListByWorker returns recent payments to a specific worker.
func (r *PgPaymentRepository) ListByWorker(ctx context.Context, workerPubkey string, limit int) ([]domain.PaymentRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, deployment_run_id, worker_pubkey, mint_url, amount_sats,
			token_hash, direction, status, error_message, metadata,
			created_at, updated_at
		FROM payments WHERE worker_pubkey = $1
		ORDER BY created_at DESC LIMIT $2
	`, workerPubkey, limit)
	if err != nil {
		return nil, fmt.Errorf("listing payments: %w", err)
	}
	defer rows.Close()

	return scanPayments(rows)
}

// GetTotalPaidToWorker returns the total sats paid to a worker (successful payments only).
func (r *PgPaymentRepository) GetTotalPaidToWorker(ctx context.Context, workerPubkey string) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sats), 0) FROM payments
		WHERE worker_pubkey = $1 AND direction = $2 AND status IN ($3, $4)
	`, workerPubkey, string(domain.PaymentDirectionPayment),
		string(domain.PaymentStatusSent), string(domain.PaymentStatusRedeemed)).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying total paid: %w", err)
	}
	return total, nil
}

// scanPayments scans multiple payment rows.
func scanPayments(rows pgx.Rows) ([]domain.PaymentRecord, error) {
	var payments []domain.PaymentRecord
	for rows.Next() {
		var p domain.PaymentRecord
		var metadataJSON []byte
		var direction, status string

		if err := rows.Scan(
			&p.ID, &p.DeploymentRunID, &p.WorkerPubkey, &p.MintURL, &p.AmountSats,
			&p.TokenHash, &direction, &status, &p.ErrorMessage, &metadataJSON,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning payment: %w", err)
		}

		p.Direction = domain.PaymentDirection(direction)
		p.Status = domain.PaymentStatus(status)
		_ = json.Unmarshal(metadataJSON, &p.Metadata)

		payments = append(payments, p)
	}
	return payments, rows.Err()
}
