package nostr

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// cpStateTopicsByFamily is every cp-state family's declared "t" topic. The
// worker values are declared with the worker contract (bahia-irsry.9.2).
var cpStateTopicsByFamily = map[int]string{
	KindServiceState:                    kinds.CPStateTopicServiceState,
	KindServiceRegistry:                 kinds.CPStateTopicServiceRegistry,
	KindEnvironmentRegistry:             kinds.CPStateTopicEnvironmentRegistry,
	KindLLMRouteRegistry:                kinds.CPStateTopicLLMRoute,
	KindLLMRouteState:                   kinds.CPStateTopicLLMState,
	KindArtifactRegistry:                kinds.CPStateTopicArtifactRegistry,
	KindDeploymentIntentRegistry:        kinds.CPStateTopicDeploymentIntent,
	KindDeploymentRunRegistry:           kinds.CPStateTopicDeploymentRun,
	KindBuildRegistry:                   kinds.CPStateTopicBuildRegistry,
	KindPolicyRegistry:                  kinds.CPStateTopicPolicyRegistry,
	KindPackageRepositoryRegistry:       kinds.CPStateTopicPackageRepository,
	KindPackageArtifactRegistry:         kinds.CPStateTopicPackageArtifact,
	KindPackagePromotionRegistry:        kinds.CPStateTopicPackagePromotion,
	KindPackageIntentState:              kinds.CPStateTopicPackageIntent,
	KindToolProvisionIntentState:        kinds.CPStateTopicToolProvisionIntent,
	KindToolDenylistState:               kinds.CPStateTopicToolDenylist,
	KindToolProfileState:                kinds.CPStateTopicToolProfile,
	KindNotificationLogState:            kinds.CPStateTopicNotificationLog,
	KindWorkerState:                     "worker-state",
	KindWorkerAssignmentState:           "worker-assignment",
	KindWorkerDrainStatus:               "worker-drain",
	KindWorkerEligibilityPreview:        "worker-eligibility",
	int(kinds.CPStateFamilyDNSZone):     kinds.DNSZoneTopic,
	int(kinds.CPStateFamilyDNSEndpoint): kinds.DNSEndpointTopic,
	int(kinds.CPStateFamilyDNSPolicy):   kinds.DNSPolicyTopic,
	int(kinds.CPStateFamilyDNSBackend):  kinds.DNSBackendTopic,
	KindMLModelRegistry:                 kinds.CPStateTopicMLModel,
	KindMLModelVersionRegistry:          kinds.CPStateTopicMLModelVersion,
	KindMLDatasetRegistry:               kinds.CPStateTopicMLDataset,
	KindMLRecipeRegistry:                kinds.CPStateTopicMLRecipe,
	KindMLRecipeRunState:                kinds.CPStateTopicMLRecipeRun,
	KindMLInferenceEndpointRegistry:     kinds.CPStateTopicMLEndpoint,
	KindMLInferenceEndpointState:        kinds.CPStateTopicMLEndpointState,
	KindMLEvaluationExperimentState:     kinds.CPStateTopicMLEvaluation,
	KindMLArtifactProvenanceGraph:       kinds.CPStateTopicMLProvenance,
	KindMLRuntimeCapabilityProfile:      kinds.CPStateTopicMLRuntimeCapability,
	KindBackupDefinitionRegistry:        kinds.CPStateTopicBackupDefinition,
	KindBackupPolicyRegistry:            kinds.CPStateTopicBackupPolicy,
	KindBackupRepositoryRegistry:        kinds.CPStateTopicBackupRepository,
	KindBackupRetentionRegistry:         kinds.CPStateTopicBackupRetention,
	KindBackupRecipeRegistry:            kinds.CPStateTopicBackupRecipe,
	KindBackupRunState:                  kinds.CPStateTopicBackupRun,
	KindBackupVerificationState:         kinds.CPStateTopicBackupVerification,
	KindBackupRestoreState:              kinds.CPStateTopicBackupRestore,
	KindBackupRuntimeObservationState:   kinds.CPStateTopicBackupRuntimeObservation,
	KindOrgRegistry:                     kinds.CPStateTopicOrgRegistry,
	KindOrgMemberRegistry:               kinds.CPStateTopicOrgMemberRegistry,
	KindOrgInviteRegistry:               kinds.CPStateTopicOrgInviteRegistry,
	KindSecretRegistry:                  kinds.CPStateTopicSecretRegistry,
	KindNotificationChannelRegistry:     kinds.CPStateTopicNotificationChannelRegistry,
	KindOrgKeyEnvelope:                  kinds.CPStateTopicOrgKeyEnvelope,
	KindPaymentRecord:                   kinds.CPStateTopicPaymentRecord,
	KindSecurityFindingRecord:           kinds.CPStateTopicSecurityFinding,
	KindSecurityScheduleRecord:          kinds.CPStateTopicSecuritySchedule,
	KindSecurityFindingDetailRecord:     kinds.CPStateTopicSecurityFindingDetail,
	KindSecurityTargetRecord:            kinds.CPStateTopicSecurityTarget,
	KindSecurityRunRecord:               kinds.CPStateTopicSecurityRun,
	KindAdoptionBindingRecord:           kinds.CPStateTopicAdoptionBinding,
	KindHiveCIPolicyRecord:              kinds.CPStateTopicHiveCIPolicy,
	KindHiveCIResultRecord:              kinds.CPStateTopicHiveCIResult,
	KindHiveCIInitiationRecord:          kinds.CPStateTopicHiveCIInitiation,
	KindLLMReleaseRegistry:              kinds.CPStateTopicLLMRelease,
	KindArtifactSignatureRegistry:       kinds.CPStateTopicArtifactSignature,
	KindArtifactSBOMRegistry:            kinds.CPStateTopicArtifactSBOM,
	KindSBOMPackageRegistry:             kinds.CPStateTopicSBOMPackage,
	KindRuntimeObservationState:         kinds.CPStateTopicRuntimeObservation,
	kinds.ManagedInstanceHealthRecord:   kinds.CPStateTopicManagedInstanceHealth,
	kinds.RouteCanaryRecord:             kinds.CPStateTopicRouteCanary,
	kinds.SoulRuntimePolicyRecord:       kinds.CPStateTopicSoulRuntimePolicy,
	kinds.BlossomAdminRecord:            kinds.CPStateTopicBlossomAdmin,
	kinds.BlossomBlobRecord:             kinds.CPStateTopicBlossomBlob,
}

