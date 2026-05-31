// Package kinds provides the single source of truth for all Bahia Nostr event kinds.
// All other packages should import kinds from here rather than defining their own.
//
// Kind families:
//   - 5xxx: Request kinds (operator → Bahia)
//   - 6xxx: Status kinds (Bahia → operator, progress updates)
//   - 7xxx: Result kinds (Bahia → operator, terminal results)
//   - 30xxx: NIP-51 style parameterized lists
//   - 31xxx: Replaceable read-model projections
//   - 32xxx: Worker state projections
//   - 38xxx: AI/ML and backup command/result kinds
package kinds

// =============================================================================
// DNS Control-Plane Kinds (5941-5945, 6941, 7941-7945)
// =============================================================================

const (
	DNSZoneCreateRequest      = 5941
	DNSPolicyApplyRequest     = 5942
	DNSRecordOverrideRequest  = 5943
	DNSDriftRemediateRequest  = 5944
	DNSBackendRegisterRequest = 5945

	DNSOperationStatus = 6941

	DNSZoneCreateResult      = 7941
	DNSPolicyApplyResult     = 7942
	DNSRecordOverrideResult  = 7943
	DNSDriftRemediateResult  = 7944
	DNSBackendRegisterResult = 7945
)

// =============================================================================
// Core Control-Plane Request Kinds (5961-5989)
// =============================================================================

const (
	DeployRequest            = 5961
	RollbackRequest          = 5962
	ServiceAction            = 5963
	ServiceCreate            = 5964
	EnvironmentCreate        = 5965
	DeploymentApproval       = 5966
	ObservationSubmit        = 5967
	DriftRemediate           = 5968
	LLMRouteCreate           = 5971
	LLMReleaseRegister       = 5972
	LLMDeployRequest         = 5973
	LLMDeploymentApproval    = 5974
	LLMRollbackRequest       = 5975
	ToolProvisionRequest     = 5976
	ToolApprovalRequest      = 5977
	AdoptionScanRequest      = 5978
	AdoptionImportRequest    = 5979
	EncryptedRequest         = 5980 // Browser → Bahia encrypted request
	ServiceUpdate            = 5981
	ServiceDelete            = 5982
	EnvironmentUpdate        = 5983
	EnvironmentDelete        = 5984
	ArtifactRegister         = 5985
	PolicyCreate             = 5986
	PolicyUpdate             = 5987
	PolicyDelete             = 5988
	PolicyEvaluate           = 5989
)

// =============================================================================
// Package Control-Plane Request Kinds (5991-5996)
// =============================================================================

const (
	PackageRepositoryApply   = 5991
	PackageRepositoryDelete  = 5992
	PackagePublishIntent     = 5993
	PackagePromotionRequest  = 5994
	PackageYankRequest       = 5995
	PackageDriftDetect       = 5996
)

// =============================================================================
// Worker Control-Plane Request Kinds (5997-6006)
// =============================================================================

const (
	WorkerCordonRequest      = 5997
	WorkerUncordonRequest    = 5998
	WorkerDrainRequest       = 5999
	WorkerUndrainRequest     = 6000
	WorkerMaintenanceEnter   = 6001
	WorkerMaintenanceExit    = 6002
	WorkerLabelsUpdate       = 6003
	WorkerPolicyApplyRequest = 6004
	WorkloadPinRequest       = 6005
	WorkerCleanupRequest     = 6006
)

// =============================================================================
// Core Control-Plane Status Kinds (6961-6997)
// =============================================================================

const (
	DeploymentStatus    = 6961
	ServiceStatus       = 6962
	ActionStatus        = 6963
	LLMDeploymentStatus = 6973
	ToolProvisionStatus = 6976
	AdoptionStatus      = 6978
	BackupRunStatus          = 6981
	BackupRestoreStatus      = 6982
	BackupVerificationStatus = 6983
	BackupObservation        = 6984
	PackageStatus       = 6991
	WorkerStatus        = 6997
)

// =============================================================================
// Core Control-Plane Result Kinds (7961-7997)
// =============================================================================

