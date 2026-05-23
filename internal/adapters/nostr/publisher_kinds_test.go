package nostr

import "testing"

func TestWorkerPublisherKindConstantsPreserveWireCompatibility(t *testing.T) {
	if KindWorkerState != 31974 {
		t.Fatalf("KindWorkerState must remain wire-compatible at kind 31974, got %d", KindWorkerState)
	}
	workerReadModels := map[string]int{
		"KindWorkerAssignmentState":    KindWorkerAssignmentState,
		"KindWorkerDrainStatus":        KindWorkerDrainStatus,
		"KindWorkerEligibilityPreview": KindWorkerEligibilityPreview,
	}
	expected := map[string]int{
		"KindWorkerAssignmentState":    31991,
		"KindWorkerDrainStatus":        31992,
		"KindWorkerEligibilityPreview": 31993,
	}
	seen := map[int]string{KindWorkerState: "KindWorkerState"}
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
