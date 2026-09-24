package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestRuntimeReleaseDeploymentApprovalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		required, unavailable bool
	}{
		{name: "require approval", required: true},
		{name: "no gate"},
		{name: "policy unavailable", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			orgID, sourceID, serviceID, envID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			releases := newMemoryAgentReleaseRepo()
			releases.sources[sourceID] = domain.AgentRuntimeSource{ID: sourceID, OrgID: orgID, ReleaseChannel: "stable"}
			release := verifiedRuntimeRelease(orgID, sourceID, "1")
			release.ID = uuid.New()
			releases.releases[release.ID] = release
			svc := domain.Service{ID: serviceID, OrgID: orgID, Name: "soul"}
			releaseService := NewAgentRuntimeReleaseService(releases, memoryServiceRepo{values: map[uuid.UUID]domain.Service{serviceID: svc}})
			if err := releaseService.BindRelease(ctx, &domain.AgentServiceReleaseBinding{
				OrgID: orgID, AgentID: "agent", ServiceID: serviceID, ReleaseID: release.ID, ReleaseChannel: "stable", SourceEventID: "binding",
			}); err != nil {
				t.Fatal(err)
			}
			services, environments, intents := newMockServiceRepo(), newMockEnvRepo(), newMockIntentRepo()
			services.services[serviceID] = &svc
			environments.envs[envID] = &domain.Environment{ID: envID, OrgID: orgID, Name: "unprotected"}
			policy := &stubApprovalPolicy{required: tc.required}
			if tc.unavailable {
				policy.err = errors.New("policy store unavailable")
			}
			registry := NewRegistryService(services, environments, nil, nil, intents, nil, nil, nil, nil, &events.NoopPublisher{}, zap.NewNop(), WithAgentRuntimeReleaseRepository(releases), WithDeploymentApprovalPolicy(policy))
			intent := &domain.DeploymentIntent{
				ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID,
				RequestedBy: "operator", SourceKind: domain.SourceKindAutoPromote,
				Status: domain.IntentStatusApproved, ApprovalStatus: domain.ApprovalStatusApproved,
			}
			resolved, err := registry.CreateDeploymentIntentForRuntimeRelease(ctx, release.ID, intent)
			if tc.unavailable {
				if err == nil || resolved != nil || len(intents.intents) != 0 {
					t.Fatalf("policy error allowed intent: resolved=%+v err=%v", resolved, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantApproval := domain.IntentStatusApproved, domain.ApprovalStatusNotRequired
			if tc.required {
				wantStatus, wantApproval = domain.IntentStatusPending, domain.ApprovalStatusPending
			}
			if resolved.Intent.Status != wantStatus || resolved.Intent.ApprovalStatus != wantApproval {
				t.Fatalf("unexpected gate result: %+v", resolved.Intent)
			}
			if policy.calls != 1 {
				t.Fatalf("policy calls=%d", policy.calls)
			}
		})
	}
}
