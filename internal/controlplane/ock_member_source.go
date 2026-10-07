package controlplane

import (
	"context"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
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
// For the special "fleet" scope, it returns fleet operators and bootstrap
// owners from the TrustSet. For real org IDs it uses
// relay-sourced members first (highest priority), falling back to Postgres.
func (s *TrustSetMemberSource) OrgMemberPubkeys(ctx context.Context, orgID string) ([]string, error) {
	// Fleet scope: return fleet operators + bootstrap owners so the
	// fleet OCK is readable by everyone who can access the web dashboard.
	if orgID == kinds.FleetOCKScope {
		return s.fleetOpsPubkeys(), nil
	}

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

// fleetOpsPubkeys returns the deduplicated set of pubkeys that should be
// able to read fleet-scoped OCK content: fleet operators (authorized_pubkeys)
// plus bootstrap owners from config.
func (s *TrustSetMemberSource) fleetOpsPubkeys() []string {
	seen := make(map[string]bool)
	var pubkeys []string
	add := func(pk string) {
		if pk != "" && !seen[pk] {
			seen[pk] = true
			pubkeys = append(pubkeys, pk)
		}
	}

	// Fleet operators from config.
	for _, pk := range s.trustSet.FleetOps() {
		add(pk)
	}
	// Bootstrap owners — they are fleet principals even if they're also
	// org-scoped; every bootstrap owner should be able to read fleet state.
	for _, pk := range s.trustSet.BootstrapOwnerPubkeys() {
		add(pk)
	}
	return pubkeys
}
