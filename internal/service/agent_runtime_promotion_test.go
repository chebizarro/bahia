package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type fakeRuntimeSubscriptionSource struct {
	subs []RuntimePromotionSubscription
}

type fakeRuntimePromotionPolicyRepo struct {
	policies []domain.HiveCIPipelinePolicy
}

func (f fakeRuntimePromotionPolicyRepo) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return f.policies, nil
}

func (f fakeRuntimeSubscriptionSource) ListRuntimeReleaseSubscriptions(
	context.Context, uuid.UUID, string,
) ([]RuntimePromotionSubscription, error) {
	return f.subs, nil
}

type recordingPromotionIntentSink struct {
	intents          []*domain.DeploymentIntent
	failAfterPersist bool
}

func (r *recordingPromotionIntentSink) SubmitPromotionIntent(_ context.Context, intent *domain.DeploymentIntent) error {
	copyIntent := *intent
	for i := range r.intents {
		if r.intents[i].ID == intent.ID {
			r.intents[i] = &copyIntent
			if r.failAfterPersist {
				r.failAfterPersist = false
				return errors.New("injected failure after durable intent write")
			}
			return nil
		}
	}
	r.intents = append(r.intents, &copyIntent)
	if r.failAfterPersist {
		r.failAfterPersist = false
		return errors.New("injected failure after durable intent write")
	}
	return nil
}

type failOnceBindReleaseRepo struct {
	*memoryAgentReleaseRepo
	fail bool
}

func (r *failOnceBindReleaseRepo) BindRelease(ctx context.Context, binding *domain.AgentServiceReleaseBinding) error {
	if r.fail {
		r.fail = false
		return errors.New("injected binding failure")
	}
	return r.memoryAgentReleaseRepo.BindRelease(ctx, binding)
}

const (
	promotionTestRepoID  = "metiq-runtime"
	promotionTestBranch  = "master"
	promotionTestChannel = "stable"
)

func promotionTestDigest(digit string) string {
	return "sha256:" + strings.Repeat(digit, 64)
}

func promotionTestCommit() string {
	return strings.Repeat("c", 40)
}

func promotionTestPubkey(digit byte) string {
	return strings.Repeat(string(digit), 64)
}

func promotionTestRepoAddress() string {
	return "30617:" + promotionTestPubkey('a') + ":" + promotionTestRepoID
}

func promotionTestSignedRun(publisher string) string {
	encoded, _ := json.Marshal(map[string]any{
		"id":      promotionTestDigest("d"),
		"kind":    kinds.HiveCIWorkflowRun,
		"content": "",
		"tags": [][]string{
			{"a", promotionTestRepoAddress()},
			{"commit", promotionTestCommit()},
			{"branch", promotionTestBranch},
			{"workflow", ".github/workflows/hiveci-release.yml"},
			{"publisher", publisher},
		},
	})
	return string(encoded)
}

