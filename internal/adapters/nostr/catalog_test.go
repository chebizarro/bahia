package nostr

import (
	"encoding/json"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
)

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

func TestRequiredGroupsHaveNonErrorDecoders(t *testing.T) {
	catalog := NewKindCatalog()
	for _, group := range catalog.Groups {
		if !group.Required {
			continue
		}
		for _, kind := range group.Kinds {
			decoder, ok := catalog.Decoder(kind)
			if !ok {
				t.Fatalf("required group %q kind %d has no decoder", group.Name, kind)
			}
			ev := requiredDecoderFixture(kind)
			decoded, err := decoder(ev)
			if err != nil {
				t.Fatalf("required group %q kind %d decoder returned error: %v", group.Name, kind, err)
			}
			if decoded == nil {
				t.Fatalf("required group %q kind %d decoder returned nil event", group.Name, kind)
			}
			if decoded.Kind != kind {
				t.Fatalf("required group %q kind %d decoded kind = %d", group.Name, kind, decoded.Kind)
			}
		}
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

func requiredDecoderFixture(kind int) *gonostr.Event {
	tags := gonostr.Tags{
		{"d", "test-dtag"},
		{"service", "svc.api"},
		{"environment", "env-prod"},
		{"artifact", "artifact-1"},
		{"intent", "intent-1"},
		{"run", "run-1"},
		{"policy", "policy-1"},
		{"worker", "worker-pubkey"},
	}
	ev := &gonostr.Event{ID: "event-id", PubKey: "author-pubkey", Kind: kind, CreatedAt: gonostr.Now(), Tags: tags, Content: "{}"}
	switch kind {
	case KindContinuityProfile:
		ev.Tags = gonostr.Tags{{"d", "continuity-profile:svc.api"}, {"service", "svc.api"}, {"profile", "full"}}
		ev.Content = ""
	case KindFailoverPolicy:
		ev.Tags = gonostr.Tags{{"d", "failover-policy:svc.api:primary"}, {"service", "svc.api"}, {"recipe", "primary"}, {"recipe-kind", "failover"}}
		ev.Content = mustJSON(map[string]any{"name": "primary", "service_key": "svc.api", "kind": "failover", "trigger": map[string]any{"type": "manual", "target": "operator", "timeout": 1000000000}, "steps": []map[string]any{{"name": "emit", "action": "emit_event", "timeout": 1000000000}}})
	case KindRecoveryWorkflow:
		ev.Tags = gonostr.Tags{{"d", "recovery-workflow:svc.api:primary"}, {"service", "svc.api"}, {"recipe", "primary"}, {"recipe-kind", "recovery"}}
		ev.Content = mustJSON(map[string]any{"name": "primary", "service_key": "svc.api", "kind": "recovery", "steps": []map[string]any{{"name": "emit", "action": "emit_event", "timeout": 1000000000}}})
	case KindStandbyNodeDefinition:
		ev.Tags = gonostr.Tags{{"d", "standby:worker-pubkey:svc.api"}, {"worker", "worker-pubkey"}, {"host", "standby-1"}, {"role", "standby"}, {"service", "svc.api"}, {"profile", "full"}}
		ev.Content = ""
	case KindReplicationPolicy:
		ev.Tags = gonostr.Tags{{"d", "replication-policy:svc.api"}, {"service", "svc.api"}}
		ev.Content = mustJSON(map[string]any{"service_key": "svc.api", "targets": []map[string]any{{"worker_pubkey": "worker-pubkey", "strategy": "event_mirror", "max_staleness": 1000000000, "required_for_modes": []string{"full"}}}})
	case KindHeartbeatObservation:
		ev.Tags = gonostr.Tags{{"d", "heartbeat:worker-pubkey"}, {"worker", "worker-pubkey"}, {"sequence", "1"}, {"interval_ms", "1000"}}
		ev.Content = ""
	case KindFailoverRequest, KindRecoveryRequest:
		ev.Tags = gonostr.Tags{{"d", "request-key"}, {"service", "svc.api"}, {"target", "worker-pubkey"}, {"profile", "full"}}
		ev.Content = ""
	}
	return ev
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
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
