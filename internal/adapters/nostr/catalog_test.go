package nostr

import (
	"encoding/json"
	"testing"

	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
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
	want := []string{"discovery_snapshot", "state_snapshot", "status_live", "audit_live"}
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
	want := []string{"discovery_snapshot", "state_snapshot", "status_live", "audit_live"}
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
	tierKinds := catalog.KindsForTier(2)
	assertKindsInclude(t, tierKinds, []int{KindCASControlState, KindCASAudit, KindNIP38Status, KindRelaySetDiscovery, KindNIP65RelayList, kinds.ContextVMServerAnnouncement})
	assertKindsExclude(t, tierKinds, []int{KindServiceRegistry, KindEnvironmentRegistry, KindControlPlaneDeployRequest, KindControlPlaneDeploymentResult, KindMLRecipeRunRequest, KindAssistantPromptRequest, KindFIPSOverlayAdvert})
	seen := map[int]struct{}{}
	for _, kind := range tierKinds {
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
			if decoded.Family == "" {
				t.Fatalf("required group %q kind %d decoded empty projection family", group.Name, kind)
			}
			if decoded.DTag == "" {
				t.Fatalf("required group %q kind %d decoded empty d tag", group.Name, kind)
			}
			if decoded.Timestamp.IsZero() {
				t.Fatalf("required group %q kind %d decoded zero timestamp", group.Name, kind)
			}
		}
	}
}

func TestProjectorSubscriberCanonicalKindsAreRepresented(t *testing.T) {
	catalog := NewKindCatalog()
	assertCatalogHasKinds(t, catalog, append(projectorKindCoverage(), subscriberKindCoverage()...))
}

func TestCatalogGroupsExcludeLegacyRuntimeKinds(t *testing.T) {
	catalog := NewKindCatalog()
	for _, group := range catalog.Groups {
		for _, kind := range group.Kinds {
			if isLegacyCatalogRuntimeKind(kind) {
				t.Fatalf("catalog group %q includes legacy runtime kind %d", group.Name, kind)
			}
		}
	}
}

func expectedCatalogKinds() []int {
	return []int{
		KindCASControlState,
		KindCASAudit,
		KindNIP38Status,
		KindRelaySetDiscovery,
		KindNIP65RelayList,
		kinds.ContextVMServerAnnouncement,
		kinds.ContextVMToolsList,
		kinds.ContextVMResourcesList,
		kinds.ContextVMResourceTemplatesList,
		kinds.ContextVMPromptsList,
		KindBahiaIdentityDefinition,
		KindBahiaReplayCheckpoint,
		KindBahiaReadinessStatus,
		KindHiveCIWorkflowRun,
		KindHiveCIWorkflowResult,
		KindLoomWorkerAdvertisement,
		KindLoomJobStatusUpdate,
		KindLoomJobResult,
		KindLoomJobCancellation,
		KindFIPSOverlayAdvert,
	}
}

func isLegacyCatalogRuntimeKind(kind int) bool {
	return (kind >= 5941 && kind <= 7999) ||
		(kind >= 31100 && kind <= 31399) ||
		(kind >= 32000 && kind <= 32099) ||
		(kind >= 38390 && kind <= 38499)
}

func projectorKindCoverage() []int {
	return []int{
		KindCASControlState,
		KindCASAudit,
		KindNIP38Status,
		KindRelaySetDiscovery,
		KindNIP65RelayList,
		kinds.ContextVMServerAnnouncement,
		kinds.ContextVMToolsList,
		kinds.ContextVMResourcesList,
		kinds.ContextVMResourceTemplatesList,
		kinds.ContextVMPromptsList,
		KindBahiaIdentityDefinition,
		KindBahiaReplayCheckpoint,
		KindBahiaReadinessStatus,
	}
}

func subscriberKindCoverage() []int {
	return []int{
		KindCASControlState,
		KindCASAudit,
		KindNIP38Status,
		kinds.ContextVMServerAnnouncement,
		kinds.ContextVMToolsList,
		kinds.ContextVMResourcesList,
		kinds.ContextVMResourceTemplatesList,
		kinds.ContextVMPromptsList,
		KindRelaySetDiscovery,
		KindNIP65RelayList,
		KindHiveCIWorkflowRun,
		KindHiveCIWorkflowResult,
		KindLoomWorkerAdvertisement,
		KindLoomJobStatusUpdate,
		KindLoomJobResult,
		KindLoomJobCancellation,
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

// ---------------------------------------------------------------------------
// Backward-compatible decoding tests (Item 8 — bahia-zu2p.7.2)
// ---------------------------------------------------------------------------

func catalogDecode(t *testing.T, catalog *KindCatalog, ev *gonostr.Event) *DecodedProjectionEvent {
	t.Helper()
	decoder, ok := catalog.Decoder(ev.Kind)
	if !ok {
		t.Fatalf("no decoder for kind %d", ev.Kind)
	}
	decoded, err := decoder(ev)
	if err != nil {
		t.Fatalf("decode kind %d: %v", ev.Kind, err)
	}
	return decoded
}

func TestCatalogDecodesLegacyStateWithoutDesiredMetadata(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindServiceState,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "svc:env"}, {"service", "svc"}, {"environment", "env"}, {"drift_status", "unknown"}},
		Content:   `{"deleted":false,"service_id":"svc","environment_id":"env","drift_status":"unknown","updated_at":"2026-05-01T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.State == nil {
		t.Fatal("decoded state is nil")
	}
	if decoded.State.DesiredHash != "" {
		t.Fatalf("legacy state should have empty desired_hash, got %q", decoded.State.DesiredHash)
	}
	if decoded.State.Renderer != "" {
		t.Fatalf("legacy state should have empty renderer, got %q", decoded.State.Renderer)
	}
}

func TestCatalogDecodesEnrichedStateWithDesiredMetadata(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindServiceState,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "svc:env"}, {"service", "svc"}, {"environment", "env"}, {"drift_status", "in_sync"}, {"desired_hash", "sha256:abc"}},
		Content:   `{"deleted":false,"service_id":"svc","environment_id":"env","drift_status":"in_sync","desired_hash":"sha256:abc","renderer":"compose","target":"api-prod","updated_at":"2026-05-26T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.State == nil {
		t.Fatal("decoded state is nil")
	}
	if decoded.State.DesiredHash != "sha256:abc" {
		t.Fatalf("desired_hash = %q, want sha256:abc", decoded.State.DesiredHash)
	}
	if decoded.State.Renderer != "compose" {
		t.Fatalf("renderer = %q, want compose", decoded.State.Renderer)
	}
	if decoded.State.Target != "api-prod" {
		t.Fatalf("target = %q, want api-prod", decoded.State.Target)
	}
}

