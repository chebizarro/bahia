package kinds

// IsRequestKind returns true for production-accepted Bahia request transport
// kinds after the startup migration boundary. Legacy kind-number commands are
// data-only migration inventory and must not be accepted by production runtime
// subscribers or sidecar policy.
func IsRequestKind(kind int) bool {
	switch kind {
	case ContextVMMessage, ContextVMGiftWrap, ContextVMEphemeralGiftWrap:
		return true
	default:
		return false
	}
}

// IsCanonicalObservableKind returns true for Bahia runtime kinds that remain
// production-readable after the Nostr-native migration boundary.
func IsCanonicalObservableKind(kind int) bool {
	switch kind {
	case CASControlState, CASAudit, NIP38Status,
		ContextVMServerAnnouncement, ContextVMToolsList, ContextVMResourcesList,
		ContextVMResourceTemplatesList, ContextVMPromptsList,
		RelaySetDiscovery, NIP65RelayList, NIP51DMRelayList, SBOMAttestation,
		BahiaIdentityDefinition, BahiaReplayCheckpoint, BahiaReadinessStatus:
		return true
	default:
		return false
	}
}

// IsBahiaProjectionKind returns true if the kind is a Bahia projection kind
// that should only be published by the service pubkey. After the migration
// boundary, production projections are canonical state/status/audit/discovery
// kinds; legacy per-domain projection constants remain migration-only.
func IsBahiaProjectionKind(kind int) bool {
	return IsCanonicalObservableKind(kind)
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

// IsAuthorScopedReadableRequestKind returns true if the kind is a legacy
// request kind that may be exposed in migration-only author-scoped reads.
// Production sidecar policy no longer exposes these kinds.
func IsAuthorScopedReadableRequestKind(kind int) bool {
	return false
}

// IsReadableKind returns true if the kind can be read from the sidecar relay.
// This includes canonical Bahia observable kinds and open interop kinds only.
func IsReadableKind(kind int) bool {
	return IsCanonicalObservableKind(kind) || IsOpenInteropKind(kind)
}

// AllRequestKinds returns all legacy Bahia request kinds for migration tools.
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

// AllStatusKinds returns canonical operational status kinds.
func AllStatusKinds() []int {
	return []int{NIP38Status}
}

// AllResultKinds returns canonical task/result kinds used by public compute flows.
func AllResultKinds() []int {
	return []int{}
}

// AllReadModelKinds returns canonical Bahia replaceable state/discovery kinds.
func AllReadModelKinds() []int {
	return []int{
		CASControlState,
		ContextVMServerAnnouncement,
		ContextVMToolsList,
		ContextVMResourcesList,
		ContextVMResourceTemplatesList,
		ContextVMPromptsList,
		RelaySetDiscovery,
		NIP65RelayList,
		NIP51DMRelayList,
		SBOMAttestation,
	}
}

// DNSRequestKinds returns all DNS request kinds.
func DNSRequestKinds() []int {
	return []int{
		DNSZoneCreateRequest, DNSPolicyApplyRequest, DNSRecordOverrideRequest,
		DNSDriftRemediateRequest, DNSBackendRegisterRequest,
	}
}

// DNSResultKinds returns canonical DNS result observable kinds.
func DNSResultKinds() []int {
	return []int{CASControlState, CASAudit, NIP38Status}
}

// DNSReadModelKinds returns canonical DNS read-model kinds.
func DNSReadModelKinds() []int {
	return []int{CASControlState}
}

// BackupRequestKinds returns all backup request kinds.
func BackupRequestKinds() []int {
	return []int{
		BackupRunRequest, BackupVerificationRequest, BackupRestoreRequest,
		BackupRestoreApproval, BackupRetentionEnforce, BackupRepositoryRegister,
		BackupPolicyApply, BackupRecipeApply, BackupDefinitionApply, BackupRepositoryProbe,
	}
}
