// Package kinds provides the single source of truth for Bahia Nostr event kind
// numbers used by runtime code, migration code, and historical transforms.
//
// Production runtime code must prefer Bahia's canonical Nostr-native policy:
//   - ContextVM kind 25910 for mutation intents, optionally wrapped with
//     CEP-4/NIP-59 kind 1059 or 21059.
//   - NIP-38 kind 30315 for operational status.
//   - Kind 30900 for canonical control-plane state projections.
//   - Kind 4903 for audit facts and attestations.
//   - ContextVM discovery kinds 11316-11320 and NIP-51 relay sets kind 30002
//     for bootstrap and capability discovery.
//   - NIP-65 kind 10002 for advisory service relay preferences.
//   - NIP-51 kind 10050 for explicitly configured DM receive relay lists.
//   - NIP-78 kind 30078 for app-specific data.
//
// Legacy request/status/result/read-model constants remain here so migration,
// fixtures, and fail-closed compatibility tests can identify old events. They
// are not permission to publish or subscribe to those kinds in production
// runtime paths. See docs/nostr-event-implementation-guide.md before adding or
// reusing any event kind.
package kinds

import cascadia "git.sharegap.net/cascadia/cascadia-go"

// Domain-specific names are semantic aliases only. Fleet transport and
// observability use the canonical Cascadia kinds; no retired wire numbers
// remain available to production code.
const (
	DNSZoneCreateRequest      = ContextVMMessage
	DNSPolicyApplyRequest     = ContextVMMessage
	DNSRecordOverrideRequest  = ContextVMMessage
	DNSDriftRemediateRequest  = ContextVMMessage
	DNSBackendRegisterRequest = ContextVMMessage
	DeployRequest             = ContextVMMessage
	RollbackRequest           = ContextVMMessage
	ServiceAction             = ContextVMMessage
	ServiceCreate             = ContextVMMessage
	EnvironmentCreate         = ContextVMMessage
	DeploymentApproval        = ContextVMMessage
	ObservationSubmit         = ContextVMMessage
	DriftRemediate            = ContextVMMessage
	LLMRouteCreate            = ContextVMMessage
	LLMReleaseRegister        = ContextVMMessage
	LLMDeployRequest          = ContextVMMessage
	LLMDeploymentApproval     = ContextVMMessage
	LLMRollbackRequest        = ContextVMMessage
	ToolProvisionRequest      = ContextVMMessage
	ToolApprovalRequest       = ContextVMMessage
	AdoptionScanRequest       = ContextVMMessage
	AdoptionImportRequest     = ContextVMMessage
	ServiceUpdate             = ContextVMMessage
	ServiceDelete             = ContextVMMessage
	EnvironmentUpdate         = ContextVMMessage
	EnvironmentDelete         = ContextVMMessage
	ArtifactRegister          = ContextVMMessage
	PolicyCreate              = ContextVMMessage
	PolicyUpdate              = ContextVMMessage
	PolicyDelete              = ContextVMMessage
	PolicyEvaluate            = ContextVMMessage
	PackageRepositoryApply    = ContextVMMessage
	PackageRepositoryDelete   = ContextVMMessage
	PackagePublishIntent      = ContextVMMessage
	PackagePromotionRequest   = ContextVMMessage
	PackageYankRequest        = ContextVMMessage
	PackageDriftDetect        = ContextVMMessage
	WorkerCordonRequest       = ContextVMMessage
	WorkerUncordonRequest     = ContextVMMessage
	WorkerDrainRequest        = ContextVMMessage
	WorkerUndrainRequest      = ContextVMMessage
	WorkerMaintenanceEnter    = ContextVMMessage
	WorkerMaintenanceExit     = ContextVMMessage
	WorkerLabelsUpdate        = ContextVMMessage
	WorkerPolicyApplyRequest  = ContextVMMessage
	WorkloadPinRequest        = ContextVMMessage
	HiveCIWorkflowRun         = ContextVMMessage
	CmdBuildRegister          = ContextVMMessage
	CmdArtifactRegister       = ContextVMMessage
	CmdIntentCreate           = ContextVMMessage
	CmdIntentApprove          = ContextVMMessage
	CmdIntentReject           = ContextVMMessage
	CmdRollbackRequest        = ContextVMMessage
)
const (
	DNSOperationStatus       = NIP38Status
	DeploymentStatus         = NIP38Status
	ServiceStatus            = NIP38Status
	ActionStatus             = NIP38Status
	LLMDeploymentStatus      = NIP38Status
	ToolProvisionStatus      = NIP38Status
	AdoptionStatus           = NIP38Status
	BackupRunStatus          = NIP38Status
	BackupRestoreStatus      = NIP38Status
	BackupVerificationStatus = NIP38Status
	BackupObservation        = NIP38Status
	PackageStatus            = NIP38Status
	WorkerStatus             = NIP38Status
	HiveCIWorkflowResult     = NIP38Status
	AssistantStatus          = NIP38Status
)
const (
	DNSZoneCreateResult           = CASAudit
	DNSPolicyApplyResult          = CASAudit
	DNSRecordOverrideResult       = CASAudit
	DNSDriftRemediateResult       = CASAudit
	DNSBackendRegisterResult      = CASAudit
	DeploymentResult              = CASAudit
	ActionResult                  = CASAudit
	ServiceCreateResult           = CASAudit
	EnvironmentCreateResult       = CASAudit
	ObservationResult             = CASAudit
	RemediationResult             = CASAudit
	LLMRouteCreateResult          = CASAudit
	LLMReleaseRegisterResult      = CASAudit
	LLMDeploymentResult           = CASAudit
	ToolProvisionResult           = CASAudit
	ToolApprovalResponse          = CASAudit
	AdoptionScanResult            = CASAudit
	AdoptionImportResult          = CASAudit
	PackageResult                 = CASAudit
	PackageDriftEvent             = CASAudit
	WorkerResult                  = CASAudit
	BackupRunAttestation          = CASAudit
	BackupVerificationAttestation = CASAudit
	AssistantResult               = CASAudit
)
const (
	// Internal projection selectors are never serialized as event kinds. The
	// projector maps each selector to kind 30900 plus domain/schema tags.
	ServiceState = -(iota + 1)
	ServiceRegistry
	EnvironmentRegistry
	LLMRouteRegistry
	LLMRouteState
	ArtifactRegistry
	DeploymentIntentRegistry
	DeploymentRunRegistry
	BuildRegistry
	PolicyRegistry
	PackageRepositoryRegistry
	PackageArtifactRegistry
	PackagePromotionRegistry
	SystemDiscovery
	WorkerState
	WorkerAssignmentState
	WorkerDrainStatus
	WorkerEligibilityPreview
	LegacyWorkerState
)