func promotionTestAcceptedRelease() domain.HiveCIAcceptedRelease {
	manifestRepo := "registry.example/metiq"
	commit := promotionTestCommit()
	repo := promotionTestRepoAddress()
	publisher := promotionTestPubkey('b')
	attestor := promotionTestPubkey('e')
	manifest := domain.HiveCIReleaseArtifact{Repository: manifestRepo, Digest: promotionTestDigest("a"), MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 1024}
	sbom := domain.HiveCIReleaseArtifact{Repository: manifestRepo, Digest: promotionTestDigest("b"), MediaType: "application/vnd.cyclonedx+json", Size: 512}
	provenance := domain.HiveCIReleaseArtifact{Repository: manifestRepo, Digest: promotionTestDigest("c"), MediaType: "application/vnd.in-toto+json", Size: 256}
	return domain.HiveCIAcceptedRelease{
		Result: domain.HiveCIReleaseResult{
			SchemaVersion:   domain.HiveCIReleaseSchemaV1,
			ResultType:      domain.HiveCIReleaseResultType,
			Status:          "success",
			ReleaseIdentity: "hiveci-release:v1:fixture",
			Lineage: domain.HiveCIReleaseLineage{
				WorkflowRunEventID: promotionTestDigest("d"),
				RepoAddress:        repo,
				Commit:             commit,
				Tree:               strings.Repeat("f", 40),
			},
			Execution: domain.HiveCIReleaseExecution{
				Complete: true, Status: "success", ExitCode: 0,
				WorkerIdentity: promotionTestPubkey('1'),
				Tests:          domain.HiveCIReleaseTestSummary{Status: "success", Total: 3, Passed: 3},
			},
			Manifest: manifest, SBOM: sbom, Provenance: provenance,
			ArtifactAttestation: domain.HiveCISignetArtifactAttestation{
				Type:         "https://sharegap.net/hiveci/signet-artifact-attestation/v1",
				SignerPubkey: attestor,
				Subjects:     []domain.HiveCIReleaseArtifact{manifest, sbom, provenance},
			},
		},
		ResultEventID:          promotionTestDigest("d"),
		Attestor:               attestor,
		Workflow:               ".github/workflows/hiveci-release.yml",
		Branch:                 promotionTestBranch,
		WorkflowRunSignedEvent: promotionTestSignedRun(publisher),
		AcceptedAt:             time.Unix(1_800_000_000, 0).UTC(),
		Policy: domain.HiveCIPipelinePolicy{
			ID: uuid.New(), RepoCoordinate: repo,
			WorkflowPath: ".github/workflows/hiveci-release.yml",
			Enabled:      true, ServiceID: uuid.New(), EnvironmentID: uuid.New(),
		},
	}
}

func promotionTestSource(orgID uuid.UUID) *domain.AgentRuntimeSource {
	return &domain.AgentRuntimeSource{
		OrgID: orgID, Repository: promotionTestRepoAddress(),
		Branch: promotionTestBranch, ReleaseChannel: promotionTestChannel,
	}
}

func promotionTestSubscription(serviceID, environmentID uuid.UUID) RuntimePromotionSubscription {
	return RuntimePromotionSubscription{
		AgentID: "scout", ServiceID: serviceID, EnvironmentID: environmentID,
		ReleaseChannel: promotionTestChannel, Repository: promotionTestRepoAddress(),
		Branch: promotionTestBranch, Publisher: promotionTestPubkey('b'),
		Commit: promotionTestCommit(), Digest: promotionTestDigest("a"),
	}
}

func TestPromoteSubscribedSoulsCreatesOneIntentPerMatchingSubscriber(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	otherServiceID, otherEnvID := uuid.New(), uuid.New()
	matched := promotionTestSubscription(serviceID, envID)
	mismatched := promotionTestSubscription(otherServiceID, otherEnvID)
	mismatched.Digest = promotionTestDigest("9")

	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{
		serviceID:      {ID: serviceID, OrgID: orgID, Name: "agent-scout"},
		otherServiceID: {ID: otherServiceID, OrgID: orgID, Name: "agent-other"},
	}}
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{matched, mismatched}},
		intentSink,
	)

	report, err := svc.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(report.Promoted) != 1 || len(report.Skipped) != 1 {
		t.Fatalf("promoted=%d skipped=%d, want 1/1", len(report.Promoted), len(report.Skipped))
	}
	if !containsReason(report.Skipped[0].Reasons, "image digest") {
		t.Fatalf("skipped reasons = %v, want digest mismatch", report.Skipped[0].Reasons)
	}
	if len(intentSink.intents) != 1 {
		t.Fatalf("intents=%d, want 1", len(intentSink.intents))
	}
	intent := intentSink.intents[0]
	if intent.ServiceID != serviceID || intent.EnvironmentID != envID || intent.SourceKind != domain.SourceKindAutoPromote {
		t.Fatalf("intent targets = %s/%s source=%s", intent.ServiceID, intent.EnvironmentID, intent.SourceKind)
	}
	if intent.Metadata["runtime_release_id"] != report.Release.ID.String() ||
		intent.Metadata["image_digest"] != promotionTestDigest("a") {
		t.Fatalf("intent metadata = %v", intent.Metadata)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusNotRequired || intent.Status != domain.IntentStatusApproved {
		t.Fatalf("unprotected intent status = %s/%s", intent.ApprovalStatus, intent.Status)
	}
	if len(releaseRepo.releases) != 1 || len(releaseRepo.bindings) != 1 {
		t.Fatalf("releases=%d bindings=%d, want 1/1", len(releaseRepo.releases), len(releaseRepo.bindings))
	}
	if releaseRepo.bindings[0].AgentID != "scout" || releaseRepo.bindings[0].ReleaseID != report.Release.ID {
		t.Fatalf("binding = %+v", releaseRepo.bindings[0])
	}
}

