package kinds

// CASControlStateTag* are the shared tag keys for canonical control-state
// projection producers and their relay subscription consumers. They live
// outside kinds.go because that file is a numeric event-kind catalog audited
// by the nostrmigration manifest test.
const (
	CASControlStateTagD      = "d"
	CASControlStateTagDomain = "domain"
	CASControlStateTagSchema = "schema"
	CASControlStateTagEntity = "entity"
	// CASControlStateTagLegacyKind names the per-family catalog kind a
	// canonical 30900 record was projected from; consumers route on it.
	CASControlStateTagLegacyKind = "legacy_kind"
	// CASControlStateTagDeleted is "true" on tombstones and "false" on live
	// records. Consumers must compare its value, not test for its presence.
	CASControlStateTagDeleted = "deleted"
	// CASControlStateSchema is the schema tag stamped on every record the
	// projector publishes through its canonical 30900 envelope.
	CASControlStateSchema = "bahia.cp-state.v1"

	// DNSDomain is the domain tag value for projected DNS state.
	DNSDomain = "dns"
	// DNS*Topic are the single-letter "t" tags the projector stamps on live
	// and tombstone DNS state. NIP-01 relays index single-letter tags, so
	// consumers scope REQs with these rather than #domain/#schema.
	DNSZoneTopic     = "dns-zone"
	DNSEndpointTopic = "dns-endpoint"
	DNSPolicyTopic   = "dns-policy"
	DNSBackendTopic  = "dns-backend"

	VirtualizationDomain        = "virtualization"
	VirtualizationStateSchema   = "bahia.state.virtualization.v1"
	VirtualizationAuditSchema   = "bahia.audit.virtualization.v1"
	VirtualizationTagOrg        = "org"
	VirtualizationTagGeneration = "generation"
	VirtualizationTagSequence   = "sequence"
	VirtualizationTagClass      = "lifecycle_class"
	VirtualizationTagJournal    = "journal"
)

// Worker cp-state contract (bahia-irsry.9.2): worker records are canonical
// 30900 cp-state (schema CASControlStateSchema, legacy_kind CPStateFamilyWorker*,
// deleted) in domain WorkerDomain, and live records and tombstones both carry
// the family's single-letter "t" topic so REQs scope on #t, not #domain/#schema.
const (
	WorkerDomain           = "worker"
	WorkerStateTopic       = "worker-state"
	WorkerAssignmentTopic  = "worker-assignment"
	WorkerDrainTopic       = "worker-drain"
	WorkerEligibilityTopic = "worker-eligibility"
	WorkerCleanupTopic     = "worker-cleanup"
)
