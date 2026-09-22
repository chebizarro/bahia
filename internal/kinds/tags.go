package kinds

// CASControlStateTag* are the shared tag keys for canonical control-state
// projection producers and their relay subscription consumers. They live
// outside kinds.go because that file is a numeric event-kind catalog audited
// by the nostrmigration manifest test.
const (
	CASControlStateTagD         = "d"
	CASControlStateTagDomain    = "domain"
	CASControlStateTagSchema    = "schema"
	CASControlStateTagEntity    = "entity"
	VirtualizationDomain        = "virtualization"
	VirtualizationStateSchema   = "bahia.state.virtualization.v1"
	VirtualizationAuditSchema   = "bahia.audit.virtualization.v1"
	VirtualizationTagOrg        = "org"
	VirtualizationTagGeneration = "generation"
	VirtualizationTagSequence   = "sequence"
	VirtualizationTagClass      = "lifecycle_class"
	VirtualizationTagJournal    = "journal"
)
