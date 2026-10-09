package reconcile

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func saveCanonicalRuntimeEvent(t *testing.T, store *localstore.Store, key gonostr.SecretKey, kind int, coordinate string, deleted bool, content string, at gonostr.Timestamp, extra gonostr.Tags) gonostr.Event {
	t.Helper()
	wire, tags := nostrAdapter.ControlStateEnvelope(kind, coordinate, deleted)
	event := gonostr.Event{Kind: gonostr.Kind(wire), CreatedAt: at, Tags: append(tags, extra...), Content: content}
	require.NoError(t, event.Sign(key))
	_, err := store.SaveEvent(event)
	require.NoError(t, err)
	return event
}

func canonicalRuntimeFixture(t *testing.T, store *localstore.Store, key gonostr.SecretKey, serviceID, envID uuid.UUID, desiredHash, host string) {
	t.Helper()
	service := domain.Service{ID: serviceID, Name: "canonical-api", RuntimeType: domain.RuntimeTypeDocker}
	environment := domain.Environment{ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}
	for _, item := range []struct {
		kind  int
		id    string
		value any
	}{
		{kinds.ServiceRegistry, serviceID.String(), service},
		{kinds.EnvironmentRegistry, envID.String(), environment},
	} {
		content, err := json.Marshal(item.value)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(content, &fields))
		fields["deleted"] = false
		content, err = json.Marshal(fields)
		require.NoError(t, err)
		saveCanonicalRuntimeEvent(t, store, key, item.kind, item.id, false, string(content), 100, nil)
	}
	if desiredHash != "" {
		observation := &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID,
			ObservedHost: host, NormalizedHash: desiredHash, HealthStatus: domain.HealthStatusHealthy,
			Source: "runtime", ObservedAt: time.Unix(100, 0).UTC()}
		state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID,
			CurrentObservationID: &observation.ID, DriftStatus: domain.DriftStatusInSync, DesiredHash: desiredHash}
		tags, content := nostrAdapter.RuntimeStateRecord(state, observation)
		saveCanonicalRuntimeEvent(t, store, key, kinds.ServiceState,
			nostrAdapter.ServiceStateDTag(serviceID, envID), false, content, 101, tags)
	}
}

func TestCanonicalRuntimeViewsIgnoreAbsentAndDivergentSQL(t *testing.T) {
	ctx := context.Background()
	serviceID, envID := uuid.New(), uuid.New()
	sqlOnlyID := uuid.New()
	divergentServices := &fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "sql-evil"}, {ID: sqlOnlyID, Name: "sql-only"}}}
	divergentEnvs := &fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "sql-zone"}}}
	divergentStates := &fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync, DesiredHash: "sql-hash"}, {ServiceID: sqlOnlyID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync}}}
	divergentObs := &fakeObservationRepo{latest: map[string]*domain.RuntimeObservation{dnsTestStateKey(serviceID, envID): {ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, ObservedHost: "203.0.113.99", HealthStatus: domain.HealthStatusHealthy}}}

	for _, index := range []struct {
		name     string
		services *fakeServiceRepo
		envs     *fakeEnvironmentRepo
		states   *fakeStateRepo
		obs      *fakeObservationRepo
	}{
		{name: "absent"},
		{name: "identical", services: &fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "canonical-api", RuntimeType: domain.RuntimeTypeDocker}}},
			envs:   &fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "prod"}}},
			states: &fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusInSync, DesiredHash: "canonical-hash"}}}},
		{name: "divergent", services: divergentServices, envs: divergentEnvs, states: divergentStates, obs: divergentObs},
	} {
		t.Run(index.name, func(t *testing.T) {
			store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
			require.NoError(t, err)
			defer store.Close()
			key := gonostr.Generate()
			source, err := NewCanonicalRuntimeSource(store, key.Public().Hex())
			require.NoError(t, err)
			services, environments, states, observations, _ := CanonicalRuntimeRepositories(source, index.services, index.envs, index.states, index.obs, nil)
			projector := NewDNSProjector(services, environments, states, observations, source, source, source, testDNSConfig(), zap.NewNop())
			before, err := projector.ListDNSEndpoints(ctx)
			require.NoError(t, err)
			require.Empty(t, before, "SQL-only rows may not reconstruct missing canonical desired state")

			canonicalRuntimeFixture(t, store, key, serviceID, envID, "canonical-hash", "10.0.0.10")
			got, err := projector.ListDNSEndpoints(ctx)
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, "canonical-api", got[0].Name)
			require.Equal(t, "10.0.0.10", got[0].Address)
			require.Equal(t, "prod.example", got[0].Zone)
			backend := &fakeDNSBackend{}
			zone := domain.DNSZone{Name: "prod.example", BackendRef: "test", TTL: 120}
			dns := NewDNSReconciler(projector, []domain.DNSZone{zone}, &fakeDNSResolver{backends: map[string]DNSBackend{"test": backend}}, time.Minute, zap.NewNop())
			ready := make(chan struct{})
			dns.SetCanonicalReadiness(ready)
			published := &fakeCanonicalPublisher{}
			dns.SetCanonicalPublisher(published)
			require.Error(t, dns.ReconcileOnce(ctx), "startup DNS must wait for relay catch-up")
			require.Zero(t, backend.syncCallCount())
			close(ready)
			require.NoError(t, dns.ReconcileOnce(ctx))
			require.Equal(t, 1, backend.syncCallCount())
			require.Len(t, published.zoneSyncs, 1)
			require.Equal(t, "10.0.0.10", published.endpoints[0].Address)
			require.NoError(t, dns.ReconcileOnce(ctx))
			require.Equal(t, 1, backend.syncCallCount(), "unchanged local desired state must not resync or republish a zone")
			require.Len(t, published.zoneSyncs, 1)
			allStates, err := states.ListAll(ctx)
			require.NoError(t, err)
			require.Len(t, allStates, 1)
			require.Equal(t, "canonical-hash", allStates[0].DesiredHash)
			// A SQL row cannot resurrect a tombstoned canonical coordinate.
			tombstone := map[string]any{"deleted": true, "service_id": serviceID.String(), "environment_id": envID.String()}
			wire, err := json.Marshal(tombstone)
			require.NoError(t, err)
			saveCanonicalRuntimeEvent(t, store, key, kinds.ServiceState, nostrAdapter.ServiceStateDTag(serviceID, envID), true, string(wire), 102, nil)
			gone, err := projector.ListDNSEndpoints(ctx)
			require.NoError(t, err)
			require.Empty(t, gone)
		})
	}
}

