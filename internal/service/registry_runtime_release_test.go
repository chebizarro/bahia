package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestCreateDeploymentIntentForRuntimeReleaseFanoutReplayAndRollbackResolution(t *testing.T) {
	ctx := context.Background()
	orgID, sourceID := uuid.New(), uuid.New()
	serviceA, serviceB := uuid.New(), uuid.New()
	environmentA, environmentB := uuid.New(), uuid.New()

	releases := newMemoryAgentReleaseRepo()
	releases.sources[sourceID] = domain.AgentRuntimeSource{ID: sourceID, OrgID: orgID, ReleaseChannel: "stable"}
	releaseA := verifiedRuntimeRelease(orgID, sourceID, "1")
	releaseA.ID = uuid.New()
	releaseB := verifiedRuntimeRelease(orgID, sourceID, "2")
	releaseB.ID = uuid.New()
	releases.releases[releaseA.ID] = releaseA
	releases.releases[releaseB.ID] = releaseB

	serviceValues := map[uuid.UUID]domain.Service{
		serviceA: {ID: serviceA, OrgID: orgID, Name: "soul-a"},
		serviceB: {ID: serviceB, OrgID: orgID, Name: "soul-b"},
	}
	releaseService := NewAgentRuntimeReleaseService(releases, memoryServiceRepo{values: serviceValues})
	for _, binding := range []*domain.AgentServiceReleaseBinding{
		{OrgID: orgID, AgentID: "agent-a", ServiceID: serviceA, ReleaseID: releaseA.ID, ReleaseChannel: "stable", SourceEventID: "bind-a-v1"},
		{OrgID: orgID, AgentID: "agent-b", ServiceID: serviceB, ReleaseID: releaseA.ID, ReleaseChannel: "stable", SourceEventID: "bind-b-v1"},
	} {
		if err := releaseService.BindRelease(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}

	services := newMockServiceRepo()
	services.services[serviceA] = &domain.Service{ID: serviceA, OrgID: orgID, Name: "soul-a"}
	services.services[serviceB] = &domain.Service{ID: serviceB, OrgID: orgID, Name: "soul-b"}
	environments := newMockEnvRepo()
	environments.envs[environmentA] = &domain.Environment{ID: environmentA, OrgID: orgID, Name: "stage-a"}
	environments.envs[environmentB] = &domain.Environment{ID: environmentB, OrgID: orgID, Name: "stage-b", Protected: true}
	artifacts := newMockArtifactRepo()
	intents := newMockIntentRepo()
	registry := NewRegistryService(
		services, environments, nil, artifacts, intents, nil, nil, nil, nil,
		&events.NoopPublisher{}, zap.NewNop(), WithAgentRuntimeReleaseRepository(releases),
	)

	create := func(serviceID, environmentID uuid.UUID) *domain.RuntimeReleaseDeploymentIntent {
		t.Helper()
		resolved, err := registry.CreateDeploymentIntentForRuntimeRelease(ctx, releaseA.ID, &domain.DeploymentIntent{
			ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID,
			RequestedBy: "soul-factory-promotion", SourceKind: domain.SourceKindAutoPromote,
		})
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	intentA := create(serviceA, environmentA)
	intentB := create(serviceB, environmentB)
	if intentA.Intent.ID == intentB.Intent.ID || len(intents.intents) != 2 {
		t.Fatalf("shared release fan-out intents=%d, ids=%s/%s", len(intents.intents), intentA.Intent.ID, intentB.Intent.ID)
	}
	if len(artifacts.artifacts) != 0 {
		t.Fatalf("release-backed intent fabricated %d artifact rows", len(artifacts.artifacts))
	}
	if intentA.Intent.ArtifactID != uuid.Nil || intentA.Intent.RuntimeReleaseID == nil || *intentA.Intent.RuntimeReleaseID != releaseA.ID {
		t.Fatalf("intent did not retain direct runtime release identity: %+v", intentA.Intent)
	}
	if intentA.Release.ImageDigest != releaseA.ImageDigest || intentA.Release.Provenance != releaseA.Provenance {
		t.Fatalf("resolved release evidence changed: %+v", intentA.Release)
	}
	if intentB.Intent.Status != domain.IntentStatusPending || intentB.Intent.ApprovalStatus != domain.ApprovalStatusPending {
		t.Fatalf("protected release intent bypassed approval: %+v", intentB.Intent)
	}

	replayIntent := &domain.DeploymentIntent{
		ID: uuid.New(), ServiceID: serviceA, EnvironmentID: environmentA,
		RequestedBy: "soul-factory-promotion", SourceKind: domain.SourceKindAutoPromote,
		Metadata: map[string]any{"runtime_release_id": releaseA.ID.String()},
	}
	if err := registry.SubmitPromotionIntent(ctx, replayIntent); err != nil {
		t.Fatal(err)
	}
	if replayIntent.ID != intentA.Intent.ID || len(intents.intents) != 2 {
		t.Fatalf("replay created a duplicate: first=%s replay=%s count=%d", intentA.Intent.ID, replayIntent.ID, len(intents.intents))
	}

	if err := releaseService.BindRelease(ctx, &domain.AgentServiceReleaseBinding{
		OrgID: orgID, AgentID: "agent-a", ServiceID: serviceA, ReleaseID: releaseB.ID,
		ReleaseChannel: "stable", SourceEventID: "bind-a-v2",
	}); err != nil {
		t.Fatal(err)
	}
	rollback, err := releaseService.GetRollbackRelease(ctx, orgID, "agent-a", serviceA, "stable")
	if err != nil || rollback == nil || rollback.Release.ID != releaseA.ID {
		t.Fatalf("rollback release=%+v err=%v", rollback, err)
	}
	rollbackIntent, err := registry.GetDeploymentIntentForRuntimeRelease(ctx, orgID, serviceA, rollback.Release.ID)
	if err != nil || rollbackIntent == nil || rollbackIntent.Release.ImageDigest != releaseA.ImageDigest || rollbackIntent.Release.Provenance != releaseA.Provenance {
		t.Fatalf("rollback digest/provenance not resolvable: %+v err=%v", rollbackIntent, err)
	}
}
