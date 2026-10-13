package controlplane

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// TrustSet resolves org authorization from relay membership and configured bootstrap owners.
//
// Resolution order for HasPermission(orgID, pubkey, permission):
//  1. If relay membership events exist for orgID, use them exclusively.
//  2. Else if pubkey is a bootstrapOwner for orgID, grant owner-level permission.
//
// Fleet operators (fleetOps) are never implicit org members. They authorize
// only fleet-scoped operations through FleetOperatorGate, unchanged from
// today's reactor gate.
//
// See docs/architecture/intents-and-authority.md.
type TrustSet struct {
	mu sync.RWMutex
	// Per-org membership from relay events.
	relay map[string]map[string]domain.Role // org-id → pubkey → role
	// Fleet-level operator pubkeys from config (not org members).
	fleetOps []string
	// Per-org bootstrap owners from config (used only when no relay members exist).
	bootstrapOwners map[string]string // org-id → pubkey
	logger          *zap.Logger
}

// TrustSetOption configures a TrustSet.
type TrustSetOption func(*TrustSet)

// WithBootstrapOwners sets the per-org bootstrap owner map from config.
func WithBootstrapOwners(owners map[string]string) TrustSetOption {
	return func(ts *TrustSet) {
		if len(owners) > 0 {
			ts.bootstrapOwners = make(map[string]string, len(owners))
			for orgID, pubkey := range owners {
				ts.bootstrapOwners[orgID] = pubkey
			}
		}
	}
}

// NewTrustSet creates a TrustSet. fleetOps are config authorized_pubkeys.
func NewTrustSet(fleetOps []string, logger *zap.Logger, opts ...TrustSetOption) *TrustSet {
	ts := &TrustSet{
		relay:           make(map[string]map[string]domain.Role),
		fleetOps:        append([]string(nil), fleetOps...),
		bootstrapOwners: make(map[string]string),
		logger:          logger,
	}
	for _, opt := range opts {
		opt(ts)
	}
	return ts
}

// HasPermission checks whether pubkey has permission in orgID. Returns true if
// authorized, false if not. See resolution order in the type doc.
func (ts *TrustSet) HasPermission(_ context.Context, orgID uuid.UUID, pubkey string, permission domain.Permission) bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	orgStr := orgID.String()

	// 1. Relay membership events (highest priority).
	if members, ok := ts.relay[orgStr]; ok && len(members) > 0 {
		role, found := members[pubkey]
		if !found {
			return false
		}
		return domain.RoleHasPermission(role, permission)
	}

	// 2. Bootstrap owners from config.
	if owner, ok := ts.bootstrapOwners[orgStr]; ok && owner == pubkey {
		// Bootstrap owners have owner-level permission.
		return domain.RoleHasPermission(domain.RoleOwner, permission)
	}

	return false
}

// RoleFor returns the role a pubkey holds in an org, or empty string if none.
func (ts *TrustSet) RoleFor(_ context.Context, orgID uuid.UUID, pubkey string) domain.Role {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	orgStr := orgID.String()

	// 1. Relay events.
	if members, ok := ts.relay[orgStr]; ok && len(members) > 0 {
		return members[pubkey] // zero value if absent
	}

	// 2. Bootstrap owners.
	if owner, ok := ts.bootstrapOwners[orgStr]; ok && owner == pubkey {
		return domain.RoleOwner
	}
	return ""
}

// AuthorPubkeys returns the deduplicated set of pubkeys the intent subscriber
// should include in its authors filter. This includes relay members,
// bootstrap owners, and fleet operators.
func (ts *TrustSet) AuthorPubkeys() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	seen := make(map[string]struct{})
	var pubkeys []string
	add := func(pk string) {
		if pk == "" {
			return
		}
		if _, ok := seen[pk]; ok {
			return
		}
		seen[pk] = struct{}{}
		pubkeys = append(pubkeys, pk)
	}

	// Fleet operators: they are in the authors filter so we can receive
	// their events (even though they are not org members, they may publish
	// intents that the processor then rejects for org-level permission).
	for _, pk := range ts.fleetOps {
		add(pk)
	}

	// Relay-sourced members.
	for _, members := range ts.relay {
		for pk := range members {
			add(pk)
		}
	}

	// Bootstrap owners.
	for _, pk := range ts.bootstrapOwners {
		add(pk)
	}

	return pubkeys
}

// IsKnownPrincipal reports whether pubkey appears in any trust source.
// Used to decide between silent drop (unknown) and rejection status (known
// but insufficient permission).
func (ts *TrustSet) IsKnownPrincipal(pubkey string) bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, pk := range ts.fleetOps {
		if pk == pubkey {
			return true
		}
	}
	for _, members := range ts.relay {
		if _, ok := members[pubkey]; ok {
			return true
		}
	}
	for _, pk := range ts.bootstrapOwners {
		if pk == pubkey {
			return true
		}
	}
	return false
}

// SetRelayMembers replaces the relay-sourced membership for an org.
// Updated from canonical member events.
func (ts *TrustSet) SetRelayMembers(orgID string, members map[string]domain.Role) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(members) == 0 {
		delete(ts.relay, orgID)
		return
	}
	cp := make(map[string]domain.Role, len(members))
	for k, v := range members {
		cp[k] = v
	}
	ts.relay[orgID] = cp
}

// FleetOps returns the fleet operator pubkeys.
func (ts *TrustSet) FleetOps() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]string(nil), ts.fleetOps...)
}

// BootstrapOwnerPubkeys returns the deduplicated set of bootstrap owner
// pubkeys from config. Used by the fleet OCK scope to include all
// configured operators in the key wrapping set.
func (ts *TrustSet) BootstrapOwnerPubkeys() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	pubkeys := make([]string, 0, len(ts.bootstrapOwners))
	seen := make(map[string]bool)
	for _, pk := range ts.bootstrapOwners {
		if pk != "" && !seen[pk] {
			seen[pk] = true
			pubkeys = append(pubkeys, pk)
		}
	}
	return pubkeys
}

// RelayMembersFor returns a copy of the relay-sourced membership map for an org,
// or nil if no relay members exist. Used by RelayMemberEventHandler for
// single-event merging.
func (ts *TrustSet) RelayMembersFor(orgID string) map[string]domain.Role {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	src, ok := ts.relay[orgID]
	if !ok || len(src) == 0 {
		return nil
	}
	cp := make(map[string]domain.Role, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp
}
