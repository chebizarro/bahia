package nostr

import "github.com/openagentsinc/bahia/internal/kinds"

// Security cp-state kind aliases.
const (
	KindSecurityFindingRecord       = kinds.SecurityFindingRecord
	KindSecurityScheduleRecord      = kinds.SecurityScheduleRecord
	KindSecurityFindingDetailRecord = kinds.SecurityFindingDetailRecord
	// Target and run are 30900-only families with no catalog kind.
	KindSecurityTargetRecord = int(kinds.CPStateFamilySecurityTarget)
	KindSecurityRunRecord    = int(kinds.CPStateFamilySecurityRun)
)

// KindAdoptionBindingRecord is the adoption binding family: a 30900-only
// family with no catalog kind.
const KindAdoptionBindingRecord = int(kinds.CPStateFamilyAdoptionBinding)
