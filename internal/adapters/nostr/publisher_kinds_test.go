package nostr

import "testing"

func TestWorkerPublisherKindConstantsUseNonCollidingCanonicalBlock(t *testing.T) {
	if KindSystemDiscovery != 31974 {
		t.Fatalf("KindSystemDiscovery must remain at kind 31974, got %d", KindSystemDiscovery)
	}
	workerReadModels := map[string]int{
		"KindWorkerState":              KindWorkerState,
		"KindWorkerAssignmentState":    KindWorkerAssignmentState,
		"KindWorkerDrainStatus":        KindWorkerDrainStatus,
		"KindWorkerEligibilityPreview": KindWorkerEligibilityPreview,
	}
	expected := map[string]int{
		"KindWorkerState":              32000,
		"KindWorkerAssignmentState":    32001,
		"KindWorkerDrainStatus":        32002,
		"KindWorkerEligibilityPreview": 32003,
	}
	seen := map[int]string{
		KindSystemDiscovery:               "KindSystemDiscovery",
		KindBackupDefinitionRegistry:      "KindBackupDefinitionRegistry",
		KindBackupPolicyRegistry:          "KindBackupPolicyRegistry",
		KindBackupRepositoryRegistry:      "KindBackupRepositoryRegistry",
		KindBackupRetentionRegistry:       "KindBackupRetentionRegistry",
		KindBackupRecipeRegistry:          "KindBackupRecipeRegistry",
		KindBackupRunState:                "KindBackupRunState",
		KindBackupVerificationState:       "KindBackupVerificationState",
		KindBackupRestoreState:            "KindBackupRestoreState",
		KindBackupRuntimeObservationState: "KindBackupRuntimeObservationState",
	}
	for name, kind := range workerReadModels {
		if kind != expected[name] {
			t.Fatalf("%s expected kind %d, got %d", name, expected[name], kind)
		}
		if previous, ok := seen[kind]; ok {
			t.Fatalf("%s collides with %s at kind %d", name, previous, kind)
		}
		seen[kind] = name
	}
}

func TestContinuityPublisherKindConstantsUnique(t *testing.T) {
	continuityKinds := map[string]int{
		"KindContinuityProfile":      KindContinuityProfile,
		"KindFailoverPolicy":         KindFailoverPolicy,
		"KindStandbyNodeDefinition":  KindStandbyNodeDefinition,
		"KindReplicationPolicy":      KindReplicationPolicy,
		"KindRecoveryWorkflow":       KindRecoveryWorkflow,
		"KindHeartbeatObservation":   KindHeartbeatObservation,
		"KindContinuityStatus":       KindContinuityStatus,
		"KindDegradedModeActivation": KindDegradedModeActivation,
		"KindRecoveryProgress":       KindRecoveryProgress,
		"KindFailoverRequest":        KindFailoverRequest,
		"KindRecoveryRequest":        KindRecoveryRequest,
	}
	expected := map[string]int{
		"KindContinuityProfile":      31400,
		"KindFailoverPolicy":         31401,
		"KindStandbyNodeDefinition":  31402,
		"KindReplicationPolicy":      31403,
		"KindRecoveryWorkflow":       31404,
		"KindHeartbeatObservation":   30315,
		"KindContinuityStatus":       30351,
		"KindDegradedModeActivation": 30352,
		"KindRecoveryProgress":       30353,
		"KindFailoverRequest":        38430,
		"KindRecoveryRequest":        38431,
	}
	seen := map[int]string{
		KindWorkerState:              "KindWorkerState",
		KindWorkerAssignmentState:    "KindWorkerAssignmentState",
		KindWorkerDrainStatus:        "KindWorkerDrainStatus",
		KindWorkerEligibilityPreview: "KindWorkerEligibilityPreview",
		KindBackupDefinitionRegistry: "KindBackupDefinitionRegistry",
		KindBackupPolicyRegistry:     "KindBackupPolicyRegistry",
		KindBackupRepositoryRegistry: "KindBackupRepositoryRegistry",
	}
	for name, kind := range continuityKinds {
		if kind != expected[name] {
			t.Fatalf("%s expected kind %d, got %d", name, expected[name], kind)
		}
		if previous, ok := seen[kind]; ok {
			t.Fatalf("%s collides with %s at kind %d", name, previous, kind)
		}
		seen[kind] = name
	}
}