func TestCatalogDecodesLegacyIntentWithoutDesiredHash(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindDeploymentIntentRegistry,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "intent-123"}, {"intent", "intent-123"}, {"status", "deploying"}},
		Content:   `{"deleted":false,"id":"intent-123","service_id":"svc","environment_id":"env","artifact_id":"art","status":"deploying","updated_at":"2026-05-01T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.Intent == nil {
		t.Fatal("decoded intent is nil")
	}
	if decoded.Intent.DesiredHash != "" {
		t.Fatalf("legacy intent should have empty desired_hash, got %q", decoded.Intent.DesiredHash)
	}
}

func TestCatalogDecodesEnrichedIntentWithDesiredHash(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindDeploymentIntentRegistry,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "intent-123"}, {"intent", "intent-123"}, {"status", "deploying"}, {"desired_hash", "sha256:xyz"}},
		Content:   `{"deleted":false,"id":"intent-123","service_id":"svc","environment_id":"env","artifact_id":"art","status":"deploying","desired_hash":"sha256:xyz","renderer":"docker","target":"api-prod","updated_at":"2026-05-26T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.Intent.DesiredHash != "sha256:xyz" {
		t.Fatalf("desired_hash = %q, want sha256:xyz", decoded.Intent.DesiredHash)
	}
	if decoded.Intent.Renderer != "docker" {
		t.Fatalf("renderer = %q, want docker", decoded.Intent.Renderer)
	}
	if decoded.Intent.Target != "api-prod" {
		t.Fatalf("target = %q, want api-prod", decoded.Intent.Target)
	}
}

func TestCatalogDecodesLegacyRunWithoutApplyMetadata(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindDeploymentRunRegistry,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "run-123"}, {"run", "run-123"}, {"status", "succeeded"}},
		Content:   `{"deleted":false,"id":"run-123","deployment_intent_id":"intent-123","status":"succeeded","updated_at":"2026-05-01T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.Run == nil {
		t.Fatal("decoded run is nil")
	}
	if decoded.Run.Renderer != "" {
		t.Fatalf("legacy run should have empty renderer, got %q", decoded.Run.Renderer)
	}
}

func TestCatalogDecodesEnrichedRunWithApplyMetadata(t *testing.T) {
	catalog := NewKindCatalog()
	ev := &gonostr.Event{
		Kind:      KindDeploymentRunRegistry,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", "run-123"}, {"run", "run-123"}, {"status", "succeeded"}, {"renderer", "compose"}},
		Content:   `{"deleted":false,"id":"run-123","deployment_intent_id":"intent-123","status":"succeeded","renderer":"compose","desired_hash":"sha256:h1","revision_hash":"sha256:r1","target":"api-prod","apply_summary":"recreated 1 service","observation_id":"obs-123","updated_at":"2026-05-26T00:00:00Z"}`,
	}
	decoded := catalogDecode(t, catalog, ev)
	if decoded.Run.Renderer != "compose" {
		t.Fatalf("renderer = %q, want compose", decoded.Run.Renderer)
	}
	if decoded.Run.DesiredHash != "sha256:h1" {
		t.Fatalf("desired_hash = %q, want sha256:h1", decoded.Run.DesiredHash)
	}
	if decoded.Run.RevisionHash != "sha256:r1" {
		t.Fatalf("revision_hash = %q, want sha256:r1", decoded.Run.RevisionHash)
	}
	if decoded.Run.Target != "api-prod" {
		t.Fatalf("target = %q, want api-prod", decoded.Run.Target)
	}
	if decoded.Run.ApplySummary != "recreated 1 service" {
		t.Fatalf("apply_summary = %q, want 'recreated 1 service'", decoded.Run.ApplySummary)
	}
	if decoded.Run.ObservationID != "obs-123" {
		t.Fatalf("observation_id = %q, want obs-123", decoded.Run.ObservationID)
	}
}
