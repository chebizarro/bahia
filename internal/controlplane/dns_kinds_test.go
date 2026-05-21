package controlplane

import "testing"

func TestDNSKindConstantsUnique(t *testing.T) {
	kinds := map[string]int{
		"KindDNSZoneCreateRequest":      KindDNSZoneCreateRequest,
		"KindDNSPolicyApplyRequest":     KindDNSPolicyApplyRequest,
		"KindDNSRecordOverrideRequest":  KindDNSRecordOverrideRequest,
		"KindDNSDriftRemediateRequest":  KindDNSDriftRemediateRequest,
		"KindDNSBackendRegisterRequest": KindDNSBackendRegisterRequest,
		"KindDNSOperationStatus":        KindDNSOperationStatus,
		"KindDNSZoneCreateResult":       KindDNSZoneCreateResult,
		"KindDNSPolicyApplyResult":      KindDNSPolicyApplyResult,
		"KindDNSRecordOverrideResult":   KindDNSRecordOverrideResult,
		"KindDNSDriftRemediateResult":   KindDNSDriftRemediateResult,
		"KindDNSBackendRegisterResult":  KindDNSBackendRegisterResult,
	}
	seen := map[int]string{}
	for name, kind := range kinds {
		if previous, ok := seen[kind]; ok {
			t.Fatalf("%s collides with %s at kind %d", name, previous, kind)
		}
		seen[kind] = name
	}
	for name, kind := range kinds {
		switch kind {
		case KindDeployRequest, KindRollbackRequest, KindServiceAction, KindServiceCreate, KindEnvironmentCreate,
			KindDeploymentApproval, KindObservationSubmit, KindDriftRemediate, KindLLMRouteCreate, KindLLMReleaseRegister,
			KindLLMDeployRequest, KindLLMDeploymentApproval, KindLLMRollbackRequest, KindToolProvisionRequest,
			KindToolApprovalRequest, KindAdoptionScanRequest, KindAdoptionImportRequest, KindEncryptedRequest,
			KindServiceUpdate, KindServiceDelete, KindEnvironmentUpdate, KindEnvironmentDelete, KindArtifactRegister,
			KindPolicyCreate, KindPolicyUpdate, KindPolicyDelete, KindPolicyEvaluate, KindPackageRepositoryApply,
			KindPackageRepositoryDelete, KindPackagePublishIntent, KindPackagePromotionRequest, KindPackageYankRequest,
			KindPackageDriftDetect, KindDeploymentStatus, KindServiceStatus, KindActionStatus, KindLLMDeploymentStatus,
			KindToolProvisionStatus, KindAdoptionStatus, KindPackageStatus, KindEncryptedResult, KindDeploymentResult,
			KindActionResult, KindServiceCreateResult, KindEnvCreateResult, KindObservationResult, KindRemediationResult,
			KindLLMRouteCreateResult, KindLLMReleaseRegisterResult, KindLLMDeploymentResult, KindToolProvisionResult,
			KindToolApprovalResponse, KindAdoptionScanResult, KindAdoptionImportResult, KindPackageResult, KindPackageDriftEvent:
			t.Fatalf("%s collides with existing kind %d", name, kind)
		}
	}
}