func topicValues(tags gonostr.Tags) []string {
	var out []string
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "t" {
			out = append(out, tag[1])
		}
	}
	return out
}

// Every family controlStateEnvelope maps onto 30900 carries exactly its
// declared single-letter topic, on live records and tombstones alike, and
// every declared topic belongs to a family.
func TestControlStateEnvelopeStampsFamilyTopic(t *testing.T) {
	families := 0
	for legacyKind := 30000; legacyKind < 40000; legacyKind++ {
		domainName, entity := canonicalStateDomain(legacyKind)
		if domainName == "" {
			continue
		}
		families++
		want, ok := cpStateTopicsByFamily[legacyKind]
		if !ok {
			t.Fatalf("cp-state family %d (%s) has no declared t topic", legacyKind, domainName)
		}
		if want != domainName+"-"+entity {
			t.Fatalf("family %d topic %q does not follow <domain>-<entity> (%s-%s)", legacyKind, want, domainName, entity)
		}
		for _, deleted := range []bool{false, true} {
			wireKind, tags := controlStateEnvelope(legacyKind, "id-1", deleted)
			if wireKind != KindCASControlState {
				t.Fatalf("family %d wire kind = %d, want %d", legacyKind, wireKind, KindCASControlState)
			}
			if got := topicValues(tags); len(got) != 1 || got[0] != want {
				t.Fatalf("family %d (deleted=%t) t topics = %v, want [%s]", legacyKind, deleted, got, want)
			}
			wantD := "id-1"
			if prefix, worker := kinds.CPStateFamily(legacyKind).WorkerDPrefix(); worker {
				wantD = prefix + "id-1"
			}
			if tagValue(tags, "d") != wantD || tagValue(tags, "legacy_kind") != strconv.Itoa(legacyKind) {
				t.Fatalf("family %d envelope coordinate changed: %v", legacyKind, tags)
			}
		}
	}
	if families != len(cpStateTopicsByFamily) {
		t.Fatalf("envelope maps %d families, %d topics declared", families, len(cpStateTopicsByFamily))
	}
}

