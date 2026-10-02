package nostr

import "github.com/openagentsinc/bahia/internal/kinds"

// Org replaceable read-model kinds are aliases to the canonical internal/kinds catalog.
// Phase 3 Wave 5 O1.
const (
	KindOrgRegistry       = kinds.OrgRegistry
	KindOrgMemberRegistry = kinds.OrgMemberRegistry
	KindOrgInviteRegistry = kinds.OrgInviteRegistry
)
