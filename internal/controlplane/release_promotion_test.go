package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

type promotionRegistryFake struct {
	artifacts map[uuid.UUID]*domain.Artifact
	state     *domain.EnvironmentServiceState
	intents   []domain.DeploymentIntent
}

func (f *promotionRegistryFake) GetArtifact(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return f.artifacts[id], nil
}
func (f *promotionRegistryFake) GetEnvironmentServiceState(context.Context, uuid.UUID, uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return f.state, nil
}
func (f *promotionRegistryFake) GetDeploymentIntentByReleasePromotionKey(
	_ context.Context,
	serviceID, environmentID uuid.UUID,
	requester, idempotencyKey string,
) (*domain.DeploymentIntent, error) {
	for index := range f.intents {
		intent := &f.intents[index]
		if intent.ServiceID == serviceID && intent.EnvironmentID == environmentID &&
			intent.Metadata["promotion_requester"] == requester &&
			intent.Metadata["promotion_idempotency_key"] == idempotencyKey {
			return intent, nil
		}
	}
	return nil, nil
}

type promotionAuditStoreFake struct {
	records []*repository.NostrEventRecord
}

func (f *promotionAuditStoreFake) Record(_ context.Context, record *repository.NostrEventRecord) (bool, error) {
	f.records = append(f.records, record)
	return true, nil
}

type promotionFixture struct {
	serviceID, environmentID uuid.UUID
	release, previous        *domain.Artifact
	env                      *domain.Environment
	event                    *nostr.Event
	params                   map[string]any
	registry                 *promotionRegistryFake
	authorizer               *ReleasePromotionAuthorizer
}

func newPromotionFixture(t *testing.T) *promotionFixture {
	t.Helper()
	serviceID, environmentID := uuid.New(), uuid.New()
	previous := &domain.Artifact{
		ID: uuid.New(), ServiceID: serviceID, ImageRepo: "harbor.example/team/bahia",
		ImageDigest: "sha256:" + strings.Repeat("1", 64),
	}
	policy := domain.HiveCIPipelinePolicy{
		ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
		RepoCoordinate: "30617:" + strings.Repeat("a", 64) + ":bahia",
		WorkflowPath:   ".gitea/workflows/release.yml", Enabled: true,
	}
	releaseDigest := "sha256:" + strings.Repeat("2", 64)
	releaseIdentity := domain.HiveCIReleaseIdentityPrefix + strings.Repeat("3", 64)
	release := &domain.Artifact{
		ID: uuid.New(), ServiceID: serviceID, ImageRepo: "harbor.example/team/bahia",
		ImageDigest: releaseDigest, ImageTag: "",
		Metadata: map[string]any{
			"registration_mode":         "hiveci_release_digest",
			"promotion_authority":       "required",
			"ci_mutates_desired_state":  false,
			"release_identity":          releaseIdentity,
			"signed_release_event":      `{"kind":5402}`,
			"signed_workflow_run_event": `{"kind":5401}`,
			"policy":                    policy,
			"verification": map[string]any{
				"source": "oci_digest", "state": "verified",
				"manifest":   domain.HiveCIReleaseArtifact{Repository: "harbor.example/team/bahia", Digest: releaseDigest, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100},
				"sbom":       domain.HiveCIReleaseArtifact{Repository: "harbor.example/team/bahia", Digest: "sha256:" + strings.Repeat("4", 64), MediaType: "application/vnd.cyclonedx+json", Size: 200},
				"provenance": domain.HiveCIReleaseArtifact{Repository: "harbor.example/team/bahia", Digest: "sha256:" + strings.Repeat("5", 64), MediaType: "application/vnd.in-toto+json", Size: 300},
			},
			"rollback_compatibility": map[string]any{"compatible_from_digests": []any{previous.ImageDigest}},
			"health_readiness_contracts": map[string]any{
				"health":    map[string]any{"type": "http", "path": "/health", "timeout_seconds": 10},
				"readiness": map[string]any{"type": "http", "path": "/ready", "timeout_seconds": 15},
			},
		},
	}
	event := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Content: "{}"}
	if err := event.Sign(nostr.Generate()); err != nil {
		t.Fatal(err)
	}
	registry := &promotionRegistryFake{
		artifacts: map[uuid.UUID]*domain.Artifact{release.ID: release, previous.ID: previous},
		state:     &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: environmentID, DesiredArtifactID: &previous.ID},
	}
	return &promotionFixture{
		serviceID: serviceID, environmentID: environmentID, release: release, previous: previous,
		env:   &domain.Environment{ID: environmentID, Name: "staging", DeployStrategy: domain.DeployStrategyCanary},
		event: event,
		params: map[string]any{
			"release_identity": releaseIdentity, "artifact_digest": releaseDigest,
			"previous_artifact_digest": previous.ImageDigest,
		},
		registry: registry, authorizer: NewReleasePromotionAuthorizer(registry, nil),
	}
}

