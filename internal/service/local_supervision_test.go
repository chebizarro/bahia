package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const supervisionTestKey = "f555555555555555555555555555555555555555555555555555555555555555"

func localSupervisionFixture(t *testing.T) (LocalSupervisionState, gonostr.SecretKey) {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	secret, err := gonostr.SecretKeyFromHex(supervisionTestKey)
	if err != nil {
		t.Fatal(err)
	}
	return LocalSupervisionState{Store: store, Author: secret.Public().Hex()}, secret
}

func saveSupervisionRecord(t *testing.T, state LocalSupervisionState, secret gonostr.SecretKey, topic, schema, coordinate string, content any, createdAt int64) {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	event := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: gonostr.Timestamp(createdAt), Tags: gonostr.Tags{{"d", coordinate}, {"t", topic}, {"schema", schema}}, Content: string(encoded)}
	if err := event.Sign(secret); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Store.SaveEvent(event); err != nil {
		t.Fatal(err)
	}
}

type failingRouteIndex struct{ RouteCanaryRepository }

func (f failingRouteIndex) UpsertState(context.Context, *domain.RouteCanaryState) error {
	return errors.New("SQL offline")
}
func (f failingRouteIndex) UpsertStateWithEvent(context.Context, *domain.RouteCanaryState, *domain.RouteCanaryEvent) error {
	return errors.New("SQL offline")
}

func TestLocalRouteCanarySweepFollowsRelayDesiredStateAndSQLFailure(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	plan := testRoutePlan()
	serviceState := domain.EnvironmentServiceState{ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DesiredRuntimeState: &domain.DesiredServiceSpec{PublicRoute: plan}}
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", serviceState, 100)
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation()}}
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	bus := &eventBusFake{}
	repo := NewLocalRouteCanaryRepository(state, failingRouteIndex{newMemoryRouteCanaryRepo()}, nil)
	supervisor, err := NewRouteCanarySupervisor(LocalRoutePlanSource{State: state}, repo, evaluator, nil, bus, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical := &savingSupervisionPublisher{store: state.Store, secret: secret}
	projector, err := NewRouteCanaryProjector(bus, canonical, nil)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.SetCanonicalProjector(projector)
	supervisor.EvaluateOnce(context.Background())
	if prober.calls == 0 || len(bus.published) == 0 || canonical.count == 0 {
		t.Fatalf("DB-less sweep lost probe/observable: probes=%d events=%d", prober.calls, len(bus.published))
	}
	beforeRetry := canonical.count
	bus.Publish(context.Background(), bus.published[0])
	require.Equal(t, beforeRetry, canonical.count, "bus retry must not duplicate canonical route records")
	previous := prober.calls
	serviceState.DesiredRuntimeState = nil
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", serviceState, 101)
	supervisor.EvaluateOnce(context.Background())
	if prober.calls != previous {
		t.Fatalf("withdrawn relay route was probed: %d -> %d", previous, prober.calls)
	}
}

func TestLocalRouteCanaryRestartResumesFailureStreak(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	plan := testRoutePlan()
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", domain.EnvironmentServiceState{ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DesiredRuntimeState: &domain.DesiredServiceSpec{PublicRoute: plan}}, 100)
	prior := domain.RouteCanaryState{RouteCanaryKey: domain.RouteCanaryKeyForPlan(plan), ConsecutiveFailures: 1, Classification: domain.RouteCanaryClassificationUpstreamError, UpdatedAt: time.Unix(100, 0)}
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicRouteCanary, routeCanaryStateSchema, prior.Coordinate(), map[string]any{"route_canary": prior}, 100)
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation()}}
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	bus := &routeCanaryPublisher{}
	supervisor, err := NewRouteCanarySupervisor(LocalRoutePlanSource{State: state}, NewLocalRouteCanaryRepository(state, nil, nil), evaluator, nil, bus, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	supervisor.EvaluateOnce(context.Background())
	found := false
	for _, event := range bus.published {
		if event.Type == events.EventRouteCanaryOutageOpened {
			found = true
		}
	}
	if !found {
		t.Fatal("restart did not resume canonical failure streak")
	}
}