func TestCanonicalDNSProjectionPreservesLLMMLAndWorkerFamilies(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	defer store.Close()
	key := gonostr.Generate()
	serviceID, envID := uuid.New(), uuid.New()
	canonicalRuntimeFixture(t, store, key, serviceID, envID, "hash", "10.0.0.10")
	put := func(kind int, d string, value any, at gonostr.Timestamp) {
		content, err := json.Marshal(value)
		require.NoError(t, err)
		saveCanonicalRuntimeEvent(t, store, key, kind, d, false, string(content), at, nil)
	}
	routeID := uuid.New()
	put(kinds.LLMRouteRegistry, routeID.String(), domain.LLMRoute{ID: routeID, Name: "review"}, 102)
	put(kinds.LLMRouteState, routeID.String()+":"+envID.String(), domain.LLMRouteState{
		RouteID: routeID, EnvironmentID: envID, GatewayStatus: domain.GatewayRouteStatusSynced,
		BackendHealth: domain.HealthStatusHealthy, BackendEndpoint: "http://10.0.0.20:8000"}, 103)
	endpointID := uuid.New()
	put(kinds.MLInferenceEndpointRegistry, "endpoint:embedding:prod", domain.MLInferenceEndpoint{ID: endpointID, Name: "embedding", EnvironmentID: envID}, 104)
	put(kinds.MLInferenceEndpointState, "endpoint-state:embedding:prod", domain.MLInferenceState{
		EndpointID: endpointID, EnvironmentID: envID, BackendHealth: domain.HealthStatusHealthy,
		BackendEndpoint: "http://10.0.0.30:8080"}, 105)
	worker := domain.Worker{PubKey: "worker-one", Name: "worker-one", Status: domain.WorkerStatusOnline,
		RuntimeTarget: &domain.WorkerRuntimeTarget{Type: domain.RuntimeTypeDocker, PublicBaseURL: "http://10.0.0.40:9000"}}
	put(int(kinds.CPStateFamilyWorkerState), worker.PubKey, worker, 106)
	source, err := NewCanonicalRuntimeSource(store, key.Public().Hex())
	require.NoError(t, err)
	services, environments, states, observations, _ := CanonicalRuntimeRepositories(source, nil, nil, nil, nil, nil)
	projector := NewDNSProjector(services, environments, states, observations, source, source, source, testDNSConfig(), zap.NewNop())
	endpoints, err := projector.ListDNSEndpoints(ctx)
	require.NoError(t, err)
	require.Len(t, endpoints, 4)
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyService, "canonical-api", "10.0.0.10")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyLLM, "review", "10.0.0.20")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyML, "embedding", "10.0.0.30")
	assertEndpoint(t, endpoints, domain.DNSEndpointFamilyWorker, "worker-one", "10.0.0.40")
}

type storingRuntimeStatePublisher struct {
	t     *testing.T
	store *localstore.Store
	key   gonostr.SecretKey
	count int
}

