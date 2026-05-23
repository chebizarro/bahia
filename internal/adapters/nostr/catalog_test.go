package nostr

import "testing"

func TestNewKindCatalogContainsExpectedKinds(t *testing.T) {
	catalog := NewKindCatalog()
	assertCatalogHasKinds(t, catalog, expectedCatalogKinds())
	if catalog.Version != KindCatalogVersion {
		t.Fatalf("catalog version = %q, want %q", catalog.Version, KindCatalogVersion)
	}
	for _, kind := range catalog.AllKinds() {
		if _, ok := catalog.Decoder(kind); !ok {
			t.Fatalf("missing decoder for kind %d", kind)
		}
	}
}

func TestGroupsForTierReturnsGroupsAtOrBelowTier(t *testing.T) {
	catalog := NewKindCatalog()
	groups := catalog.GroupsForTier(1)
	got := groupNames(groups)
	want := []string{"system_snapshot", "continuity_snapshot", "worker_snapshot", "continuity_live"}
	assertStringSetEqual(t, got, want)
	for _, group := range groups {
		if group.Tier > 1 {
			t.Fatalf("GroupsForTier(1) returned tier %d group %q", group.Tier, group.Name)
		}
	}
}

func TestSnapshotAndLiveGroupsFilterBySnapshotFlag(t *testing.T) {
	catalog := NewKindCatalog()
	for _, group := range catalog.SnapshotGroups() {
		if !group.Snapshot {
			t.Fatalf("SnapshotGroups returned live group %q", group.Name)
		}
	}
	for _, group := range catalog.LiveGroups() {
		if group.Snapshot {
			t.Fatalf("LiveGroups returned snapshot group %q", group.Name)
		}
	}
}

func TestAllKindsReturnsUniqueSortedValues(t *testing.T) {
	catalog := NewKindCatalog()
	kinds := catalog.AllKinds()
	seen := map[int]struct{}{}
	last := -1
	for _, kind := range kinds {
		if _, ok := seen[kind]; ok {
			t.Fatalf("AllKinds returned duplicate kind %d", kind)
		}
		if kind < last {
			t.Fatalf("AllKinds returned unsorted values: %d before %d", last, kind)
		}
		seen[kind] = struct{}{}
		last = kind
	}
}

func TestRequiredGroupsForTierFiltersByTierAndRequired(t *testing.T) {
	catalog := NewKindCatalog()
	got := groupNames(catalog.RequiredGroupsForTier(2))
	want := []string{"system_snapshot", "continuity_snapshot", "worker_snapshot", "continuity_live", "core_registry_snapshot", "core_control_plane_live"}
	assertStringSetEqual(t, got, want)
	for _, group := range catalog.RequiredGroupsForTier(2) {
		if !group.Required {
			t.Fatalf("RequiredGroupsForTier returned optional group %q", group.Name)
		}
		if group.Tier > 2 {
			t.Fatalf("RequiredGroupsForTier returned tier %d group %q", group.Tier, group.Name)
		}
	}
}

func TestKindsForTierReturnsUniqueTierKinds(t *testing.T) {
	catalog := NewKindCatalog()
	kinds := catalog.KindsForTier(2)
	assertKindsInclude(t, kinds, []int{KindServiceRegistry, KindEnvironmentRegistry, KindControlPlaneDeployRequest, KindControlPlaneDeploymentResult})
	assertKindsExclude(t, kinds, []int{KindMLRecipeRunRequest, KindAssistantPromptRequest, KindFIPSOverlayAdvert})
	seen := map[int]struct{}{}
	for _, kind := range kinds {
		if _, ok := seen[kind]; ok {
			t.Fatalf("KindsForTier returned duplicate kind %d", kind)
		}
		seen[kind] = struct{}{}
	}
}

func TestProjectorSubscriberReactorKindsAreRepresented(t *testing.T) {
	catalog := NewKindCatalog()
	assertCatalogHasKinds(t, catalog, append(append(projectorKindCoverage(), subscriberKindCoverage()...), reactorKindCoverage()...))
}

