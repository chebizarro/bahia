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

// CPStateTopic* are the single-letter "t" topics the projector's
// controlStateEnvelope stamps on every canonical cp-state record (bahia-irsry.9.3,
// audit A-27). The value is "<domain>-<entity>" of the record family, the same
// rule the DNS*Topic values above follow. NIP-01 relays index single-letter
// tags only, so consumers scope 30900 REQs with #t instead of #domain/#schema.
// The worker families' topics ("worker-state", "worker-assignment", ...) are
// declared with the worker contract (bahia-irsry.9.2), not here.
const (
	CPStateTopicServiceState             = "service-state"
	CPStateTopicServiceRegistry          = "service-registry"
	CPStateTopicEnvironmentRegistry      = "environment-registry"
	CPStateTopicLLMRoute                 = "llm-route"
	CPStateTopicLLMState                 = "llm-state"
	CPStateTopicArtifactRegistry         = "artifact-registry"
	CPStateTopicDeploymentIntent         = "deployment-intent"
	CPStateTopicDeploymentRun            = "deployment-run"
	CPStateTopicBuildRegistry            = "build-registry"
	CPStateTopicPolicyRegistry           = "policy-registry"
	CPStateTopicPackageRepository        = "package-repository"
	CPStateTopicPackageArtifact          = "package-artifact"
	CPStateTopicPackagePromotion         = "package-promotion"
	CPStateTopicMLModel                  = "ml-model"
	CPStateTopicMLModelVersion           = "ml-model-version"
	CPStateTopicMLDataset                = "ml-dataset"
	CPStateTopicMLRecipe                 = "ml-recipe"
	CPStateTopicMLRecipeRun              = "ml-recipe-run"
	CPStateTopicMLEndpoint               = "ml-endpoint"
	CPStateTopicMLEndpointState          = "ml-endpoint-state"
	CPStateTopicMLEvaluation             = "ml-evaluation"
	CPStateTopicMLProvenance             = "ml-provenance"
	CPStateTopicMLRuntimeCapability      = "ml-runtime-capability"
	CPStateTopicBackupDefinition         = "backup-definition"
	CPStateTopicBackupPolicy             = "backup-policy"
	CPStateTopicBackupRepository         = "backup-repository"
	CPStateTopicBackupRetention          = "backup-retention"
	CPStateTopicBackupRecipe             = "backup-recipe"
	CPStateTopicBackupRun                = "backup-run"
	CPStateTopicBackupVerification       = "backup-verification"
	CPStateTopicBackupRestore            = "backup-restore"
	CPStateTopicBackupRuntimeObservation = "backup-runtime"
)

// CPAudit* describe the projector's append-only audit facts: regular kind
// 4903 events (never addressable, no d tag) correlated to the entity's state
// coordinate and deduplicated per source fact (audit C-16).
const (
	// CPAuditTopic is the single-letter "t" topic on every projected audit fact.
	CPAuditTopic = "cp-audit"
	// CPAuditTagState carries the audited entity's cp-state coordinate.
	CPAuditTagState = "state"
	// CPAuditTagFact is the deterministic source-fact id: republishing the same
	// fact reuses it, so publishers and consumers can drop the duplicate.
	CPAuditTagFact = "fact"
)
