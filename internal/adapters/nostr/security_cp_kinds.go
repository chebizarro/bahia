package nostr

import "github.com/openagentsinc/bahia/internal/kinds"

// Security cp-state kind aliases (bahia-irsry.60).
const (
	KindSecurityFindingRecord       = kinds.SecurityFindingRecord
	KindSecurityScheduleRecord      = kinds.SecurityScheduleRecord
	KindSecurityFindingDetailRecord = kinds.SecurityFindingDetailRecord
	// Target and run are 30900-only families with no catalog kind (audit B-32).
	KindSecurityTargetRecord = int(kinds.CPStateFamilySecurityTarget)
	KindSecurityRunRecord    = int(kinds.CPStateFamilySecurityRun)
)