func TestProductionPromotionConsumerPersistsReleaseBackedIntentWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, envID := uuid.New(), uuid.New(), uuid.New()
	accepted := promotionTestAcceptedRelease()
	accepted.Policy.ServiceID = serviceID
	accepted.Policy.EnvironmentID = envID
	accepted.Policy.BranchPattern = promotionTestBranch
	accepted.Policy.Metadata = map[string]any{
		runtimePromotionEnabledMetadataKey:   true,
		runtimePromotionAgentMetadataKey:     "scout",
		runtimePromotionChannelMetadataKey:   promotionTestChannel,
		runtimePromotionPublisherMetadataKey: promotionTestPubkey('b'),
		runtimePromotionCommitMetadataKey:    promotionTestCommit(),
		runtimePromotionDigestMetadataKey:    promotionTestDigest("a"),
	}
	services := newMockServiceRepo()
	services.services[serviceID] = &domain.Service{ID: serviceID, OrgID: orgID, Name: "agent-scout"}
	environments := newMockEnvRepo()
	environments.envs[envID] = &domain.Environment{ID: envID, OrgID: orgID, Name: "staging"}
	releases := newMemoryAgentReleaseRepo()
	intents := newMockIntentRepo()
	registry := NewRegistryService(
		services, environments, nil, newMockArtifactRepo(), intents, nil, nil, nil, nil,
		&events.NoopPublisher{}, zap.NewNop(), WithAgentRuntimeReleaseRepository(releases),
	)
	promotion, err := NewProductionAgentRuntimePromotionService(
		releases, services, environments,
		fakeRuntimePromotionPolicyRepo{policies: []domain.HiveCIPipelinePolicy{accepted.Policy}},
		registry,
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := promotion.PromoteAcceptedHiveCIRelease(ctx, domain.HiveCIReleaseCommitResult{Release: accepted})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Promoted) != 1 || len(releases.releases) != 1 || len(releases.bindings) != 1 || len(intents.intents) != 1 {
		t.Fatalf("production promotion persisted promoted=%d releases=%d bindings=%d intents=%d, want 1 each",
			len(report.Promoted), len(releases.releases), len(releases.bindings), len(intents.intents))
	}
	intent := intents.intents[report.Promoted[0].Intent.ID]
	if intent.RuntimeReleaseID == nil || *intent.RuntimeReleaseID != report.Release.ID || intent.ArtifactID != uuid.Nil {
		t.Fatalf("production sink did not persist a release-backed intent: %+v", intent)
	}
}

func TestProtectedSubscriptionRequiresApproval(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	sub := promotionTestSubscription(serviceID, envID)
	sub.Protected = true
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: orgID}}}
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{sub}},
		intentSink,
	)
	if _, err := svc.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(intentSink.intents) != 1 {
		t.Fatalf("intents=%d, want 1", len(intentSink.intents))
	}
	if intentSink.intents[0].ApprovalStatus != domain.ApprovalStatusPending ||
		intentSink.intents[0].Status != domain.IntentStatusPending {
		t.Fatalf("protected intent status = %s/%s, want pending", intentSink.intents[0].ApprovalStatus, intentSink.intents[0].Status)
	}
}