// Producer-shaped events: the builders that used to assemble their own 30900
// tags (service, environment, policy and LLM route registries) and the DNS
// builders that stamped their own topic now carry exactly one family topic.
func TestProjectedControlStateCarriesFamilyTopic(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	now := time.Now().UTC()
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, now)

	steps := []func() error{
		func() error {
			return projector.publishServiceRegistry(ctx, &domain.Service{ID: serviceID, Name: "api", CreatedAt: now, UpdatedAt: now}, false)
		},
		func() error {
			return projector.publishEnvironmentRegistry(ctx, &domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}, false)
		},
		func() error {
			routeID := uuid.New()
			return projector.publishControlState(ctx, KindLLMRouteRegistry, routeID.String(), false, nil, "{}", "llm_route", &routeID)
		},
		func() error {
			tags, contentJSON := RuntimeStateRecord(&state, nil)
			return projector.publishControlState(ctx, KindServiceState, serviceStateDTag(serviceID, envID), false, tags, contentJSON, "state.projection", &serviceID)
		},
		func() error {
			tags, contentJSON := RuntimeStateTombstoneRecord(serviceID, envID)
			return projector.publishControlState(ctx, KindServiceState, serviceStateDTag(serviceID, envID), true, tags, contentJSON, "state.projection", &serviceID)
		},
		func() error {
			// Phase 3 D1: DNS publish methods moved to DNSCanonicalPublisher;
			// use publishControlState directly for the envelope topic test.
			content, _ := json.Marshal(map[string]any{"deleted": false, "name": "prod.cascadia", "backend_ref": "fs"})
			tags := gonostr.Tags{{"zone", "prod.cascadia"}, {"backend", "fs"}, {"t", "bahia"}}
			return projector.publishControlState(ctx, KindDNSZoneState, dnsZoneDTag("prod.cascadia"), false, tags, string(content), "dns_zone.projection", nil)
		},
		func() error {
			content, _ := json.Marshal(map[string]any{"deleted": true, "coordinate": "endpoint:service:api:prod", "fqdn": "api.prod.cascadia"})
			tags := gonostr.Tags{{"t", "bahia"}, {"dns", "api.prod.cascadia"}}
			return projector.publishControlState(ctx, KindDNSEndpointState, "endpoint:service:api:prod", true, tags, string(content), "dns_endpoint.projection", nil)
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	sink.mu.Lock()
	published := append([]gonostr.Event(nil), sink.events...)
	sink.mu.Unlock()
	if len(published) != len(steps) {
		t.Fatalf("published %d events, want %d", len(published), len(steps))
	}
	for _, ev := range published {
		legacyKind, err := strconv.Atoi(tagValue(ev.Tags, "legacy_kind"))
		if err != nil || eventKindInt(&ev) != KindCASControlState {
			t.Fatalf("event is not a cp-state record: kind=%d tags=%v", eventKindInt(&ev), ev.Tags)
		}
		want := cpStateTopicsByFamily[legacyKind]
		var family []string
		for _, topic := range topicValues(ev.Tags) {
			if topic != "bahia" {
				family = append(family, topic)
			}
		}
		if len(family) != 1 || family[0] != want {
			t.Fatalf("family %d topics = %v, want exactly [%s]", legacyKind, family, want)
		}
		if tagValue(ev.Tags, "d") == "" || tagValue(ev.Tags, "deleted") == "" {
			t.Fatalf("family %d lost its envelope tags: %v", legacyKind, ev.Tags)
		}
	}
}
