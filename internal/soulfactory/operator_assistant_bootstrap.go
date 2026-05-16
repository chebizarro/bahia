package soulfactory

import (
	"context"
	"fmt"
	"time"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
)

const OperatorAssistantAgentID = "bahia-operator-assistant"

var OperatorAssistantAllowedKinds = []int{
	controlplane.KindDeployRequest,
	controlplane.KindRollbackRequest,
	controlplane.KindLLMDeployRequest,
	controlplane.KindLLMDeploymentApproval,
	controlplane.KindLLMRollbackRequest,
	controlplane.KindMLInferenceDeployRequest,
	controlplane.KindMLInferenceDeploymentApproval,
	controlplane.KindMLInferenceRollbackRequest,
}

type OperatorAssistantIdentity struct {
	AgentID string `json:"agent_id"`
	Pubkey  string `json:"pubkey"`
	Npub    string `json:"npub"`
}

// EnsureOperatorAssistantSoul ensures the managed operator assistant identity exists.
// Signet finding (2026-05-16): internal/adapters/signet/client.go exposes SignAs,
// which can sign arbitrary nostr.Event values for provisioned agents. Phase 1 still
// uses the service-signed fallback for downstream command events and attaches
// ["agent", "bahia-operator-assistant"] for attribution; the soul key is identity
// metadata until arbitrary-kind bunker signing is validated end-to-end.
func EnsureOperatorAssistantSoul(ctx context.Context, reactor *Reactor) (*OperatorAssistantIdentity, error) {
	if reactor == nil {
		return nil, fmt.Errorf("soulfactory reactor is not configured")
	}
	if soul, err := reactor.GetSoul(ctx, OperatorAssistantAgentID); err != nil {
		return nil, fmt.Errorf("lookup operator assistant soul: %w", err)
	} else if soul != nil && soul.NostrPubkey != "" {
		return &OperatorAssistantIdentity{AgentID: soul.AgentID, Pubkey: soul.NostrPubkey, Npub: soul.NostrNpub}, nil
	}
	if reactor.signer == nil {
		return nil, fmt.Errorf("soulfactory signer is not configured")
	}
	pubkey, npub, _, err := reactor.signer.ProvisionAgent(ctx, OperatorAssistantAgentID, OperatorAssistantAllowedKinds)
	if err != nil {
		return nil, fmt.Errorf("provision operator assistant soul: %w", err)
	}
	_ = &domain.AgentSoul{AgentID: OperatorAssistantAgentID, Name: "Bahia Operator Assistant", Purpose: "Event-native operational assistant", Tier: domain.SoulTierLightweight, Status: domain.SoulStatusActive, NostrPubkey: pubkey, NostrNpub: npub, AllowedKinds: append([]int{}, OperatorAssistantAllowedKinds...), CreatedAt: time.Now()}
	return &OperatorAssistantIdentity{AgentID: OperatorAssistantAgentID, Pubkey: pubkey, Npub: npub}, nil
}