func TestPromotionReplayIsIdempotent(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: orgID}}}
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, envID)}},
		intentSink,
	)
	source := promotionTestSource(orgID)
	release := promotionTestAcceptedRelease()
	for i := 0; i < 2; i++ {
		if _, err := svc.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{Source: source, Release: release}); err != nil {
			t.Fatalf("promote run %d: %v", i, err)
		}
	}
	if len(releaseRepo.releases) != 1 {
		t.Fatalf("shared releases=%d, want 1", len(releaseRepo.releases))
	}
	if len(releaseRepo.bindings) != 1 {
		t.Fatalf("bindings=%d, want 1 after replay", len(releaseRepo.bindings))
	}
	if len(intentSink.intents) != 1 {
		t.Fatalf("intents=%d, want exactly 1 after replay", len(intentSink.intents))
	}
}

func TestPromotionBindFailureThenRetryCreatesExactlyOneIntent(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	releaseRepo := &failOnceBindReleaseRepo{memoryAgentReleaseRepo: newMemoryAgentReleaseRepo(), fail: true}
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: orgID}}}
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, envID)}},
		intentSink,
	)
	req := RuntimePromotionRequest{Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease()}
	if _, err := svc.PromoteSubscribedSouls(context.Background(), req); err == nil {
		t.Fatal("first promotion succeeded despite injected binding failure")
	}
	if len(intentSink.intents) != 0 {
		t.Fatalf("intent was submitted before binding: count=%d", len(intentSink.intents))
	}
	if _, err := svc.PromoteSubscribedSouls(context.Background(), req); err != nil {
		t.Fatalf("retry promotion: %v", err)
	}
	if len(releaseRepo.bindings) != 1 || len(intentSink.intents) != 1 {
		t.Fatalf("bindings=%d intents=%d, want exactly 1/1 after retry", len(releaseRepo.bindings), len(intentSink.intents))
	}
}

func TestPromotionRestartAfterDurableIntentWriteCreatesExactlyOneIntent(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: orgID}}}
	intentSink := &recordingPromotionIntentSink{failAfterPersist: true}
	subscriptions := fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, envID)}}
	source := promotionTestSource(orgID)
	release := promotionTestAcceptedRelease()
	first := NewAgentRuntimePromotionService(NewAgentRuntimeReleaseService(releaseRepo, services), subscriptions, intentSink)
	if _, err := first.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{Source: source, Release: release}); err == nil {
		t.Fatal("first promotion succeeded despite injected post-persist failure")
	}
	if len(releaseRepo.bindings) != 1 || len(intentSink.intents) != 1 {
		t.Fatalf("mid-flight state bindings=%d intents=%d, want 1/1", len(releaseRepo.bindings), len(intentSink.intents))
	}

	// A new service instance has no process-local replay state. Durable binding
	// replay plus deterministic intent identity must still converge to one row.
	restarted := NewAgentRuntimePromotionService(NewAgentRuntimeReleaseService(releaseRepo, services), subscriptions, intentSink)
	if _, err := restarted.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{Source: source, Release: release}); err != nil {
		t.Fatalf("restart retry promotion: %v", err)
	}
	if len(releaseRepo.bindings) != 1 || len(intentSink.intents) != 1 {
		t.Fatalf("restart state bindings=%d intents=%d, want exactly 1/1", len(releaseRepo.bindings), len(intentSink.intents))
	}
}

func TestEvaluatePromotionGateRejectsEachMismatch(t *testing.T) {
	orgID := uuid.New()
	source := *promotionTestSource(orgID)
	accepted := promotionTestAcceptedRelease()
	base := promotionTestSubscription(uuid.New(), uuid.New())

	cases := []struct {
		name   string
		mutate func(*RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease
		reason string
	}{
		{name: "repository", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.Repository = "30617:" + promotionTestPubkey('a') + ":other"
			return &accepted
		}, reason: "repository"},
		{name: "branch", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.Branch = "develop"
			return &accepted
		}, reason: "branch"},
		{name: "publisher", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.Publisher = promotionTestPubkey('7')
			return &accepted
		}, reason: "publisher"},
		{name: "commit", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.Commit = strings.Repeat("d", 40)
			return &accepted
		}, reason: "commit"},
		{name: "digest", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.Digest = promotionTestDigest("9")
			return &accepted
		}, reason: "digest"},
		{name: "channel", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			s.ReleaseChannel = "edge"
			return &accepted
		}, reason: "channel"},
		{name: "disabled policy", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			bad := accepted
			bad.Policy.Enabled = false
			return &bad
		}, reason: "policy"},
		{name: "red tests", mutate: func(s *RuntimePromotionSubscription) *domain.HiveCIAcceptedRelease {
			bad := accepted
			bad.Result.Execution.Tests = domain.HiveCIReleaseTestSummary{Status: "failure", Total: 3, Passed: 2, Failed: 1}
			return &bad
		}, reason: "test gate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := base
			decision := tc.mutate(&sub)
			got := NewAgentRuntimePromotionService(nil, nil, nil).EvaluatePromotionGate(*decision, source, sub)
			if got.Matched {
				t.Fatalf("gate matched, want rejection")
			}
			if !containsReason(got.Reasons, tc.reason) {
				t.Fatalf("reasons = %v, want one containing %q", got.Reasons, tc.reason)
			}
		})
	}
}

