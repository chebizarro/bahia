package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

// TrustSetMemberSource implements OCKMemberSource by reading from TrustSet
// (relay-first) with Postgres fallback. This ensures OCK wrapping works
// even without a database, as long as relay-sourced membership events exist.
type TrustSetMemberSource struct {
	trustSet *TrustSet
	members  repository.OrgMemberRepository // may be nil (no-DB mode)
}

// NewTrustSetMemberSource creates a member source backed by TrustSet.
func NewTrustSetMemberSource(trustSet *TrustSet, members repository.OrgMemberRepository) *TrustSetMemberSource {
	return &TrustSetMemberSource{trustSet: trustSet, members: members}
}

// OrgMemberPubkeys returns the pubkeys of all current members for an org.
// Uses relay-sourced members first (highest priority in TrustSet), falling
// back to Postgres when no relay data is available.
func (s *TrustSetMemberSource) OrgMemberPubkeys(ctx context.Context, orgID string) ([]string, error) {
	// Try relay source first (event-derived, DB-independent).
	relayMembers := s.trustSet.RelayMembersFor(orgID)
	if len(relayMembers) > 0 {
		pubkeys := make([]string, 0, len(relayMembers))
		for pk := range relayMembers {
			pubkeys = append(pubkeys, pk)
		}
		return pubkeys, nil
	}

	// Fall back to Postgres if available.
	if s.members != nil {
		orgUUID, err := uuid.Parse(orgID)
		if err != nil {
			return nil, nil // Not a valid UUID — return empty set.
		}
		members, err := s.members.ListByOrg(ctx, orgUUID)
		if err != nil {
			return nil, nil // Postgres unavailable — return empty set.
		}
		pubkeys := make([]string, 0, len(members))
		for _, m := range members {
			pubkeys = append(pubkeys, m.Pubkey)
		}
		return pubkeys, nil
	}

	return nil, nil // No member source available.
}