func (p *storingRuntimeStatePublisher) PublishState(_ context.Context, state *domain.EnvironmentServiceState, obs *domain.RuntimeObservation) error {
	p.count++
	tags, content := nostrAdapter.RuntimeStateRecord(state, obs)
	saveCanonicalRuntimeEvent(p.t, p.store, p.key, kinds.ServiceState, nostrAdapter.ServiceStateDTag(state.ServiceID, state.EnvironmentID), false, content, gonostr.Timestamp(200+p.count), tags)
	return nil
}
func (p *storingRuntimeStatePublisher) PublishStateTombstone(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func TestCanonicalRuntimeReconcileDoesNotSignFromDivergentSQLAndDedupesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.bolt")
	store, err := localstore.Open(path)
	require.NoError(t, err)
	key := gonostr.Generate()
	serviceID, envID := uuid.New(), uuid.New()
	source, err := NewCanonicalRuntimeSource(store, key.Public().Hex())
	require.NoError(t, err)
	sqlServices := &fakeServiceRepo{services: []domain.Service{{ID: serviceID, Name: "sql-evil", RuntimeType: domain.RuntimeTypeDocker}}}
	sqlEnvs := &fakeEnvironmentRepo{environments: []domain.Environment{{ID: envID, Name: "sql-zone"}}}
	sqlStates := &fakeStateRepo{states: []domain.EnvironmentServiceState{{ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "sql-hash", DriftStatus: domain.DriftStatusDrifted}}}
	observedHash := "canonical-hash"
	build := func(source *CanonicalRuntimeSource, statePub RuntimeStatePublisher) *Reconciler {
		services, environments, states, observations, units := CanonicalRuntimeRepositories(source, sqlServices, sqlEnvs, sqlStates, nil, nil)
		r := NewReconciler(services, environments, nil, units, observations, states,
			&mockRuntimeResolver{rt: &mockRuntime{observeNormHash: observedHash, observeHealth: domain.HealthStatusHealthy}},
			&mockPublisher{}, time.Minute, zap.NewNop())
		r.SetRuntimeStatePublisher(statePub)
		return r
	}
	pub := &storingRuntimeStatePublisher{t: t, store: store, key: key}
	build(source, pub).reconcileAll(ctx)
	require.Zero(t, pub.count, "SQL-only desired row must not sign runtime state")
	canonicalRuntimeFixture(t, store, key, serviceID, envID, "", "")
	state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "canonical-hash", DriftStatus: domain.DriftStatusUnknown}
	tags, content := nostrAdapter.RuntimeStateRecord(state, nil)
	saveCanonicalRuntimeEvent(t, store, key, kinds.ServiceState, nostrAdapter.ServiceStateDTag(serviceID, envID), false, content, 101, tags)
	build(source, pub).reconcileAll(ctx)
	require.Equal(t, 1, pub.count)
	latest, err := source.state(ctx, serviceID, envID)
	require.NoError(t, err)
	require.Equal(t, "canonical-hash", latest.DesiredHash)
	require.Equal(t, domain.DriftStatusInSync, latest.DriftStatus)
	require.NoError(t, store.Close())
	store, err = localstore.Open(path)
	require.NoError(t, err)
	defer store.Close()
	source, err = NewCanonicalRuntimeSource(store, key.Public().Hex())
	require.NoError(t, err)
	pub.store = store
	build(source, pub).reconcileAll(ctx)
	require.Equal(t, 1, pub.count, "restart with unchanged canonical observation must not re-sign")
	for ev := range store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{gonostr.Kind(kinds.CASControlState)}, Tags: gonostr.TagMap{"d": {nostrAdapter.ServiceStateDTag(serviceID, envID)}}}) {
		stored, err := store.SaveEvent(ev)
		require.NoError(t, err)
		require.False(t, stored, "duplicate relay delivery must not grow local state")
	}
	build(source, pub).reconcileAll(ctx)
	require.Equal(t, 1, pub.count)
	// Auto-apply mode can still report drift, but the SQL-backed deploy
	// lifecycle is not admitted as a remediation source.
	autoEnv := domain.Environment{ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeAutoApply}}
	autoContent, err := json.Marshal(autoEnv)
	require.NoError(t, err)
	var autoFields map[string]any
	require.NoError(t, json.Unmarshal(autoContent, &autoFields))
	autoFields["deleted"] = false
	autoContent, err = json.Marshal(autoFields)
	require.NoError(t, err)
	saveCanonicalRuntimeEvent(t, store, key, kinds.EnvironmentRegistry, envID.String(), false, string(autoContent), 150, nil)
	observedHash = "drifted-runtime-hash"
	noSQLLifecycle := build(source, pub)
	require.Nil(t, noSQLLifecycle.deployer, "SQL-backed lifecycle must not be wired to canonical runtime reconciliation")
	current, err := source.state(ctx, serviceID, envID)
	require.NoError(t, err)
	require.NoError(t, noSQLLifecycle.reconcileOne(ctx, &current.EnvironmentServiceState))
	require.Equal(t, 2, pub.count)
	latest, err = source.state(ctx, serviceID, envID)
	require.NoError(t, err)
	require.Equal(t, "canonical-hash", latest.DesiredHash)
	require.Equal(t, domain.DriftStatusDrifted, latest.DriftStatus)
}
