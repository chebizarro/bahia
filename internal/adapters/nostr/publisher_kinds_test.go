package nostr

import "testing"

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
