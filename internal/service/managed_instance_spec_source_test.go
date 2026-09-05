package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type supervisionResolverFake struct {
	runtime runtime.Runtime
}

func (r supervisionResolverFake) Resolve(*domain.Service, *domain.Environment) (runtime.Runtime, error) {
	return r.runtime, nil
}

func TestRepositorySupervisionSpecSourceFailsClosedForStoppedOrUnknownDesiredState(t *testing.T) {
	serviceID, environmentID, artifactID := uuid.New(), uuid.New(), uuid.New()
	svc := &domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}
	env := &domain.Environment{ID: environmentID, Name: "production", RuntimeConfig: map[string]any{"type": "docker", "endpoint": "tcp://user:password@private-host:2376"}}
	services, environments := newMockServiceRepo(), newMockEnvRepo()
	services.services[serviceID] = svc
	environments.envs[environmentID] = env
	adapter := runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())

	for _, tc := range []struct {
		name    string
		desired *domain.DesiredServiceSpec
		want    bool
	}{
		{name: "operator stopped", desired: nil, want: false},
		{name: "unknown desired state", desired: &domain.DesiredServiceSpec{}, want: false},
		{name: "complete running desired state", desired: &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			states := newMockStateRepo()
			states.states[stateKey(serviceID, environmentID)] = &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: environmentID, DesiredArtifactID: &artifactID, DesiredRuntimeState: tc.desired}
			source := &RepositorySupervisionSpecSource{States: states, Services: services, Environments: environments, Units: &mockDeploymentUnitRepo{}, Resolver: supervisionResolverFake{runtime: adapter}}

			specs, err := source.SupervisionSpecs(context.Background())
			require.NoError(t, err)
			require.Len(t, specs, 1)
			require.Equal(t, tc.want, specs[0].DesiredRunning)
			if !tc.want {
				decision := domain.EvaluateRecovery(specs[0].DesiredRunning, domain.ManagedInstanceHealth{Status: domain.InstanceHealthStatusStopped}, domain.RecoveryPolicy{Enabled: true, RestartBudget: domain.RestartBudget{MaxAttempts: 1}}, domain.RestartBudget{MaxAttempts: 1}, nil)
				require.Equal(t, domain.RecoveryDecisionIntentionallyStopped, decision.Action)
			}
			require.Equal(t, uuid.Nil, specs[0].Key.DeploymentUnitID)
			require.Equal(t, "production", specs[0].Host)
		})
	}
}
