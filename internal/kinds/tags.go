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

// Worker cp-state contract: worker records are canonical
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

// Assistant and relay-settings topics. NIP-01 relays index
// single-letter tags only, so these records carry a "t" topic and REQs scope on
// #t (or on an exact #d coordinate) rather than #schema, #domain or #session.
const (
	// AssistantTranscriptTopic is on every 30316 transcript message.
	AssistantTranscriptTopic = "assistant-transcript"
	// AssistantTranscriptSessionTopicPrefix prefixes the per-session topic
	// every transcript message also carries (AssistantTranscriptSessionTopic),
	// so a session replay is an indexed REQ.
	AssistantTranscriptSessionTopicPrefix = "assistant-transcript:"
	// AssistantStatusTopic is on every 30315 assistant status.
	AssistantStatusTopic = "assistant-status"
	// RelaySettingsTopic is on the relay-settings operator policy (30900).
	RelaySettingsTopic = "relay-settings"
)

// AssistantTranscriptSessionTopic is the "t" topic scoping one session's
// transcript messages.
func AssistantTranscriptSessionTopic(sessionID string) string {
	return AssistantTranscriptSessionTopicPrefix + sessionID
}

// CPStateTopic* are the single-letter "t" topics the projector's
// controlStateEnvelope stamps on every canonical cp-state record (
// ). The value is "<domain>-<entity>" of the record family, the same
// rule the DNS*Topic values above follow. NIP-01 relays index single-letter
// tags only, so consumers scope 30900 REQs with #t instead of #domain/#schema.
// The worker families' topics ("worker-state", "worker-assignment",...) are
// declared with the worker contract, not here.
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
	CPStateTopicManagedInstanceHealth    = "runtime-instance-health"
	CPStateTopicRouteCanary              = "route-canary"
	CPStateTopicSoulRuntimePolicy        = "soul-factory-runtime-policy"
	CPStateTopicSoulFactorySagaRun       = "soul-factory-saga-run"
	CPStateTopicSoulFactoryAdapterLedger = "soul-factory-adapter-ledger"
	// CPStateTopicOperatorAllowlist is the fleet-OCK encrypted operator
	// allowlist family (CPStateFamilyOperatorAllowlist).
	CPStateTopicOperatorAllowlist = "operator-allowlist"
	CPStateTopicBlossomAdmin      = "blossom-admin"
	CPStateTopicBlossomBlob       = "blossom-blob"

	// Secret and notification channel state topics ( N1).
	CPStateTopicSecretRegistry              = "secret-registry"
	CPStateTopicNotificationChannelRegistry = "notification-channel"
)

// CPAudit* describe the projector's append-only audit facts: regular kind
// 4903 events (never addressable, no d tag) correlated to the entity's state
// coordinate and deduplicated per source fact.
const (
	// CPAuditTopic is the single-letter "t" topic on every projected audit fact.
	CPAuditTopic = "cp-audit"
	// CPAuditTagState carries the audited entity's cp-state coordinate.
	CPAuditTagState = "state"
	// CPAuditTagFact is the deterministic source-fact id: republishing the same
	// fact reuses it, so publishers and consumers can drop the duplicate.
	CPAuditTagFact = "fact"
)

// Org cp-state topics ( O1).
const (
	CPStateTopicOrgRegistry       = "org-registry"
	CPStateTopicOrgMemberRegistry = "org-member"
	CPStateTopicOrgInviteRegistry = "org-invite"
	CPStateTopicOrgKeyEnvelope    = "org-key-envelope"
)

// Assistant session-state topic. NIP-01 relays index
// single-letter tags only, so the recovery subscription and web session
// filter scope on #t instead of #schema.
const AssistantSessionTopic = "assistant-session"

// Security and SBOM observable topics. These records are
// published outside the cp-state envelope (the security scanner and SBOM
// publisher sign them directly), so they carry their own "t" topics rather
// than inheriting one from controlStateEnvelope.
// Continuity heartbeat observation topic. The heartbeat
// producer stamps this on 30315 observations so the web client can scope
// with #t instead of #domain.
const ContinuityHeartbeatTopic = "continuity-heartbeat"

const (
	SecurityScanStatusTopic = "security-scan-status"
	SecuritySummaryTopic    = "security-summary"
	SecurityFindingsTopic   = "security-findings"
	SecurityAuditTopic      = "security-audit"
	SBOMReferenceTopic      = "sbom-reference"
	SBOMAvailabilityTopic   = "sbom-availability"
)

// Payment and security cp-state topics. These records are
// published through controlStateEnvelope as confidential cp-state (OCK-
// encrypted) so only org members and the daemon can decrypt them.
const (
	CPStateTopicPaymentRecord         = "payment-record"
	CPStateTopicSecurityFinding       = "security-finding"
	CPStateTopicSecuritySchedule      = "security-schedule"
	CPStateTopicSecurityFindingDetail = "security-finding-detail"
	CPStateTopicSecurityTarget        = "security-target"
	CPStateTopicSecurityRun           = "security-run"
	// CPStateTopicAdoptionBinding is the adoption binding family.
	CPStateTopicAdoptionBinding = "adoption-binding"

	// Hive-CI execution state, fleet-OCK encrypted.
	CPStateTopicHiveCIPolicy     = "hiveci-policy"
	CPStateTopicHiveCIResult     = "hiveci-result"
	CPStateTopicHiveCIInitiation = "hiveci-initiation"
	CPStateTopicHiveCIRelease    = "hiveci-release"

	CPStateTopicLLMRelease         = "llm-release"
	CPStateTopicArtifactSignature  = "artifact-signature"
	CPStateTopicArtifactSBOM       = "artifact-sbom"
	CPStateTopicSBOMPackage        = "artifact-sbom-package"
	CPStateTopicRuntimeObservation = "runtime-observation"
)

// F74b confidential fleet cp-state topics.
const (
	CPStateTopicPackageIntent       = "package-intent"
	CPStateTopicToolProvisionIntent = "tool-provision-intent"
	CPStateTopicToolDenylist        = "tool-denylist"
	CPStateTopicToolProfile         = "tool-profile"
	CPStateTopicNotificationLog     = "notification-log"
)
