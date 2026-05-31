package kinds

// IsRequestKind returns true if the kind is a Bahia request kind that requires
// an authorized operator pubkey. These are command events sent to Bahia.
func IsRequestKind(kind int) bool {
	switch {
	// DNS requests (5941-5945)
	case kind >= DNSZoneCreateRequest && kind <= DNSBackendRegisterRequest:
		return true
	// Core control-plane requests (5961-5989)
	case kind >= DeployRequest && kind <= PolicyEvaluate:
		return kind != EncryptedRequest // 5980 is special
	// Package requests (5991-5996)
	case kind >= PackageRepositoryApply && kind <= PackageDriftDetect:
		return true
	// Worker requests (5997-6006)
	case kind >= WorkerCordonRequest && kind <= WorkerCleanupRequest:
		return true
	// ML requests (38390-38394)
	case kind >= MLRecipeRunRequest && kind <= MLModelImportRequest:
		return true
	// Backup requests (38400-38409)
	case kind >= BackupRunRequest && kind <= BackupRepositoryProbe:
		return true
	// Assistant requests (38420-38421)
	case kind == AssistantPromptRequest || kind == AssistantApproval:
		return true
	// Continuity requests (38430-38431)
	case kind == FailoverRequest || kind == RecoveryRequest:
		return true
	default:
		return false
	}
}

// IsBahiaProjectionKind returns true if the kind is a Bahia projection kind
// that should only be published by the service pubkey. These are read-model
// events emitted by Bahia.
func IsBahiaProjectionKind(kind int) bool {
	switch {
	// Relay set discovery
	case kind == RelaySetDiscovery:
		return true
	// SBOM kinds
	case kind == SBOMAttestation || kind == SBOMIndex:
		return true
	// DNS status (6941) and results (7941-7945)
	case kind == DNSOperationStatus:
		return true
	case kind >= DNSZoneCreateResult && kind <= DNSBackendRegisterResult:
		return true
	// Core status kinds (6961-6991)
	case kind >= DeploymentStatus && kind <= ActionStatus:
		return true
	case kind == LLMDeploymentStatus:
		return true
	case kind == ToolProvisionStatus:
		return true
	case kind == AdoptionStatus:
		return true
	case kind >= BackupRunStatus && kind <= BackupObservation:
		return true
	case kind == PackageStatus:
		return true
	case kind == WorkerStatus:
		return true
	// Core result kinds (7961-7997)
	case kind >= DeploymentResult && kind <= RemediationResult:
		return true
	case kind >= LLMRouteCreateResult && kind <= LLMDeploymentResult:
		return true
	case kind >= ToolProvisionResult && kind <= AdoptionImportResult:
		return true
	case kind >= PackageResult && kind <= PackageDriftEvent:
		return true
	case kind == WorkerResult:
		return true
	// Continuity observation/status kinds (30350-30353)
	case kind >= HeartbeatObservation && kind <= RecoveryProgress:
		return true
	// Backup attestation kinds (31310-31311)
	case kind == BackupRunAttestation || kind == BackupVerificationAttestation:
		return true
	// Continuity definition kinds (31400-31404)
	case kind >= ContinuityProfile && kind <= RecoveryWorkflow:
		return true
	// Replaceable registries (31961-31978)
	case kind >= ServiceState && kind <= DNSBackendState:
		return true
	// ML read-model kinds (31980-31989)
	case kind >= MLModelRegistry && kind <= MLRuntimeCapabilityProfile:
		return true
	// Backup read-model kinds (31991-31999)
	case kind >= BackupDefinitionRegistry && kind <= BackupRuntimeObservationState:
		return true
	// Worker state kinds (32000-32003)
	case kind >= WorkerState && kind <= WorkerEligibilityPreview:
		return true
	// Audit kinds (31000-31099)
	case kind >= AuditMin && kind <= AuditMax:
		return true
	// ML results (38395-38399)
	case kind >= MLRecipeRunResult && kind <= MLModelImportResult:
		return true
	// Backup results (38410-38419)
	case kind >= BackupRunResult && kind <= BackupRepositoryProbeResult:
		return true
	// Assistant status/result (38422-38423)
	case kind == AssistantStatus || kind == AssistantResult:
		return true
	// Assistant session (31990)
	case kind == AssistantSession:
		return true
	default:
		return false
	}
}

