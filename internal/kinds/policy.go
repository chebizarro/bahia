package kinds

// IsRequestKind returns true for legacy Bahia command kinds that are kept only
// for migration/inventory code. Production subscribers and sidecar reads must
// use ContextVM/NIP-17 intent transport instead of these kind-number commands.
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

// IsCanonicalObservableKind returns true for Bahia runtime kinds that remain
// production-readable after the Nostr-native migration boundary.
func IsCanonicalObservableKind(kind int) bool {
	switch kind {
	case CASControlState, CASAudit, NIP38Status,
		ContextVMServerAnnouncement, ContextVMToolsList, ContextVMResourcesList,
		ContextVMResourceTemplatesList, ContextVMPromptsList,
		RelaySetDiscovery, NIP65RelayList, SBOMAttestation,
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