func expectedCatalogKinds() []int {
	return append(append(append(projectorKindCoverage(), subscriberKindCoverage()...), reactorKindCoverage()...),
		KindNIP65RelayList,
		KindBahiaIdentityDefinition,
		KindBahiaReplayCheckpoint,
		KindBahiaReadinessStatus,
		KindContinuityProfile,
		KindFailoverPolicy,
		KindStandbyNodeDefinition,
		KindReplicationPolicy,
		KindRecoveryWorkflow,
		KindHeartbeatObservation,
		KindContinuityStatus,
		KindDegradedModeActivation,
		KindRecoveryProgress,
		KindFailoverRequest,
		KindRecoveryRequest,
		KindBackupDefinitionRegistry,
		KindBackupPolicyRegistry,
		KindBackupRepositoryRegistry,
		KindBackupRetentionRegistry,
		KindBackupRecipeRegistry,
		KindBackupRunState,
		KindBackupVerificationState,
		KindBackupRestoreState,
		KindBackupRuntimeObservationState,
		KindFIPSOverlayAdvert,
	)
}

func projectorKindCoverage() []int {
	return []int{
		KindDNSEndpointState,
		KindRelaySetDiscovery,
		KindControlPlaneDeployRequest,
		KindControlPlaneRollbackRequest,
		KindControlPlaneServiceAction,
		KindControlPlaneServiceCreate,
		KindControlPlaneEnvironmentCreate,
		KindControlPlaneDeploymentApproval,
		KindControlPlaneObservationSubmit,
		KindControlPlaneDriftRemediate,
		KindControlPlaneDeploymentStatus,
		KindControlPlaneServiceStatus,
		KindControlPlaneDeploymentResult,
		KindControlPlaneActionResult,
		KindControlPlaneServiceCreateResult,
		KindControlPlaneEnvironmentCreateResult,
		KindControlPlaneObservationResult,
		KindControlPlaneRemediationResult,
		KindServiceState,
		KindServiceRegistry,
		KindEnvironmentRegistry,
		KindLLMRouteRegistry,
		KindLLMRouteState,
		KindArtifactRegistry,
		KindDeploymentIntentRegistry,
		KindDeploymentRunRegistry,
		KindBuildRegistry,
		KindPolicyRegistry,
		KindWorkerState,
		KindWorkerAssignmentState,
		KindWorkerDrainStatus,
		KindWorkerEligibilityPreview,
		KindControlPlaneWorkerCordonRequest,
		KindControlPlaneWorkerUncordonRequest,
		KindControlPlaneWorkerDrainRequest,
		KindControlPlaneWorkerUndrainRequest,
		KindControlPlaneWorkerMaintenanceEnter,
		KindControlPlaneWorkerMaintenanceExit,
		KindControlPlaneWorkerLabelsUpdate,
		KindControlPlaneWorkerStatus,
		KindControlPlaneWorkerResult,
		KindMLRecipeRunRequest,
		KindMLInferenceDeployRequest,
		KindMLInferenceDeploymentApproval,
		KindMLInferenceRollbackRequest,
		KindMLModelImportRequest,
		KindMLRecipeRunResult,
		KindMLInferenceDeployResult,
		KindMLInferenceApprovalResult,
		KindMLInferenceRollbackResult,
		KindMLModelImportResult,
	}
}

