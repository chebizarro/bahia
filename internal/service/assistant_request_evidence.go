package service

import (
	"context"
	"errors"

	"github.com/openagentsinc/bahia/internal/domain"
)

// AssistantIntentRequestEvidenceResolver proves submission only from the
// durable in-process intent processor marker. An ephemeral ContextVM request
// is never reconstructed by asking a relay for its event ID.
type AssistantIntentRequestEvidenceResolver struct {
	IntentEvidence func(name, actor, key, eventID string) (*domain.AsyncToolReceipt, error)
}

var _ AssistantRequestEvidenceResolver = (*AssistantIntentRequestEvidenceResolver)(nil)

func (r *AssistantIntentRequestEvidenceResolver) ResolveAssistantRequestEvidence(_ context.Context, requestEventID string, x domain.AssistantExecution, w domain.AssistantWorkItem) (*domain.AsyncToolReceipt, error) {
	if r == nil || r.IntentEvidence == nil {
		return nil, errors.New("assistant intent evidence resolver is not configured")
	}
	return r.IntentEvidence(w.ToolName, x.OperatorPubkey, assistantExpectedIdempotencyKey(x, w), requestEventID)
}
