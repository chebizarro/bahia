package service

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestCreateDeploymentIntent_AutoApprovedPublishesApprovedEvent(t *testing.T) {
	ctx := context.Background()
	svcRepo := newMockServiceRepo()
	envRepo := newMockEnvRepo()
	buildRepo := newMockBuildRepo()
	artRepo := newMockArtifactRepo()
	intentRepo := newMockIntentRepo()
	runRepo := newMockRunRepo()
	obsRepo := newMockObsRepo()
	stateRepo := newMockStateRepo()
	publisher := events.NewInProcessPublisher(zap.NewNop())

	approved := make(chan events.Event, 1)
	publisher.Subscribe(events.EventDeploymentIntentApproved, func(_ context.Context, e events.Event) {
		approved <- e
	})

	registry := NewRegistryService(
		svcRepo, envRepo, buildRepo, artRepo,
		intentRepo, runRepo, obsRepo, stateRepo,
		echoDigestVerifier{}, publisher, zap.NewNop(),
	)

	svc, env := seedServiceAndEnv(t, registry)
	artifact := seedArtifact(t, registry, svc, "sha256:autoapprove")
	if env.Protected {
		t.Fatal("test fixture expected unprotected environment")
	}

	intent := &domain.DeploymentIntent{
		ServiceID:     svc.ID,
		EnvironmentID: env.ID,
		ArtifactID:    artifact.ID,
		RequestedBy:   "test",
		SourceKind:    domain.SourceKindManual,
	}

	if err := registry.CreateDeploymentIntent(ctx, intent); err != nil {
		t.Fatalf("CreateDeploymentIntent() error = %v", err)
	}

	select {
	case evt := <-approved:
		if evt.EntityID != intent.ID.String() {
			t.Fatalf("approved event entity_id = %s, want %s", evt.EntityID, intent.ID.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for deployment_intent.approved event")
	}
}