const (
	DeploymentResult         = 7961
	ActionResult             = 7962
	ServiceCreateResult      = 7963
	EnvironmentCreateResult  = 7964
	ObservationResult        = 7965
	RemediationResult        = 7966
	LLMRouteCreateResult     = 7971
	LLMReleaseRegisterResult = 7972
	LLMDeploymentResult      = 7973
	ToolProvisionResult      = 7976
	ToolApprovalResponse     = 7977
	AdoptionScanResult       = 7978
	AdoptionImportResult     = 7979
	EncryptedResult          = 7980 // Bahia → Browser encrypted result
	PackageResult            = 7991
	PackageDriftEvent        = 7992
	WorkerResult             = 7997
)

// =============================================================================
// Interop Kinds (Loom, Hive-CI)
// =============================================================================

const (
	LoomWorkerAdvertisement = 10100
	LoomJobStatusUpdate     = 30100
	LoomJobResult           = 5101
	LoomJobCancellation     = 5102

	HiveCIWorkflowRun    = 5401
	HiveCIWorkflowResult = 5402
)

// =============================================================================
// NIP-65 and Discovery Kinds
// =============================================================================

const (
	NIP65RelayList        = 10002
	RelaySetDiscovery     = 30002
)

// =============================================================================
// Continuity Fabric Kinds (30350-30353, 31400-31404, 38430-38431)
// =============================================================================

const (
	HeartbeatObservation   = 30350
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
// SBOM / Attestation Kinds
// =============================================================================

const (
	SBOMAttestation = 30078
	SBOMIndex       = 30079
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
	BuildRegistered           = 31000
	ArtifactRegistered        = 31001
	DeploymentCreated         = 31002
	DeploymentComplete        = 31003
	DriftDetected             = 31004
	Observation               = 31005
	ServiceRegistryAudit      = 31006
	EnvironmentRegistryAudit  = 31007
	StateChangedAudit         = 31008
	RuntimeActionAudit        = 31009
	ReconcileAudit            = 31010
	AdoptionAudit             = 31011
	DeploymentApprovalAudit   = 31012
	DeploymentRunAudit        = 31013
	LLMRouteRegistryAudit     = 31014
	LLMReleaseRegisteredAudit = 31015
	LLMDeploymentAudit        = 31016
	LLMRunAudit               = 31017
	LLMRouteStateAudit        = 31018
	LLMGatewayAudit           = 31019
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
// Nostr Signature Kind
// =============================================================================

const (
	NostrSignature = 31200
)

// =============================================================================
// Backup Attestation Kinds
// =============================================================================

const (
	BackupRunAttestation          = 31310
	BackupVerificationAttestation = 31311
)

// =============================================================================
// Replaceable Read-Model Registry Kinds (31961-31999)
// =============================================================================

const (
	ServiceState              = 31961
	ServiceRegistry           = 31962
	EnvironmentRegistry       = 31963
	LLMRouteRegistry          = 31964
	LLMRouteState             = 31965
	ArtifactRegistry          = 31966
	DeploymentIntentRegistry  = 31967
	DeploymentRunRegistry     = 31968
	BuildRegistry             = 31969
	PolicyRegistry            = 31970
	PackageRepositoryRegistry = 31971
	PackageArtifactRegistry   = 31972
	PackagePromotionRegistry  = 31973
	SystemDiscovery           = 31974

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
// Worker State Kinds (32000-32003)
// =============================================================================

const (
	WorkerState              = 32000
	WorkerAssignmentState    = 32001
	WorkerDrainStatus        = 32002
	WorkerEligibilityPreview = 32003
)

// =============================================================================
// Legacy Worker State Kinds (deprecated, for mixed-version compatibility)
// =============================================================================

const (
	LegacyWorkerState              = 31974 // Conflicts with SystemDiscovery; use WorkerState
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
	AssistantStatus        = 38422
	AssistantResult        = 38423
)

// =============================================================================
// NIP-98 HTTP Auth Kind
// =============================================================================

const (
	HTTPAuth = 27235
)