func TestLocalRouteCanaryRestartRetainsOpenOutageSince(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	plan := testRoutePlan()
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", domain.EnvironmentServiceState{ServiceID: plan.ServiceID, EnvironmentID: plan.EnvironmentID, DesiredRuntimeState: &domain.DesiredServiceSpec{PublicRoute: plan}}, 100)
	openedAt := time.Unix(90, 0).UTC()
	prior := domain.RouteCanaryState{RouteCanaryKey: domain.RouteCanaryKeyForPlan(plan), Open: true, ConsecutiveFailures: 2, Classification: domain.RouteCanaryClassificationUpstreamError, OpenedAt: &openedAt, LastObservedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(100, 0).UTC()}
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicRouteCanary, routeCanaryStateSchema, prior.Coordinate(), map[string]any{"route_canary": prior}, 100)
	prober := &stubRouteProber{byPerspective: map[domain.RouteCanaryPerspective]domain.RouteCanaryObservation{domain.RouteCanaryPerspectivePublicEdge: staleUpstreamObservation()}}
	evaluator, err := NewRouteCanaryEvaluator(prober, testRouteCanaryPolicy())
	require.NoError(t, err)
	repo := NewLocalRouteCanaryRepository(state, nil, nil)
	supervisor, err := NewRouteCanarySupervisor(LocalRoutePlanSource{State: state}, repo, evaluator, nil, &routeCanaryPublisher{}, time.Minute, nil)
	require.NoError(t, err)
	supervisor.EvaluateOnce(context.Background())
	resumed, err := repo.GetState(context.Background(), prior.RouteCanaryKey)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.True(t, resumed.Open)
	require.Equal(t, prior.ConsecutiveFailures+1, resumed.ConsecutiveFailures)
	require.NotNil(t, resumed.OpenedAt)
	require.True(t, resumed.OpenedAt.Equal(openedAt), "restart must retain the canonical outage start")
}

type savingSupervisionPublisher struct {
	store  *localstore.Store
	secret gonostr.SecretKey
	count  int
}

func (p *savingSupervisionPublisher) PublishSignedEvent(_ context.Context, event *gonostr.Event) error {
	if err := event.Sign(p.secret); err != nil {
		return err
	}
	if _, err := p.store.SaveEvent(*event); err != nil {
		return err
	}
	p.count++
	return nil
}

type failingManagedIndex struct{ *supervisorRepoFake }

func (f failingManagedIndex) UpsertHealthWithEvent(context.Context, *domain.ManagedInstanceHealth, *domain.ManagedInstanceHealthEvent) error {
	return errors.New("SQL offline")
}
func (f failingManagedIndex) RecordRecoveryAttempt(context.Context, *domain.RecoveryAttempt) (bool, error) {
	return false, errors.New("SQL offline")
}
func (f failingManagedIndex) CompleteRecoveryAttemptWithHealthEvent(context.Context, string, domain.RecoveryAttemptResult, string, *domain.ManagedInstanceHealth, *domain.ManagedInstanceHealthEvent) (bool, error) {
	return false, errors.New("SQL offline")
}

func TestLocalManagedInstanceSweepSurvivesSQLIndexFailureAndRestartBudget(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	publisher := &savingSupervisionPublisher{store: state.Store, secret: secret}
	repo := NewLocalManagedInstanceState(state, failingManagedIndex{&supervisorRepoFake{}}, publisher, nil)
	now := time.Now().UTC().Truncate(time.Second)
	key := testKey()
	rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}, {Status: domain.InstanceHealthStatusRunning, ObservedAt: now.Add(time.Second)}, {Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now.Add(2 * time.Second)}}}
	spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
	bus := &eventBusFake{}
	supervisor, err := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, repo, lockFake{acquired: true}, bus, time.Second, zap.NewNop())
	require.NoError(t, err)
	supervisor.SetCanonicalProjector(NewManagedInstanceHealthProjector(bus, publisher, zap.NewNop()))
	require.NoError(t, supervisor.EvaluateOnce(context.Background()))
	require.Equal(t, 1, rt.restarts)
	require.GreaterOrEqual(t, publisher.count, 2, "pending and terminal recovery are canonical before SQL indexing")
	require.Equal(t, 1, publishedEventCount(bus.published, events.EventRuntimeRecoveryRequested))
	restarted := NewLocalManagedInstanceState(state, nil, publisher, nil)
	attempts, err := restarted.ListRecentRecoveryAttempts(context.Background(), key, 10)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)
	restoredHealth, err := restarted.GetHealth(context.Background(), key)
	require.NoError(t, err)
	require.NotNil(t, restoredHealth)
	require.Equal(t, domain.InstanceHealthStatusRunning, restoredHealth.Status)
	beforeRetry := publisher.count
	inserted, err := restarted.RecordRecoveryAttempt(context.Background(), &attempts[0])
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, beforeRetry, publisher.count, "retry must not duplicate canonical attempt records")
}