func TestReleasePromotionAuthorizerAcceptsExactReplayAndRejectsConflict(t *testing.T) {
	f := newPromotionFixture(t)
	decision, err := f.authorizer.Authorize(context.Background(), f.event, f.params,
		f.serviceID, f.environmentID, f.release.ID, "canary", "promote-1", f.env)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Replay || decision.Fingerprint == "" || decision.Metadata["promotion_strategy"] != "canary" {
		t.Fatalf("decision=%+v", decision)
	}
	existing := domain.DeploymentIntent{
		ID: uuid.New(), ServiceID: f.serviceID, EnvironmentID: f.environmentID, ArtifactID: f.release.ID,
		Status: domain.IntentStatusApproved, Metadata: decision.Metadata,
	}
	f.registry.intents = []domain.DeploymentIntent{existing}
	f.registry.state.DesiredArtifactID = &f.release.ID
	f.registry.state.DesiredIntentID = &existing.ID
	replay, err := f.authorizer.Authorize(context.Background(), f.event, f.params,
		f.serviceID, f.environmentID, f.release.ID, "canary", "promote-1", f.env)
	if err != nil || !replay.Replay || replay.ExistingIntent == nil || replay.ExistingIntent.ID != existing.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	f.registry.intents[0].Metadata["promotion_fingerprint"] = "sha256:" + strings.Repeat("f", 64)
	if _, err := f.authorizer.Authorize(context.Background(), f.event, f.params,
		f.serviceID, f.environmentID, f.release.ID, "canary", "promote-1", f.env); !errors.Is(err, ErrPromotionReplayConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}

func TestReleasePromotionAuthorizerFailsClosedWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*promotionFixture)
	}{
		{"mutable tag", func(f *promotionFixture) { f.release.ImageTag = "latest" }},
		{"wrong environment policy", func(f *promotionFixture) {
			policy := f.release.Metadata["policy"].(domain.HiveCIPipelinePolicy)
			policy.EnvironmentID = uuid.New()
			f.release.Metadata["policy"] = policy
		}},
		{"non-canary environment", func(f *promotionFixture) { f.env.DeployStrategy = domain.DeployStrategyReplace }},
		{"missing SBOM", func(f *promotionFixture) { delete(f.release.Metadata["verification"].(map[string]any), "sbom") }},
		{"missing provenance", func(f *promotionFixture) { delete(f.release.Metadata["verification"].(map[string]any), "provenance") }},
		{"missing signed release", func(f *promotionFixture) { delete(f.release.Metadata, "signed_release_event") }},
		{"missing signed workflow run", func(f *promotionFixture) { delete(f.release.Metadata, "signed_workflow_run_event") }},
		{"missing health contract", func(f *promotionFixture) {
			delete(f.release.Metadata["health_readiness_contracts"].(map[string]any), "health")
		}},
		{"missing readiness contract", func(f *promotionFixture) {
			delete(f.release.Metadata["health_readiness_contracts"].(map[string]any), "readiness")
		}},
		{"rollback incompatible", func(f *promotionFixture) { f.release.Metadata["rollback_compatibility"] = map[string]any{} }},
		{"stale previous digest", func(f *promotionFixture) { f.params["previous_artifact_digest"] = "sha256:" + strings.Repeat("9", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newPromotionFixture(t)
			test.mutate(f)
			if _, err := f.authorizer.Authorize(context.Background(), f.event, f.params,
				f.serviceID, f.environmentID, f.release.ID, "canary", "promote-1", f.env); err == nil {
				t.Fatal("expected fail-closed rejection")
			}
			if len(f.registry.intents) != 0 {
				t.Fatal("rejection changed deployment intent state")
			}
		})
	}
}

func TestSignedReleasePromotionAuditUsesCanonicalOutbox(t *testing.T) {
	store := &promotionAuditStoreFake{}
	audit := NewSignedReleasePromotionAudit(keyer.NewPlainKeySigner(nostr.Generate()), store)
	audit.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	decision := ReleasePromotionDecision{
		ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + strings.Repeat("3", 64),
		ArtifactDigest:  "sha256:" + strings.Repeat("2", 64),
		IdempotencyKey:  "promote-1", Requester: strings.Repeat("a", 64),
		RequestEventID: strings.Repeat("b", 64),
	}
	if err := audit.AuditPromotionDecision(context.Background(), decision, "accepted", nil); err != nil {
		t.Fatal(err)
	}
	if err := audit.AuditPromotionDecision(context.Background(), decision, "rejected", errors.New("unauthorized")); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 2 {
		t.Fatalf("records=%d", len(store.records))
	}
	for _, record := range store.records {
		if record.Kind != cascadia.CAS_AUDIT || record.PublishState != repository.NostrPublishStatePending ||
			record.ID == "" || record.Sig == "" {
			t.Fatalf("invalid promotion audit record: %+v", record)
		}
	}
}