// IsOpenInteropKind returns true if the kind is an open interop kind that
// does not require authorization. These are events from external systems
// (Loom, Hive-CI) that interoperate with Bahia.
func IsOpenInteropKind(kind int) bool {
	switch kind {
	case LoomWorkerAdvertisement, LoomJobStatusUpdate, LoomJobResult, LoomJobCancellation:
		return true
	case HiveCIWorkflowRun, HiveCIWorkflowResult:
		return true
	default:
		return false
	}
}

// IsAuthorScopedReadableRequestKind returns true if the kind is a request kind
// that can be read when scoped to authorized author pubkeys. Most request kinds
// allow author-scoped reads except for encrypted requests.
func IsAuthorScopedReadableRequestKind(kind int) bool {
	return IsRequestKind(kind) && kind != EncryptedRequest
}

// IsReadableKind returns true if the kind can be read from the sidecar relay.
// This includes both Bahia projection kinds and open interop kinds.
func IsReadableKind(kind int) bool {
	return IsBahiaProjectionKind(kind) || IsOpenInteropKind(kind)
}

// AllRequestKinds returns all Bahia request kinds.
func AllRequestKinds() []int {
	return []int{
		// DNS requests
		DNSZoneCreateRequest, DNSPolicyApplyRequest, DNSRecordOverrideRequest,
		DNSDriftRemediateRequest, DNSBackendRegisterRequest,
		// Core requests
		DeployRequest, RollbackRequest, ServiceAction, ServiceCreate,
		EnvironmentCreate, DeploymentApproval, ObservationSubmit, DriftRemediate,
		LLMRouteCreate, LLMReleaseRegister, LLMDeployRequest, LLMDeploymentApproval,
		LLMRollbackRequest, ToolProvisionRequest, ToolApprovalRequest,
		AdoptionScanRequest, AdoptionImportRequest,
		ServiceUpdate, ServiceDelete, EnvironmentUpdate, EnvironmentDelete,
		ArtifactRegister, PolicyCreate, PolicyUpdate, PolicyDelete, PolicyEvaluate,
		// Package requests
		PackageRepositoryApply, PackageRepositoryDelete, PackagePublishIntent,
		PackagePromotionRequest, PackageYankRequest, PackageDriftDetect,
		// Worker requests
		WorkerCordonRequest, WorkerUncordonRequest, WorkerDrainRequest,
		WorkerUndrainRequest, WorkerMaintenanceEnter, WorkerMaintenanceExit,
		WorkerLabelsUpdate, WorkerPolicyApplyRequest, WorkloadPinRequest,
		WorkerCleanupRequest,
		// ML requests
		MLRecipeRunRequest, MLInferenceDeployRequest, MLInferenceDeploymentApproval,
		MLInferenceRollbackRequest, MLModelImportRequest,
		// Backup requests
		BackupRunRequest, BackupVerificationRequest, BackupRestoreRequest,
		BackupRestoreApproval, BackupRetentionEnforce, BackupRepositoryRegister,
		BackupPolicyApply, BackupRecipeApply, BackupDefinitionApply, BackupRepositoryProbe,
		// Assistant requests
		AssistantPromptRequest, AssistantApproval,
		// Continuity requests
		FailoverRequest, RecoveryRequest,
	}
}

// AllStatusKinds returns all Bahia status kinds.
func AllStatusKinds() []int {
	return []int{
		DNSOperationStatus,
		DeploymentStatus, ServiceStatus, ActionStatus, LLMDeploymentStatus,
		ToolProvisionStatus, AdoptionStatus,
		BackupRunStatus, BackupRestoreStatus, BackupVerificationStatus, BackupObservation,
		PackageStatus, WorkerStatus,
		AssistantStatus,
	}
}