func TestPromotionAbortsOnUnmappableSubscription(t *testing.T) {
	orgID := uuid.New()
	sub := promotionTestSubscription(uuid.Nil, uuid.New())
	releaseRepo := newMemoryAgentReleaseRepo()
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, memoryServiceRepo{}),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{sub}},
		intentSink,
	)
	_, err := svc.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
	})
	if err == nil || len(intentSink.intents) != 0 {
		t.Fatalf("err=%v intents=%d, want rejection before intent", err, len(intentSink.intents))
	}
}

func TestRepositoryStateTriggerIsReplaySafe(t *testing.T) {
	svc := NewAgentRuntimePromotionService(nil, nil, nil)
	event := &nostr.Event{
		Kind: nostr.Kind(kinds.NIP34RepositoryState),
		Tags: nostr.Tags{{"a", promotionTestRepoAddress()}, {"commit", promotionTestCommit()}},
	}
	first, err := svc.HandleRepositoryStateTrigger(event)
	if err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	if first.Replay || first.Fingerprint == "" {
		t.Fatalf("first trigger replay=%v fingerprint=%q", first.Replay, first.Fingerprint)
	}
	second, err := svc.HandleRepositoryStateTrigger(event)
	if err != nil {
		t.Fatalf("second trigger: %v", err)
	}
	if !second.Replay || second.Fingerprint != first.Fingerprint {
		t.Fatalf("second trigger replay=%v fingerprint=%q", second.Replay, second.Fingerprint)
	}
	if _, err := svc.HandleRepositoryStateTrigger(&nostr.Event{Kind: nostr.Kind(kinds.HiveCIWorkflowRun)}); err == nil {
		t.Fatal("non-30618 event accepted, want error")
	}
}

func TestSecondaryTriggerCannotWidenPromotionAuthority(t *testing.T) {
	orgID := uuid.New()
	serviceID, envID := uuid.New(), uuid.New()
	releaseRepo := newMemoryAgentReleaseRepo()
	services := memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: {ID: serviceID, OrgID: orgID}}}
	intentSink := &recordingPromotionIntentSink{}
	svc := NewAgentRuntimePromotionService(
		NewAgentRuntimeReleaseService(releaseRepo, services),
		fakeRuntimeSubscriptionSource{subs: []RuntimePromotionSubscription{promotionTestSubscription(serviceID, envID)}},
		intentSink,
	)
	_, err := svc.PromoteSubscribedSouls(context.Background(), RuntimePromotionRequest{
		Source: promotionTestSource(orgID), Release: promotionTestAcceptedRelease(),
		Trigger: &RepositoryStateTrigger{RepoAddress: promotionTestRepoAddress(), Commit: strings.Repeat("9", 40), Fingerprint: "conflict"},
	})
	if err == nil {
		t.Fatal("conflicting trigger accepted, want rejection")
	}
	if len(intentSink.intents) != 0 || len(releaseRepo.releases) != 0 {
		t.Fatalf("intents=%d releases=%d, want no mutation", len(intentSink.intents), len(releaseRepo.releases))
	}
}

func containsReason(reasons []string, needle string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, needle) {
			return true
		}
	}
	return false
}