// =============================================================================
// DNS Control-Plane Kinds (canonical, canonical, canonical)
// =============================================================================

const ()

// =============================================================================
// Core Control-Plane Request Kinds (canonical)
// =============================================================================

const (
	EncryptedRequest = 5980 // Browser → Bahia encrypted request
)

// =============================================================================
// Package Control-Plane Request Kinds (canonical)
// =============================================================================

const ()

// =============================================================================
// Worker Control-Plane Request Kinds (canonical)
// =============================================================================

const (
	WorkerCleanupRequest = 6006
)

// =============================================================================
// Core Control-Plane Status Kinds (canonical)
// =============================================================================

const ()

// =============================================================================
// Core Control-Plane Result Kinds (canonical)
// =============================================================================

const (
	EncryptedResult = 7980 // Bahia → Browser encrypted result
)

// =============================================================================
// Canonical Cascadia Observable Kinds
// =============================================================================

const (
	CASAudit            = cascadia.CAS_AUDIT
	NIP38Status         = cascadia.NIP38_USER_STATUS
	AssistantTranscript = cascadia.CAS_AGENT_HEARTBEAT
	CASControlState     = cascadia.CAS_CP_STATE
)

// =============================================================================
// Interop Kinds (Loom, Hive-CI, NIP-34, SoulFactory)
// =============================================================================