// AllResultKinds returns all Bahia result kinds.
func AllResultKinds() []int {
	return []int{
		// DNS results
		DNSZoneCreateResult, DNSPolicyApplyResult, DNSRecordOverrideResult,
		DNSDriftRemediateResult, DNSBackendRegisterResult,
		// Core results
		DeploymentResult, ActionResult, ServiceCreateResult, EnvironmentCreateResult,
		ObservationResult, RemediationResult, LLMRouteCreateResult,
		LLMReleaseRegisterResult, LLMDeploymentResult, ToolProvisionResult,
		ToolApprovalResponse, AdoptionScanResult, AdoptionImportResult,
		EncryptedResult, PackageResult, PackageDriftEvent, WorkerResult,
		// ML results
		MLRecipeRunResult, MLInferenceDeployResult, MLInferenceApprovalResult,
		MLInferenceRollbackResult, MLModelImportResult,
		// Backup results
		BackupRunResult, BackupVerificationResult, BackupRestoreResult,
		BackupRestoreApprovalResult, BackupRetentionResult,
		BackupRepositoryRegisterResult, BackupPolicyApplyResult,
		BackupRecipeApplyResult, BackupDefinitionApplyResult, BackupRepositoryProbeResult,
		// Assistant result
		AssistantResult,
	}
}

// AllReadModelKinds returns all Bahia replaceable read-model kinds.
func AllReadModelKinds() []int {
	return []int{
		// Core registries
		ServiceState, ServiceRegistry, EnvironmentRegistry, LLMRouteRegistry,
		LLMRouteState, ArtifactRegistry, DeploymentIntentRegistry,
		DeploymentRunRegistry, BuildRegistry, PolicyRegistry,
		PackageRepositoryRegistry, PackageArtifactRegistry, PackagePromotionRegistry,
		SystemDiscovery,
		// DNS
		DNSZoneState, DNSEndpointState, DNSPolicyState, DNSBackendState,
		// ML
		MLModelRegistry, MLModelVersionRegistry, MLDatasetRegistry, MLRecipeRegistry,
		MLRecipeRunState, MLInferenceEndpointRegistry, MLInferenceEndpointState,
		MLEvaluationExperimentState, MLArtifactProvenanceGraph, MLRuntimeCapabilityProfile,
		// Backup
		BackupDefinitionRegistry, BackupPolicyRegistry, BackupRepositoryRegistry,
		BackupRetentionRegistry, BackupRecipeRegistry, BackupRunState,
		BackupVerificationState, BackupRestoreState, BackupRuntimeObservationState,
		// Worker
		WorkerState, WorkerAssignmentState, WorkerDrainStatus, WorkerEligibilityPreview,
		// Legacy worker (for compatibility)
		LegacyWorkerState, LegacyWorkerAssignmentState, LegacyWorkerDrainStatus,
		LegacyWorkerEligibilityPreview,
		// Assistant
		AssistantSession,
		// Continuity
		ContinuityProfile, FailoverPolicy, StandbyNodeDefinition,
		ReplicationPolicy, RecoveryWorkflow,
		// Continuity runtime
		HeartbeatObservation, ContinuityStatus, DegradedModeActivation, RecoveryProgress,
		// SBOM
		SBOMAttestation, SBOMIndex,
		// System
		RelaySetDiscovery,
	}
}

// DNSRequestKinds returns all DNS request kinds.
func DNSRequestKinds() []int {
	return []int{
		DNSZoneCreateRequest, DNSPolicyApplyRequest, DNSRecordOverrideRequest,
		DNSDriftRemediateRequest, DNSBackendRegisterRequest,
	}
}

// DNSResultKinds returns all DNS result kinds.
func DNSResultKinds() []int {
	return []int{
		DNSZoneCreateResult, DNSPolicyApplyResult, DNSRecordOverrideResult,
		DNSDriftRemediateResult, DNSBackendRegisterResult,
	}
}

// DNSReadModelKinds returns all DNS read-model kinds.
func DNSReadModelKinds() []int {
	return []int{
		DNSZoneState, DNSEndpointState, DNSPolicyState, DNSBackendState,
	}
}

// BackupRequestKinds returns all backup request kinds.
func BackupRequestKinds() []int {
	return []int{
		BackupRunRequest, BackupVerificationRequest, BackupRestoreRequest,
		BackupRestoreApproval, BackupRetentionEnforce, BackupRepositoryRegister,
		BackupPolicyApply, BackupRecipeApply, BackupDefinitionApply, BackupRepositoryProbe,
	}
}
