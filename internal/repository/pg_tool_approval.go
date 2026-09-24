package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ApplyToolApprovalDecision atomically consumes a pending tool approval. The
// status predicate is the compare-and-set guard: a decided intent cannot be
// approved or rejected again, including by a concurrent responder.
func (r *PgToolProvisioningRepository) ApplyToolApprovalDecision(ctx context.Context, id uuid.UUID, decision domain.ToolProvisionStatus, actorPubkey string, decidedAt time.Time) (*domain.ToolProvisionIntent, error) {
	if decision != domain.ToolProvisionStatusApproved && decision != domain.ToolProvisionStatusRejected {
		return nil, fmt.Errorf("invalid tool approval decision %q: %w", decision, domain.ErrInvalidValue)
	}
	actorPubkey = strings.TrimSpace(actorPubkey)
	if actorPubkey == "" || decidedAt.IsZero() {
		return nil, fmt.Errorf("tool approval actor and decision time are required: %w", domain.ErrInvalidValue)
	}

	var approvedBy any
	var approvedAt any
	if decision == domain.ToolProvisionStatusApproved {
		approvedBy = actorPubkey
		approvedAt = decidedAt.UTC()
	}
	intent, err := r.scanIntent(r.pool.QueryRow(ctx, `
		UPDATE tool_provision_intents
		SET status = $2,
			approved_by = $3,
			approved_at = $4
		WHERE id = $1 AND status = $5
		RETURNING `+toolIntentColumns,
		id, decision, approvedBy, approvedAt, domain.ToolProvisionStatusAwaitingApproval))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("tool approval intent %s is not awaiting approval: %w", id, ErrConflict)
		}
		return nil, fmt.Errorf("applying tool approval decision: %w", err)
	}
	return intent, nil
}