const (
	LoomWorkerAdvertisement = cascadia.CAS_WORKER_AD
	LoomJobResult           = 5101

	NIP22Comment = 1111

	NIP34UserGraspList          = 10317
	NIP34Patch                  = 1617
	NIP34PullRequest            = 1618
	NIP34PullRequestUpdate      = 1619
	NIP34Issue                  = 1621
	NIP34StatusOpen             = 1630
	NIP34StatusAppliedOrMerged  = 1631
	NIP34StatusClosed           = 1632
	NIP34StatusDraft            = 1633
	NIP34RepositoryAnnouncement = 30617
	NIP34RepositoryState        = 30618

	SoulFactoryAction              = cascadia.CAS_INTENT
	SoulFactoryActionLegacyResult  = cascadia.CAS_AUDIT
	SoulFactoryProvisioningRequest = cascadia.CAS_INTENT
	SoulFactoryProvisioningStatus  = cascadia.NIP38_USER_STATUS
	SoulFactoryProvisioningResult  = cascadia.CAS_AUDIT
	SoulFactoryRuntimeCapability   = cascadia.CAS_AGENT_CAPABILITY
	SoulFactoryRuntimeControl      = cascadia.CAS_INTENT
	SoulFactoryRuntimeResult       = cascadia.CAS_AUDIT
	SoulFactoryTemplate            = cascadia.CAS_CP_STATE
	SoulFactoryAgentSoul           = cascadia.CAS_CP_STATE
	SoulFactoryDraft               = cascadia.CAS_CP_STATE
)

// =============================================================================
// ContextVM Transport and Discovery Kinds
// =============================================================================

const (
	ContextVMMessage               = cascadia.CAS_INTENT
	ContextVMGiftWrap              = cascadia.NIP59_GIFT_WRAP
	ContextVMEphemeralGiftWrap     = cascadia.NIP59_EPHEMERAL_GIFT_WRAP
	ContextVMServerAnnouncement    = cascadia.CTXVM_SERVER_ANNOUNCEMENT
	ContextVMToolsList             = cascadia.CTXVM_TOOLS_ANNOUNCEMENT
	ContextVMResourcesList         = cascadia.CTXVM_RESOURCES_ANNOUNCEMENT
	ContextVMResourceTemplatesList = cascadia.CTXVM_RESOURCE_TEMPLATES_ANNOUNCEMENT
	ContextVMPromptsList           = cascadia.CTXVM_PROMPTS_ANNOUNCEMENT
)

// =============================================================================
// NIP-65 and Discovery Kinds
// =============================================================================

const (
	NIP65RelayList    = 10002
	NIP51DMRelayList  = 10050
	RelaySetDiscovery = 30002
)

// =============================================================================
// Continuity Fabric Kinds (30315, 30351-30353, 31400-31404, 38430-38431)
// =============================================================================

const (
	// HeartbeatObservation is a semantic alias for NIP-38 operational status kind 30315.
	// Continuity heartbeat observations are disambiguated with #domain=continuity
	// plus heartbeat schema/d/worker tags; 30350 is not a production heartbeat kind.
	HeartbeatObservation   = cascadia.NIP38_USER_STATUS
	ContinuityStatus       = 30351
	DegradedModeActivation = 30352
	RecoveryProgress       = 30353

	ContinuityProfile     = 31400
	FailoverPolicy        = 31401
	StandbyNodeDefinition = 31402
	ReplicationPolicy     = 31403
	RecoveryWorkflow      = 31404

	FailoverRequest = 38430
	RecoveryRequest = 38431
)

// =============================================================================
// SBOM Reference and Availability Kinds
// =============================================================================

const (
	SBOMReference        = 30078
	SBOMAvailabilityList = 30004
	LegacySBOMIndex      = 30079

	SBOMAttestation = SBOMReference
	SBOMIndex       = LegacySBOMIndex
)

// =============================================================================
// Bahia System Kinds
// =============================================================================

const (
	BahiaReadinessStatus    = 30360
	BahiaIdentityDefinition = 31410
	BahiaReplayCheckpoint   = 31411
)

// =============================================================================
// Audit Event Kinds (31000-31099)
// =============================================================================

const (
	BuildRegistered              = 31000
	ArtifactRegistered           = 31001
	DeploymentCreated            = 31002
	DeploymentComplete           = 31003
	DriftDetected                = 31004
	Observation                  = 31005
	ServiceRegistryAudit         = 31006
	EnvironmentRegistryAudit     = 31007
	StateChangedAudit            = 31008
	RuntimeActionAudit           = 31009
	ReconcileAudit               = 31010
	AdoptionAudit                = 31011
	DeploymentApprovalAudit      = 31012
	DeploymentRunAudit           = 31013
	LLMRouteRegistryAudit        = 31014
	LLMReleaseRegisteredAudit    = 31015
	LLMDeploymentAudit           = 31016
	LLMRunAudit                  = 31017
	LLMRouteStateAudit           = 31018
	LLMGatewayAudit              = 31019
	DNSZoneSyncedAudit           = 31020
	DNSRecordChangedAudit        = 31021
	DNSDriftDetectedAudit        = 31022
	DNSEndpointRegisteredAudit   = 31023
	DNSEndpointDeregisteredAudit = 31024

	// Audit kind range bounds
	AuditMin = 31000
	AuditMax = 31099
)

