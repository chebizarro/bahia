package nostr

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type supervisionContractResolver struct{ runtime runtime.Runtime }

func (r supervisionContractResolver) Resolve(*domain.Service, *domain.Environment) (runtime.Runtime, error) {
	return r.runtime, nil
}

// TestSupervisionSourcesReadRelayFirstRecords pins the contract between the
// cp-state record builders and the supervisors' local-store sources (B-33,
// B-34): the records the daemon publishes for a service, its environment and
// its desired state are exactly what route-canary and managed-instance
// supervision enumerate, with no repository in between.
func TestSupervisionSourcesReadRelayFirstRecords(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	secret := gonostr.Generate()
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	save := func(legacyKind int, id string, deleted bool, tags gonostr.Tags, content string) {
		t.Helper()
		wireKind, envelope := controlStateEnvelope(legacyKind, id, deleted)
		event := gonostr.Event{Kind: gonostr.Kind(wireKind), CreatedAt: gonostr.Timestamp(at.Unix()), Tags: append(envelope, tags...), Content: content}
		require.NoError(t, event.Sign(secret))
		stored, err := store.SaveEvent(event)
		require.NoError(t, err)
		require.True(t, stored)
		at = at.Add(time.Second)
	}

	svc := &domain.Service{ID: uuid.New(), OrgID: uuid.New(), Name: "git", ArtifactRepo: "registry.example/git", DefaultBranch: "main", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: at, UpdatedAt: at}
	env := &domain.Environment{ID: uuid.New(), OrgID: svc.OrgID, Name: "production", RuntimeConfig: map[string]any{"type": "docker"}, CreatedAt: at, UpdatedAt: at}
	unit := domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: env.ID, Key: domain.DefaultDeploymentUnitKey, RuntimeType: domain.RuntimeTypeDocker, OwnershipMode: domain.OwnershipModeBahiaManaged, CreatedAt: at, UpdatedAt: at}
	artifactID := uuid.New()
	route := &domain.DesiredPublicRoutePlan{SchemaVersion: "1", ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: unit.ID, Hostname: "git.example.net", Zone: "example.net", Proxy: domain.DesiredPublicRouteProxy{HealthPath: "/healthz"}}
	state := &domain.EnvironmentServiceState{
		ServiceID: svc.ID, EnvironmentID: env.ID, DesiredArtifactID: &artifactID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: at,
		DesiredRuntimeState: &domain.DesiredServiceSpec{ServiceID: svc.ID, EnvironmentID: env.ID, ArtifactID: artifactID, StableServiceKey: "git", PublicRoute: route},
	}

	serviceTags, serviceContent := serviceRegistryRecord(svc, false)
	save(KindServiceRegistry, svc.ID.String(), false, serviceTags, serviceContent)
	environmentTags, environmentContent := environmentRegistryRecord(env, []domain.DeploymentUnit{unit}, false)
	save(KindEnvironmentRegistry, env.ID.String(), false, environmentTags, environmentContent)
	stateTags, stateContent := RuntimeStateRecord(state, nil)
	save(KindServiceState, ServiceStateDTag(svc.ID, env.ID), false, stateTags, stateContent)

	local, err := service.NewLocalSupervisionState(store, secret.Public().Hex())
	require.NoError(t, err)
	routes := service.LocalRoutePlanSource{State: local}
	instances := &service.LocalSupervisionSpecSource{State: local, Resolver: supervisionContractResolver{runtime: runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())}}
	ctx := context.Background()

	plans, err := routes.ListManagedRoutePlans(ctx)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.Equal(t, route, plans[0], "the signed route plan is probed as published")

	specs, err := instances.SupervisionSpecs(ctx)
	require.NoError(t, err)
	require.Len(t, specs, 1)
	require.Equal(t, domain.ManagedInstanceKey{ServiceID: svc.ID, EnvironmentID: env.ID, DeploymentUnitID: unit.ID, RuntimeTargetName: svc.RuntimeTargetName()}, specs[0].Key)
	require.True(t, specs[0].DesiredRunning)
	require.Equal(t, "production", specs[0].Host)

	// The state publisher's tombstone withdraws both.
	tombstoneTags, tombstoneContent := RuntimeStateTombstoneRecord(svc.ID, env.ID)
	save(KindServiceState, ServiceStateDTag(svc.ID, env.ID), true, tombstoneTags, tombstoneContent)
	plans, err = routes.ListManagedRoutePlans(ctx)
	require.NoError(t, err)
	require.Empty(t, plans)
	specs, err = instances.SupervisionSpecs(ctx)
	require.NoError(t, err)
	require.Empty(t, specs)
}
