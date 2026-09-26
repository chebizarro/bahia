package service

import (
	"context"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantTurnEngine is the only execution owner for batch and iterative
// proposals. Its implementation and production wiring belong to later items.
type AssistantTurnEngine interface {
	StartTurn(context.Context, AssistantTurnStartRequest) (AssistantTurnResult, error)
	Decide(context.Context, AssistantTurnDecisionRequest) (AssistantTurnResult, error)
	Cancel(context.Context, AssistantTurnCancellationRequest) (AssistantTurnResult, error)
	Recover(context.Context, AssistantExecutionReference) error
	Reconcile(context.Context, AssistantTurnReconciliationRequest) (AssistantTurnResult, error)
}

type AssistantTurnStartRequest struct {
	Prompt          domain.AssistantPromptRequest
	OperatorPubkey  string
	RequestEventID  string
	ExistingSession *domain.AssistantSessionV2
	DefaultWorkflow domain.AssistantWorkflow
}

type AssistantTurnDecisionRequest struct {
	Approval       domain.AssistantApprovalRequest
	OperatorPubkey string
	RequestEventID string
}

type AssistantTurnCancellationRequest struct {
	Cancellation   domain.AssistantCancellationRequest
	OperatorPubkey string
	RequestEventID string
}

type AssistantTurnReconciliationRequest struct {
	Reconciliation domain.AssistantReconciliationRequest
	OperatorPubkey string
	RequestEventID string
}

type AssistantExecutionReference struct {
	SessionID           string
	RunID               string
	CheckpointEventID   string
	LegacySourceEventID string
}

type AssistantTurnResult struct {
	Session           domain.AssistantSessionV2
	ExecutionRevision uint64
	Acknowledgment    string
	PendingEffects    int
}

// AssistantProposalKind is the exhaustive set of proposer outcomes.
type AssistantProposalKind string

const (
	AssistantProposalBatch         AssistantProposalKind = "batch"
	AssistantProposalCalls         AssistantProposalKind = "calls"
	AssistantProposalFinal         AssistantProposalKind = "final"
	AssistantProposalClarification AssistantProposalKind = "clarification"
	AssistantProposalBlocked       AssistantProposalKind = "blocked"
)

// AssistantProposal carries all calls from one iterative model response. A
// producer cannot dispatch; the engine checkpoints this whole sequence first.
type AssistantProposal struct {
	Kind   AssistantProposalKind
	Batch  *domain.AssistantPlan
	Calls  []domain.AssistantAgentToolCall
	Text   string
	Reason string
}

type AssistantProposalRequest struct {
	SessionID string
	RunID     string
	TurnID    string
	Prompt    string
	Scope     domain.AssistantCommandScope
}

type AssistantBatchProposer interface {
	ProposeBatch(context.Context, AssistantProposalRequest) (AssistantProposal, error)
}

type AssistantIterativeProposer interface {
	ProposeIterative(context.Context, AssistantProposalRequest) (AssistantProposal, error)
}