func TestLocalManagedInstanceMaintenanceOverrideSurvivesRestart(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	publisher := &savingSupervisionPublisher{store: state.Store, secret: secret}
	key := testKey()
	now := time.Now().UTC().Truncate(time.Second)
	override := &domain.MaintenanceOverride{ID: uuid.New(), ManagedInstanceKey: key, Actor: "operator", Reason: "maintenance", CreatedAt: now}
	repo := NewLocalManagedInstanceState(state, nil, publisher, nil)
	require.NoError(t, repo.CreateMaintenanceOverride(context.Background(), override))
	restarted := NewLocalManagedInstanceState(state, nil, publisher, nil)
	active, err := restarted.GetActiveMaintenanceOverride(context.Background(), key, now)
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, override.ID, active.ID)
	require.NoError(t, restarted.ClearMaintenanceOverride(context.Background(), key))
	cleared := NewLocalManagedInstanceState(state, nil, publisher, nil)
	active, err = cleared.GetActiveMaintenanceOverride(context.Background(), key, now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, active)
}

func TestLocalManagedInstanceRestartUsesCanonicalBudget(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	publisher := &savingSupervisionPublisher{store: state.Store, secret: secret}
	key := testKey()
	now := time.Now().UTC().Truncate(time.Second)
	for i := range 3 {
		attempt := domain.RecoveryAttempt{ID: uuid.New(), ManagedInstanceKey: key, CorrelationID: string(rune('a' + i)), RequestedAt: now.Add(-time.Duration(i+1) * time.Minute), Result: domain.RecoveryAttemptFailed}
		_, err := NewLocalManagedInstanceState(state, nil, publisher, nil).RecordRecoveryAttempt(context.Background(), &attempt)
		require.NoError(t, err)
	}
	rt := &sequenceRuntime{observations: []*runtime.InstanceObservation{{Status: domain.InstanceHealthStatusUnhealthy, ObservedAt: now}}}
	spec := SupervisionSpec{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: testPolicy(false)}
	bus := &eventBusFake{}
	supervisor, err := NewManagedInstanceSupervisor(StaticSupervisionSpecSource{spec}, NewLocalManagedInstanceState(state, nil, publisher, nil), lockFake{acquired: true}, bus, time.Second, zap.NewNop())
	require.NoError(t, err)
	require.NoError(t, supervisor.EvaluateOnce(context.Background()))
	require.Zero(t, rt.restarts)
	require.Equal(t, 1, publishedEventCount(bus.published, events.EventRuntimeRecoveryBudgetExhausted))
}

func TestLocalManagedInstanceSpecSourceFollowsRelayReplacement(t *testing.T) {
	state, secret := localSupervisionFixture(t)
	serviceID, environmentID, artifactID := uuid.New(), uuid.New(), uuid.New()
	svc := domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}
	env := domain.Environment{ID: environmentID, Name: "production", RuntimeConfig: map[string]any{"type": "docker"}}
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceRegistry, kinds.CASControlStateSchema, serviceID.String(), svc, 100)
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicEnvironmentRegistry, kinds.CASControlStateSchema, environmentID.String(), map[string]any{"id": environmentID, "name": env.Name, "runtime_config": env.RuntimeConfig, "deployment_units": []map[string]any{{"key": "default", "implicit": true}}}, 100)
	desired := domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: environmentID, DesiredArtifactID: &artifactID, DesiredRuntimeState: &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api"}}
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", desired, 100)
	source := &LocalSupervisionSpecSource{State: state, Resolver: supervisionResolverFake{runtime: runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())}}
	specs, err := source.SupervisionSpecs(context.Background())
	require.NoError(t, err)
	require.Len(t, specs, 1)
	require.True(t, specs[0].DesiredRunning)
	desired.DesiredArtifactID, desired.DesiredRuntimeState = nil, nil
	saveSupervisionRecord(t, state, secret, kinds.CPStateTopicServiceState, kinds.CASControlStateSchema, "service-state", desired, 101)
	specs, err = source.SupervisionSpecs(context.Background())
	require.NoError(t, err)
	require.Empty(t, specs)
}
