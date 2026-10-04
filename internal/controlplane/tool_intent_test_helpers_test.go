package controlplane

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type atomicToolApprovalRepo struct {
	*toolProvisioningRepoFake
	mu            sync.Mutex
	decisionCalls int
	applied       int
	approvalLogs  int
}

func newAtomicToolApprovalRepo(id uuid.UUID, status domain.ToolProvisionStatus) *atomicToolApprovalRepo {
	return &atomicToolApprovalRepo{toolProvisioningRepoFake: &toolProvisioningRepoFake{intent: &domain.ToolProvisionIntent{ID: id, Status: status}}}
}

func (r *atomicToolApprovalRepo) ApplyToolApprovalDecision(_ context.Context, id uuid.UUID, decision domain.ToolProvisionStatus, actorPubkey string, decidedAt time.Time) (*domain.ToolProvisionIntent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisionCalls++
	if r.intent == nil || r.intent.ID != id {
		return nil, repository.ErrNotFound
	}
	if r.intent.Status != domain.ToolProvisionStatusAwaitingApproval {
		return nil, repository.ErrConflict
	}
	copy := *r.intent
	copy.Status = decision
	if decision == domain.ToolProvisionStatusApproved {
		copy.ApprovedBy = actorPubkey
		copy.ApprovedAt = &decidedAt
	}
	r.intent = &copy
	r.applied++
	return &copy, nil
}

func (r *atomicToolApprovalRepo) LogApproval(context.Context, uuid.UUID, string, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvalLogs++
	return nil
}

func (r *atomicToolApprovalRepo) counts() (calls, applied, logs int, status domain.ToolProvisionStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decisionCalls, r.applied, r.approvalLogs, r.intent.Status
}

func toolApprovalEvent(t *testing.T, privateKey, requestID string, intentID uuid.UUID, action string) *nostr.Event {
	t.Helper()
	return makeContextVMEvent(t, privateKey, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q,"params":{"intent_id":%q,"action":%q,"reason":"operator reviewed"}}`, requestID, "tool/approval-response", intentID.String(), action))
}