func subscriberKindCoverage() []int {
	return []int{
		KindHiveCIWorkflowRun,
		KindHiveCIWorkflowResult,
		KindLoomWorkerAdvertisement,
		KindLoomJobStatusUpdate,
		KindLoomJobResult,
		KindLoomJobCancellation,
		KindCmdBuildRegister,
		KindCmdArtifactRegister,
		KindCmdIntentCreate,
		KindCmdIntentApprove,
		KindCmdIntentReject,
		KindCmdRollbackRequest,
		KindControlPlaneDeployRequest,
		KindControlPlaneRollbackRequest,
		KindControlPlaneServiceAction,
		KindControlPlaneServiceCreate,
		KindControlPlaneEnvironmentCreate,
		KindControlPlaneDeploymentApproval,
		KindControlPlaneObservationSubmit,
		KindControlPlaneDriftRemediate,
		KindControlPlaneLLMRouteCreate,
		KindControlPlaneLLMReleaseRegister,
		KindControlPlaneLLMDeployRequest,
		KindControlPlaneLLMDeploymentApproval,
		KindControlPlaneLLMRollbackRequest,
		KindControlPlaneToolProvisionRequest,
		KindControlPlaneToolApprovalResponse,
		KindControlPlaneAdoptionScanRequest,
		KindControlPlaneAdoptionImportRequest,
		KindControlPlaneServiceUpdate,
		KindControlPlaneServiceDelete,
		KindControlPlaneEnvironmentUpdate,
		KindControlPlaneEnvironmentDelete,
		KindControlPlaneArtifactRegister,
		KindControlPlanePolicyCreate,
		KindControlPlanePolicyUpdate,
		KindControlPlanePolicyDelete,
		KindControlPlanePolicyEvaluate,
		KindMLRecipeRunRequest,
		KindMLInferenceDeployRequest,
		KindMLInferenceDeploymentApproval,
		KindMLInferenceRollbackRequest,
		KindMLModelImportRequest,
		KindControlPlanePackageRepositoryApply,
		KindControlPlanePackageRepositoryDelete,
		KindControlPlanePackagePublishIntent,
		KindControlPlanePackagePromotionRequest,
		KindControlPlanePackageYankRequest,
		KindControlPlanePackageDriftDetect,
		KindAssistantPromptRequest,
		KindAssistantApproval,
	}
}

