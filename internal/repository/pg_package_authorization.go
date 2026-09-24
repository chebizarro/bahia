package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PackageAuthorizationStore is authoritative admission state, not a relay
// projection. Claims and consumed approvals must survive projection rebuilds.
type PackageAuthorizationStore interface {
	ClaimPackageRequest(context.Context, PackageRequestClaim) (*PackageRequestClaim, bool, error)
	CompletePackageRequest(context.Context, string) error
	CreatePackageApproval(context.Context, PackageApproval) error
	ConsumePackageApproval(context.Context, uuid.UUID, string, string, string, []string) (string, error)
}

type PackageRequestClaim struct {
	Requester, Method, Token, EventID, Fingerprint string
	Completed                                      bool
}

type PackageApproval struct {
	ID                                             uuid.UUID
	Requester, Approver, Method, PlanHash, EventID string
	CreatedAt, ExpiresAt                           time.Time
}

var ErrPackageApprovalInvalid = errors.New("package approval is missing, expired, consumed, or does not match the plan")

func (r *PgPackageControlPlaneRepository) ClaimPackageRequest(ctx context.Context, c PackageRequestClaim) (*PackageRequestClaim, bool, error) {
	result, err := r.pool.Exec(ctx, `INSERT INTO package_request_claims (requester, method, token, event_id, fingerprint)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, c.Requester, c.Method, c.Token, c.EventID, c.Fingerprint)
	if err != nil {
		return nil, false, fmt.Errorf("claim package request: %w", err)
	}
	if result.RowsAffected() == 1 {
		return &c, true, nil
	}
	var existing PackageRequestClaim
	err = r.pool.QueryRow(ctx, `SELECT requester, method, token, event_id, fingerprint, completed FROM package_request_claims
		WHERE requester=$1 AND method=$2 AND token=$3`, c.Requester, c.Method, c.Token).
		Scan(&existing.Requester, &existing.Method, &existing.Token, &existing.EventID, &existing.Fingerprint, &existing.Completed)
	if err != nil {
		return nil, false, fmt.Errorf("load package claim: %w", err)
	}
	if existing.Fingerprint != c.Fingerprint {
		return nil, false, fmt.Errorf("package idempotency key reused with different parameters")
	}
	return &existing, false, nil
}

func (r *PgPackageControlPlaneRepository) CompletePackageRequest(ctx context.Context, eventID string) error {
	result, err := r.pool.Exec(ctx, `UPDATE package_request_claims SET completed=true WHERE event_id=$1`, eventID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("package claim not found")
	}
	return nil
}

func (r *PgPackageControlPlaneRepository) CreatePackageApproval(ctx context.Context, a PackageApproval) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO package_approvals
		(id, requester, approver, method, plan_hash, event_id, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, a.ID, a.Requester, a.Approver, a.Method, a.PlanHash, a.EventID, a.CreatedAt, a.ExpiresAt)
	return err
}

// The conditional UPDATE is the single-use boundary, including across replicas.
// A failed or interrupted backend attempt never restores an approval.
func (r *PgPackageControlPlaneRepository) ConsumePackageApproval(ctx context.Context, id uuid.UUID, requester, method, hash string, approvers []string) (string, error) {
	var approver string
	err := r.pool.QueryRow(ctx, `UPDATE package_approvals SET consumed_at=clock_timestamp()
		WHERE id=$1 AND requester=$2 AND method=$3 AND plan_hash=$4
		AND approver=ANY($5::text[]) AND approver<>requester AND consumed_at IS NULL
		AND created_at<=clock_timestamp() AND expires_at>clock_timestamp()
		RETURNING approver`, id, requester, method, hash, approvers).Scan(&approver)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPackageApprovalInvalid
	}
	return approver, err
}