// =============================================================================
// Deprecated Legacy Command Kinds (canonical)
// =============================================================================

const ()

// =============================================================================
// Nostr Signature Kind
// =============================================================================

const (
	NostrSignature = 31200
)

// =============================================================================
// Backup Attestation Kinds
// =============================================================================

const ()

// =============================================================================
// Replaceable Read-Model Registry Kinds (canonical)
// =============================================================================

const (
	DNSZoneState     = 31975
	DNSEndpointState = 31976
	DNSPolicyState   = 31977
	DNSBackendState  = 31978

	// ML Read-Model Kinds (31980-31989)
	MLModelRegistry             = 31980
	MLModelVersionRegistry      = 31981
	MLDatasetRegistry           = 31982
	MLRecipeRegistry            = 31983
	MLRecipeRunState            = 31984
	MLInferenceEndpointRegistry = 31985
	MLInferenceEndpointState    = 31986
	MLEvaluationExperimentState = 31987
	MLArtifactProvenanceGraph   = 31988
	MLRuntimeCapabilityProfile  = 31989

	// Assistant Session
	AssistantSession = 31990

	// Backup Read-Model Kinds (31991-31999)
	BackupDefinitionRegistry      = 31991
	BackupPolicyRegistry          = 31992
	BackupRepositoryRegistry      = 31993
	BackupRetentionRegistry       = 31994
	BackupRecipeRegistry          = 31995
	BackupRunState                = 31996
	BackupVerificationState       = 31997
	BackupRestoreState            = 31998
	BackupRuntimeObservationState = 31999
)

// =============================================================================
// Worker State Kinds (canonical)
// =============================================================================

const ()

// =============================================================================
// Legacy Worker State Kinds (deprecated, for mixed-version compatibility)
// =============================================================================

const (
	LegacyWorkerAssignmentState    = 31991 // Conflicts with BackupDefinitionRegistry
	LegacyWorkerDrainStatus        = 31992
	LegacyWorkerEligibilityPreview = 31993
)

// =============================================================================
// FIPS Overlay Kind
// =============================================================================

const (
	FIPSOverlayAdvert = 37195
)

// =============================================================================
// AI/ML Command/Result Kinds (38390-38399)
// =============================================================================

const (
	MLRecipeRunRequest            = 38390
	MLInferenceDeployRequest      = 38391
	MLInferenceDeploymentApproval = 38392
	MLInferenceRollbackRequest    = 38393
	MLModelImportRequest          = 38394
	MLRecipeRunResult             = 38395
	MLInferenceDeployResult       = 38396
	MLInferenceApprovalResult     = 38397
	MLInferenceRollbackResult     = 38398
	MLModelImportResult           = 38399
)

// =============================================================================
// Backup Command/Result Kinds (38400-38419)
// =============================================================================

const (
	BackupRunRequest               = 38400
	BackupVerificationRequest      = 38401
	BackupRestoreRequest           = 38402
	BackupRestoreApproval          = 38403
	BackupRetentionEnforce         = 38404
	BackupRepositoryRegister       = 38405
	BackupPolicyApply              = 38406
	BackupRecipeApply              = 38407
	BackupDefinitionApply          = 38408
	BackupRepositoryProbe          = 38409
	BackupRunResult                = 38410
	BackupVerificationResult       = 38411
	BackupRestoreResult            = 38412
	BackupRestoreApprovalResult    = 38413
	BackupRetentionResult          = 38414
	BackupRepositoryRegisterResult = 38415
	BackupPolicyApplyResult        = 38416
	BackupRecipeApplyResult        = 38417
	BackupDefinitionApplyResult    = 38418
	BackupRepositoryProbeResult    = 38419
)

// =============================================================================
// Operator Assistant Kinds (38420-38423)
// =============================================================================

const (
	AssistantPromptRequest = 38420
	AssistantApproval      = 38421
)

// =============================================================================
// NIP-23 Long-Form Content Kinds
// =============================================================================

const (
	LongFormContent = 30023
	LongFormDraft   = 30024
)

// =============================================================================
// NIP-98 HTTP Auth Kind
// =============================================================================

const (
	HTTPAuth = 27235
)