func reactorKindCoverage() []int {
	return []int{
		KindControlPlaneDeployRequest,
		KindControlPlaneRollbackRequest,
		KindControlPlaneServiceAction,
		KindControlPlaneServiceCreate,
		KindControlPlaneEnvironmentCreate,
		KindControlPlaneDeploymentApproval,
		KindControlPlaneObservationSubmit,
		KindControlPlaneDriftRemediate,
		KindControlPlaneLLMRouteCreate,
		KindControlPlaneLLMReleaseRegister,
		KindControlPlaneLLMDeployRequest,
		KindControlPlaneLLMDeploymentApproval,
		KindControlPlaneLLMRollbackRequest,
		KindControlPlaneToolProvisionRequest,
		KindControlPlaneToolApprovalRequest,
		KindControlPlaneAdoptionScanRequest,
		KindControlPlaneAdoptionImportRequest,
		KindControlPlaneServiceUpdate,
		KindControlPlaneServiceDelete,
		KindControlPlaneEnvironmentUpdate,
		KindControlPlaneEnvironmentDelete,
		KindControlPlaneArtifactRegister,
		KindControlPlanePolicyCreate,
		KindControlPlanePolicyUpdate,
		KindControlPlanePolicyDelete,
		KindControlPlanePolicyEvaluate,
		KindControlPlanePackageRepositoryApply,
		KindControlPlanePackageRepositoryDelete,
		KindControlPlanePackagePublishIntent,
		KindControlPlanePackagePromotionRequest,
		KindControlPlanePackageYankRequest,
		KindControlPlanePackageDriftDetect,
		KindControlPlaneWorkerCordonRequest,
		KindControlPlaneWorkerUncordonRequest,
		KindControlPlaneWorkerDrainRequest,
		KindControlPlaneWorkerUndrainRequest,
		KindControlPlaneWorkerMaintenanceEnter,
		KindControlPlaneWorkerMaintenanceExit,
		KindControlPlaneWorkerLabelsUpdate,
		KindControlPlaneWorkerPolicyApplyRequest,
		KindControlPlaneWorkloadPinRequest,
		KindMLRecipeRunRequest,
		KindMLInferenceDeployRequest,
		KindMLInferenceDeploymentApproval,
		KindMLInferenceRollbackRequest,
		KindMLModelImportRequest,
		KindMLRecipeRunResult,
		KindMLInferenceDeployResult,
		KindMLInferenceApprovalResult,
		KindMLInferenceRollbackResult,
		KindMLModelImportResult,
		KindControlPlaneDeploymentStatus,
		KindControlPlaneServiceStatus,
		KindControlPlaneActionStatus,
		KindControlPlaneLLMDeploymentStatus,
		KindControlPlaneToolProvisionStatus,
		KindControlPlaneAdoptionStatus,
		KindControlPlanePackageStatus,
		KindControlPlaneWorkerStatus,
		KindControlPlaneDeploymentResult,
		KindControlPlaneActionResult,
		KindControlPlaneServiceCreateResult,
		KindControlPlaneEnvironmentCreateResult,
		KindControlPlaneObservationResult,
		KindControlPlaneRemediationResult,
		KindControlPlaneLLMRouteCreateResult,
		KindControlPlaneLLMReleaseRegisterResult,
		KindControlPlaneLLMDeploymentResult,
		KindControlPlaneToolProvisionResult,
		KindControlPlaneToolApprovalResponse,
		KindControlPlaneAdoptionScanResult,
		KindControlPlaneAdoptionImportResult,
		KindControlPlanePackageResult,
		KindControlPlanePackageDriftEvent,
		KindControlPlaneWorkerResult,
		KindServiceState,
		KindServiceRegistry,
		KindEnvironmentRegistry,
		KindLLMRouteRegistry,
		KindLLMRouteState,
		KindArtifactRegistry,
		KindDeploymentIntentRegistry,
		KindDeploymentRunRegistry,
		KindBuildRegistry,
		KindPolicyRegistry,
		KindPackageRepositoryRegistry,
		KindPackageArtifactRegistry,
		KindPackagePromotionRegistry,
		KindWorkerState,
		KindWorkerAssignmentState,
		KindWorkerDrainStatus,
		KindWorkerEligibilityPreview,
		KindLegacyWorkerState,
		KindLegacyWorkerAssignmentState,
		KindLegacyWorkerDrainStatus,
		KindLegacyWorkerEligibilityPreview,
	}
}

func assertCatalogHasKinds(t *testing.T, catalog *KindCatalog, want []int) {
	t.Helper()
	got := make(map[int]struct{})
	for _, kind := range catalog.AllKinds() {
		got[kind] = struct{}{}
	}
	for _, kind := range want {
		if _, ok := got[kind]; !ok {
			t.Fatalf("catalog missing kind %d", kind)
		}
	}
}

func assertKindsInclude(t *testing.T, got []int, want []int) {
	t.Helper()
	gotSet := intsToSet(got)
	for _, kind := range want {
		if _, ok := gotSet[kind]; !ok {
			t.Fatalf("missing kind %d from %v", kind, got)
		}
	}
}

func assertKindsExclude(t *testing.T, got []int, want []int) {
	t.Helper()
	gotSet := intsToSet(got)
	for _, kind := range want {
		if _, ok := gotSet[kind]; ok {
			t.Fatalf("unexpected kind %d in %v", kind, got)
		}
	}
}

func intsToSet(values []int) map[int]struct{} {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func groupNames(groups []ReplayGroup) []string {
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		names = append(names, group.Name)
	}
	return names
}

func assertStringSetEqual(t *testing.T, got []string, want []string) {
	t.Helper()
	gotSet := make(map[string]struct{}, len(got))
	for _, value := range got {
		gotSet[value] = struct{}{}
	}
	if len(gotSet) != len(want) {
		t.Fatalf("got groups %v, want %v", got, want)
	}
	for _, value := range want {
		if _, ok := gotSet[value]; !ok {
			t.Fatalf("got groups %v, want %v", got, want)
		}
	}
}
