package controlplane

import (
	"context"
	"errors"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
)

const (
	intentDelegationVersionML  = "bahia.ml.delegation.v1"
	intentDelegationVersionLLM = "bahia.llm.delegation.v1"

	intentDelegationCapabilityMLRollback  = "ml:rollback"
	intentDelegationCapabilityLLMDeploy   = "llm:deploy"
	intentDelegationCapabilityLLMRollback = "llm:rollback"
)

type intentDelegationRecord struct {
	Version          string `json:"version"`
	RequesterPubkey  string `json:"requester_pubkey"`
	RequestEventID   string `json:"request_event_id"`
	RequestEventKind int    `json:"request_event_kind"`
	TenantID         string `json:"tenant_id,omitempty"`
	Capability       string `json:"capability"`
	ServicePubkey    string `json:"service_pubkey"`
}

var (
	errIntentDelegationNoEvent   = errors.New("signed request event is required")
	errIntentDelegationNoPubkey  = errors.New("signed request pubkey is required")
	errIntentDelegationUntrusted = errors.New("signer is not entitled to claim delegation")
	errIntentDelegationSelf      = errors.New("Bahia service signer cannot supply requester authority")
)

// authorizeIntentDelegation turns a requested_by claim into a verifiable
// delegation record. The signer stays authoritative: ServicePubkey is always
// the event signer, RequesterPubkey is the claimed human, and a claim is only
// accepted from a trusted signer that is not claiming itself.
func authorizeIntentDelegation(event *nostr.Event, claimedHuman, version, capability string, trustedSigner bool) (*intentDelegationRecord, error) {
	claimedHuman = strings.TrimSpace(claimedHuman)
	if claimedHuman == "" {
		return nil, nil
	}
	if event == nil {
		return nil, errIntentDelegationNoEvent
	}
	if event.PubKey == (nostr.PubKey{}) {
		return nil, errIntentDelegationNoPubkey
	}
	signer := event.PubKey.Hex()
	if !trustedSigner {
		return nil, errIntentDelegationUntrusted
	}
	if claimedHuman == signer {
		return nil, errIntentDelegationSelf
	}
	return &intentDelegationRecord{
		Version:          version,
		RequesterPubkey:  claimedHuman,
		RequestEventID:   event.ID.Hex(),
		RequestEventKind: int(event.Kind),
		Capability:       capability,
		ServicePubkey:    signer,
	}, nil
}

func (r *Reactor) delegationTenantID(ctx context.Context, envID uuid.UUID) string {
	if r == nil || r.registry == nil || envID == uuid.Nil {
		return ""
	}
	env, err := r.registry.GetEnvironment(ctx, envID)
	if err != nil || env == nil || env.OrgID == uuid.Nil {
		return ""
	}
	return env.OrgID.String()
}