func TestDNSPublisherKindConstantsUnique(t *testing.T) {
	dnsKinds := map[string]int{
		"KindDNSZoneSyncedAudit":           KindDNSZoneSyncedAudit,
		"KindDNSRecordChangedAudit":        KindDNSRecordChangedAudit,
		"KindDNSDriftDetectedAudit":        KindDNSDriftDetectedAudit,
		"KindDNSEndpointRegisteredAudit":   KindDNSEndpointRegisteredAudit,
		"KindDNSEndpointDeregisteredAudit": KindDNSEndpointDeregisteredAudit,
		"KindDNSZoneState":                 KindDNSZoneState,
		"KindDNSEndpointState":             KindDNSEndpointState,
		"KindDNSPolicyState":               KindDNSPolicyState,
		"KindDNSBackendState":              KindDNSBackendState,
	}
	existing := map[int]string{
		KindBuildRegistered:             "KindBuildRegistered",
		KindArtifactRegistered:          "KindArtifactRegistered",
		KindDeploymentCreated:           "KindDeploymentCreated",
		KindDeploymentComplete:          "KindDeploymentComplete",
		KindDriftDetected:               "KindDriftDetected",
		KindObservation:                 "KindObservation",
		KindServiceRegistryAudit:        "KindServiceRegistryAudit",
		KindEnvironmentRegistryAudit:    "KindEnvironmentRegistryAudit",
		KindStateChangedAudit:           "KindStateChangedAudit",
		KindRuntimeActionAudit:          "KindRuntimeActionAudit",
		KindReconcileAudit:              "KindReconcileAudit",
		KindAdoptionAudit:               "KindAdoptionAudit",
		KindDeploymentApprovalAudit:     "KindDeploymentApprovalAudit",
		KindDeploymentRunAudit:          "KindDeploymentRunAudit",
		KindLLMRouteRegistryAudit:       "KindLLMRouteRegistryAudit",
		KindLLMReleaseRegisteredAudit:   "KindLLMReleaseRegisteredAudit",
		KindLLMDeploymentAudit:          "KindLLMDeploymentAudit",
		KindLLMRunAudit:                 "KindLLMRunAudit",
		KindLLMRouteStateAudit:          "KindLLMRouteStateAudit",
		KindLLMGatewayAudit:             "KindLLMGatewayAudit",
		KindServiceState:                "KindServiceState",
		KindServiceRegistry:             "KindServiceRegistry",
		KindEnvironmentRegistry:         "KindEnvironmentRegistry",
		KindLLMRouteRegistry:            "KindLLMRouteRegistry",
		KindLLMRouteState:               "KindLLMRouteState",
		KindArtifactRegistry:            "KindArtifactRegistry",
		KindDeploymentIntentRegistry:    "KindDeploymentIntentRegistry",
		KindDeploymentRunRegistry:       "KindDeploymentRunRegistry",
		KindBuildRegistry:               "KindBuildRegistry",
		KindPolicyRegistry:              "KindPolicyRegistry",
		KindPackageRepositoryRegistry:   "KindPackageRepositoryRegistry",
		KindPackageArtifactRegistry:     "KindPackageArtifactRegistry",
		KindPackagePromotionRegistry:    "KindPackagePromotionRegistry",
		KindSystemDiscovery:             "KindSystemDiscovery",
		KindMLModelRegistry:             "KindMLModelRegistry",
		KindMLModelVersionRegistry:      "KindMLModelVersionRegistry",
		KindMLDatasetRegistry:           "KindMLDatasetRegistry",
		KindMLRecipeRegistry:            "KindMLRecipeRegistry",
		KindMLRecipeRunState:            "KindMLRecipeRunState",
		KindMLInferenceEndpointRegistry: "KindMLInferenceEndpointRegistry",
		KindMLInferenceEndpointState:    "KindMLInferenceEndpointState",
		KindMLEvaluationExperimentState: "KindMLEvaluationExperimentState",
		KindMLArtifactProvenanceGraph:   "KindMLArtifactProvenanceGraph",
		KindMLRuntimeCapabilityProfile:  "KindMLRuntimeCapabilityProfile",
	}
	seen := map[int]string{}
	for name, kind := range dnsKinds {
		if previous, ok := seen[kind]; ok {
			t.Fatalf("%s collides with %s at kind %d", name, previous, kind)
		}
		seen[kind] = name
		if existingName, ok := existing[kind]; ok {
			t.Fatalf("%s collides with existing %s at kind %d", name, existingName, kind)
		}
	}
}
