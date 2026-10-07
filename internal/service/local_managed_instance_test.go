package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
)

type supervisionResolverFake struct {
	runtime runtime.Runtime
}

func (r supervisionResolverFake) Resolve(*domain.Service, *domain.Environment) (runtime.Runtime, error) {
	return r.runtime, nil
}

// supervisedRuntime is a Docker runtime adapter whose instance observation and
// control are scripted.
type supervisedRuntime struct {
	*runtime.DockerObserver
	instances *sequenceRuntime
}

func newSupervisedRuntime(instances *sequenceRuntime) supervisedRuntime {
	return supervisedRuntime{DockerObserver: runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop()), instances: instances}
}

func (r supervisedRuntime) ObserveInstance(ctx context.Context, key domain.ManagedInstanceKey) (*runtime.InstanceObservation, error) {
	return r.instances.ObserveInstance(ctx, key)
}
func (r supervisedRuntime) RestartInstance(ctx context.Context, key domain.ManagedInstanceKey) error {
	return r.instances.RestartInstance(ctx, key)
}
func (r supervisedRuntime) StopInstance(ctx context.Context, key domain.ManagedInstanceKey) error {
	return r.instances.StopInstance(ctx, key)
}

// managedDaemon is the managed-instance wiring of one daemon process over the
// fixture's local event store. Building another one is a restart: nothing but
// the store carries over.
type managedDaemon struct {
	supervisor *ManagedInstanceSupervisor
	state      *LocalManagedInstanceState
	bus        *eventBusFake
	publisher  *storePublisher
}

func (f *supervisionFixture) managedDaemon(clock *testClock, source SupervisionSpecSource, index repository.ManagedInstanceHealthRepository) *managedDaemon {
	f.t.Helper()
	bus := &eventBusFake{}
	publisher := f.publisher()
	state := NewLocalManagedInstanceState(f.state, index, publisher, nil)
	state.now = clock.Now
	supervisor, err := NewManagedInstanceSupervisor(source, state, NewSupervisionApplyLock(nil, nil), bus, time.Second, zap.NewNop())
	require.NoError(f.t, err)
	supervisor.now = clock.Now
	supervisor.SetCanonicalProjector(NewManagedInstanceHealthProjector(bus, publisher, nil))
	return &managedDaemon{supervisor: supervisor, state: state, bus: bus, publisher: publisher}
}

func (d *managedDaemon) announced(eventType events.EventType) int {
	return publishedEventCount(d.bus.published, eventType)
}

// managedDesiredState is a Bahia-managed service deployed to an environment's
// implicit default unit.
type managedDesiredState struct {
	service     domain.Service
	environment domain.Environment
	state       domain.EnvironmentServiceState
}

func newManagedDesiredState() managedDesiredState {
	serviceID, environmentID, artifactID := uuid.New(), uuid.New(), uuid.New()
	return managedDesiredState{
		service:     domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker},
		environment: domain.Environment{ID: environmentID, Name: "production", RuntimeConfig: map[string]any{"type": "docker"}},
		state: domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: environmentID, DesiredArtifactID: &artifactID, DesiredRuntimeState: &domain.DesiredServiceSpec{
			ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api",
		}},
	}
}

func (f *supervisionFixture) deliverManaged(desired managedDesiredState, at time.Time) {
	f.t.Helper()
	f.deliverService(desired.service, at)
	f.deliverEnvironment(desired.environment, nil, at)
	f.deliverServiceState(desired.state, at)
}

func staticSpec(key domain.ManagedInstanceKey, rt *sequenceRuntime, policy domain.RecoveryPolicy) StaticSupervisionSpecSource {
	return StaticSupervisionSpecSource{{Key: key, SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: rt, Controller: rt, RecoveryPolicy: policy}}
}

func observed(status domain.InstanceHealthStatus, at time.Time) *runtime.InstanceObservation {
	return &runtime.InstanceObservation{Status: status, ObservedAt: at}
}

// storedLedger returns the attempts of the single canonical recovery ledger.
func (f *supervisionFixture) storedLedger() (gonostr.Event, []domain.RecoveryAttempt) {
	f.t.Helper()
	ledgers := f.stored(kinds.CASControlState, managedRecoveryLedgerSchema)
	require.Len(f.t, ledgers, 1, "one canonical ledger record per instance")
	var content struct {
		Attempts []domain.RecoveryAttempt `json:"attempts"`
	}
	require.NoError(f.t, json.Unmarshal([]byte(ledgers[0].Content), &content))
	return ledgers[0], content.Attempts
}

// offlineManagedIndex is a SQL index during a database outage: the writes the
// supervisor makes fail, and any read fails the test, because supervision must
// never decide from SQL.
type offlineManagedIndex struct {
	repository.ManagedInstanceHealthRepository
	t *testing.T
}

func (i offlineManagedIndex) UpsertHealth(context.Context, *domain.ManagedInstanceHealth) error {
	return errSQLOffline
}
func (i offlineManagedIndex) UpsertHealthWithEvent(context.Context, *domain.ManagedInstanceHealth, *domain.ManagedInstanceHealthEvent) error {
	return errSQLOffline
}
func (i offlineManagedIndex) RecordRecoveryAttempt(context.Context, *domain.RecoveryAttempt) (bool, error) {
	return false, errSQLOffline
}
func (i offlineManagedIndex) CompleteRecoveryAttemptWithHealthEvent(context.Context, string, domain.RecoveryAttemptResult, string, *domain.ManagedInstanceHealth, *domain.ManagedInstanceHealthEvent) (bool, error) {
	return false, errSQLOffline
}
func (i offlineManagedIndex) GetHealth(context.Context, domain.ManagedInstanceKey) (*domain.ManagedInstanceHealth, error) {
	i.t.Error("managed instance supervision read health from the SQL index")
	return nil, errSQLOffline
}
func (i offlineManagedIndex) ListRecentRecoveryAttempts(context.Context, domain.ManagedInstanceKey, int) ([]domain.RecoveryAttempt, error) {
	i.t.Error("managed instance supervision read recovery attempts from the SQL index")
	return nil, errSQLOffline
}
func (i offlineManagedIndex) GetActiveMaintenanceOverride(context.Context, domain.ManagedInstanceKey, time.Time) (*domain.MaintenanceOverride, error) {
	i.t.Error("managed instance supervision read a maintenance override from the SQL index")
	return nil, errSQLOffline
}

func TestLocalManagedInstanceSupervisorSweepsWithoutSQL(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	desired := newManagedDesiredState()
	f.deliverManaged(desired, clock.Now())
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusRunning, clock.Now().Add(time.Second)),
	}}
	source := &LocalSupervisionSpecSource{State: f.state, Resolver: supervisionResolverFake{runtime: newSupervisedRuntime(instances)}, Policy: testPolicy(false)}
	daemon := f.managedDaemon(clock, source, nil)

	require.NoError(t, daemon.supervisor.EvaluateOnce(context.Background()))

	require.Equal(t, 1, instances.restarts, "the unhealthy instance in relay-derived desired state is restarted")
	require.Equal(t, domain.ManagedInstanceKey{ServiceID: desired.service.ID, EnvironmentID: desired.environment.ID, RuntimeTargetName: desired.service.RuntimeTargetName()}, instances.keys[0])
	require.Equal(t, 1, daemon.announced(events.EventRuntimeRecoveryRequested))
	require.Equal(t, 1, daemon.announced(events.EventRuntimeRecoveryCompleted))

	_, attempts := f.storedLedger()
	require.Len(t, attempts, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)
	healths := f.stored(kinds.CASControlState, managedHealthStateSchema)
	require.Len(t, healths, 1, "one canonical health record per instance")
	health, ok := decodeManagedHealthRecord(healths[0])
	require.True(t, ok)
	require.Equal(t, domain.InstanceHealthStatusRunning, health.Status)
	recoveryAudits := 0
	for _, audit := range f.stored(kinds.CASAudit, managedHealthAuditSchema) {
		if supervisionTag(audit, "correlation") == attempts[0].CorrelationID {
			recoveryAudits++
		}
	}
	require.Equal(t, 2, recoveryAudits, "the request and its completion are audited")
}

func TestLocalManagedInstanceRestartResumesRecoveryBudget(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	policy := testPolicy(false)
	policy.RestartBudget.MaxAttempts = 1
	ctx := context.Background()

	// The only restart the budget allows fails.
	spent := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now().Add(time.Second)),
	}}
	first := f.managedDaemon(clock, staticSpec(key, spent, policy), nil)
	require.NoError(t, first.supervisor.EvaluateOnce(ctx))
	require.Equal(t, 1, spent.restarts)
	require.Equal(t, 1, first.announced(events.EventRuntimeRecoveryFailed))

	// A restarted daemon must not restart the instance again.
	clock.Advance(5 * time.Minute)
	afterRestart := &sequenceRuntime{observations: []*runtime.InstanceObservation{observed(domain.InstanceHealthStatusUnhealthy, clock.Now())}}
	second := f.managedDaemon(clock, staticSpec(key, afterRestart, policy), nil)
	require.NoError(t, second.supervisor.EvaluateOnce(ctx))
	require.Zero(t, afterRestart.restarts, "restart must resume the canonical recovery budget")
	require.Equal(t, 1, second.announced(events.EventRuntimeRecoveryBudgetExhausted))

	// Nor report the same exhausted budget again after another restart.
	clock.Advance(time.Minute)
	again := &sequenceRuntime{observations: []*runtime.InstanceObservation{observed(domain.InstanceHealthStatusUnhealthy, clock.Now())}}
	third := f.managedDaemon(clock, staticSpec(key, again, policy), nil)
	require.NoError(t, third.supervisor.EvaluateOnce(ctx))
	require.Zero(t, again.restarts)
	require.Zero(t, third.announced(events.EventRuntimeRecoveryBudgetExhausted), "one exhaustion per failure generation across restarts")
	require.Zero(t, third.publisher.published(kinds.CASControlState, managedRecoveryLedgerSchema))

	_, attempts := f.storedLedger()
	require.Len(t, attempts, 2)
}

func TestLocalManagedInstanceRestartReconcilesPendingAttempt(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()

	// The daemon claimed a restart and stopped before recording its outcome.
	crashed := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil)
	pending := newCorrelatedRecoveryAttempt(key, clock.Now(), "interrupted", domain.RecoveryDecision{Reason: "observed status requires recovery"}, domain.RecoveryAttemptPending)
	inserted, err := crashed.state.RecordRecoveryAttempt(ctx, &pending)
	require.NoError(t, err)
	require.True(t, inserted)

	clock.Advance(time.Minute)
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{observed(domain.InstanceHealthStatusRunning, clock.Now())}}
	restarted := f.managedDaemon(clock, staticSpec(key, instances, testPolicy(false)), nil)
	require.NoError(t, restarted.supervisor.EvaluateOnce(ctx))

	require.Zero(t, instances.restarts, "a pending claim is settled, not repeated")
	require.Equal(t, 1, restarted.announced(events.EventRuntimeRecoveryCompleted))
	_, attempts := f.storedLedger()
	require.Len(t, attempts, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)
}

func TestLocalManagedInstanceDesiredStateChangeFromRelayChangesSupervisedSet(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	desired := newManagedDesiredState()
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{observed(domain.InstanceHealthStatusHealthy, clock.Now())}}
	source := &LocalSupervisionSpecSource{State: f.state, Resolver: supervisionResolverFake{runtime: newSupervisedRuntime(instances)}, Policy: testPolicy(false)}
	daemon := f.managedDaemon(clock, source, offlineManagedIndex{t: t})
	ctx := context.Background()
	supervised := func() []SupervisionSpec {
		t.Helper()
		specs, err := source.SupervisionSpecs(ctx)
		require.NoError(t, err)
		return specs
	}

	require.Empty(t, supervised())
	require.NoError(t, daemon.supervisor.EvaluateOnce(ctx))
	require.Empty(t, f.stored(kinds.CASControlState, managedHealthStateSchema))

	// The deployment arrives as relay events.
	f.deliverManaged(desired, clock.Now())
	specs := supervised()
	require.Len(t, specs, 1)
	require.True(t, specs[0].DesiredRunning)
	require.Equal(t, "production", specs[0].Host)
	require.NoError(t, daemon.supervisor.EvaluateOnce(ctx))
	require.Len(t, f.stored(kinds.CASControlState, managedHealthStateSchema), 1, "the new instance is observed")

	// An operator stop replaces the desired runtime state.
	clock.Advance(time.Minute)
	stopped := desired.state
	stopped.DesiredRuntimeState = nil
	f.deliverServiceState(stopped, clock.Now())
	specs = supervised()
	require.Len(t, specs, 1)
	require.False(t, specs[0].DesiredRunning, "a stopped service must never be restarted")

	// A tombstone removes the instance from the set.
	clock.Advance(time.Minute)
	f.deliverServiceStateTombstone(desired.service.ID, desired.environment.ID, clock.Now())
	require.Empty(t, supervised())

	// So does deleting the service while its state record still exists.
	clock.Advance(time.Minute)
	f.deliverServiceState(desired.state, clock.Now())
	require.Len(t, supervised(), 1)
	f.deliverServiceTombstone(desired.service.ID, clock.Now())
	require.Empty(t, supervised(), "a deleted service must not be supervised")
}

func TestLocalSupervisionSpecSourceFailsClosedForStoppedOrUnknownDesiredState(t *testing.T) {
	adapter := runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())
	for _, tc := range []struct {
		name    string
		desired func(managedDesiredState) *domain.DesiredServiceSpec
		want    bool
	}{
		{name: "operator stopped", desired: func(managedDesiredState) *domain.DesiredServiceSpec { return nil }, want: false},
		{name: "unknown desired state", desired: func(managedDesiredState) *domain.DesiredServiceSpec { return &domain.DesiredServiceSpec{} }, want: false},
		{name: "complete running desired state", desired: func(d managedDesiredState) *domain.DesiredServiceSpec { return d.state.DesiredRuntimeState }, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupervisionFixture(t)
			desired := newManagedDesiredState()
			desired.environment.RuntimeConfig = map[string]any{"type": "docker", "endpoint": "tcp://user:password@private-host:2376"}
			desired.state.DesiredRuntimeState = tc.desired(desired)
			f.deliverManaged(desired, time.Now())
			source := &LocalSupervisionSpecSource{State: f.state, Resolver: supervisionResolverFake{runtime: adapter}}

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

func TestLocalSupervisionSpecSourceResolvesExplicitDeploymentUnits(t *testing.T) {
	f := newSupervisionFixture(t)
	adapter := runtime.NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())
	now := time.Now()
	managedUnit, externalUnit := uuid.New(), uuid.New()
	environment := domain.Environment{ID: uuid.New(), Name: "production", RuntimeConfig: map[string]any{"type": "docker"}}
	f.deliverEnvironment(environment, []map[string]any{
		{"id": managedUnit.String(), "key": "default", "implicit": false, "runtime_type": "docker", "ownership_mode": string(domain.OwnershipModeBahiaManaged)},
		{"id": externalUnit.String(), "key": "edge", "implicit": false, "runtime_type": "docker", "ownership_mode": string(domain.OwnershipModeExternal)},
	}, now)
	deploy := func(unit *uuid.UUID) uuid.UUID {
		desired := newManagedDesiredState()
		desired.state.EnvironmentID, desired.state.DesiredRuntimeState.EnvironmentID = environment.ID, environment.ID
		desired.state.DeploymentUnitID = unit
		f.deliverService(desired.service, now)
		f.deliverServiceState(desired.state, now)
		return desired.service.ID
	}
	onManagedUnit, onDefaultUnit, _ := deploy(&managedUnit), deploy(nil), deploy(&externalUnit)
	source := &LocalSupervisionSpecSource{State: f.state, Resolver: supervisionResolverFake{runtime: adapter}}

	specs, err := source.SupervisionSpecs(context.Background())
	require.NoError(t, err)
	require.Len(t, specs, 2, "a unit Bahia does not manage is never supervised")
	services := map[uuid.UUID]uuid.UUID{}
	for _, spec := range specs {
		services[spec.Key.ServiceID] = spec.Key.DeploymentUnitID
	}
	require.Equal(t, map[uuid.UUID]uuid.UUID{onManagedUnit: managedUnit, onDefaultUnit: managedUnit}, services)
}

func TestLocalManagedInstanceSQLIndexFailureDoesNotBlockRecoveryOrCanonicalObservables(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusRunning, clock.Now().Add(time.Second)),
	}}
	daemon := f.managedDaemon(clock, staticSpec(key, instances, testPolicy(false)), offlineManagedIndex{t: t})

	require.NoError(t, daemon.supervisor.EvaluateOnce(context.Background()))

	require.Equal(t, 1, instances.restarts, "recovery runs although SQL is down")
	require.Equal(t, 1, daemon.announced(events.EventRuntimeRecoveryRequested))
	require.Equal(t, 1, daemon.announced(events.EventRuntimeRecoveryCompleted))
	require.Equal(t, 2, daemon.announced(events.EventRuntimeInstanceHealthChanged))
	require.Equal(t, 2, daemon.publisher.published(kinds.CASControlState, managedHealthStateSchema))
	require.Equal(t, 2, daemon.publisher.published(kinds.CASControlState, managedRecoveryLedgerSchema), "the claim and its outcome")
	_, attempts := f.storedLedger()
	require.Len(t, attempts, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)
}

func TestLocalManagedInstanceRetryPublishesNoDuplicateCanonicalRecords(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusRunning, clock.Now().Add(time.Second)),
	}}
	daemon := f.managedDaemon(clock, staticSpec(key, instances, testPolicy(false)), nil)
	require.NoError(t, daemon.supervisor.EvaluateOnce(ctx))
	_, attempts := f.storedLedger()
	require.Len(t, attempts, 1)
	published := daemon.publisher.total()

	// The bus redelivers every announced event to the projector.
	for _, event := range append([]events.Event(nil), daemon.bus.published...) {
		daemon.bus.Publish(ctx, event)
	}
	require.Equal(t, published, daemon.publisher.total(), "a redelivered event must not be signed again")

	// A retried claim or completion of the same attempt publishes nothing, in
	// this process or after a restart.
	restarted := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil)
	for _, state := range []*LocalManagedInstanceState{daemon.state, restarted.state} {
		retry := attempts[0]
		inserted, err := state.RecordRecoveryAttempt(ctx, &retry)
		require.NoError(t, err)
		require.False(t, inserted)
		completed, err := state.CompleteRecoveryAttemptWithHealthEvent(ctx, retry.CorrelationID, domain.RecoveryAttemptFailed, "late", &domain.ManagedInstanceHealth{ManagedInstanceKey: key}, nil)
		require.NoError(t, err)
		require.False(t, completed)
	}
	require.Equal(t, published, daemon.publisher.total())
	require.Zero(t, restarted.publisher.total())
	_, attempts = f.storedLedger()
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result, "a settled attempt keeps its outcome")
}

func TestLocalManagedInstanceLedgerRewrittenWithinOneSecondReplacesPredecessor(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()
	daemon := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil)
	attempt := newCorrelatedRecoveryAttempt(key, clock.Now(), "fast-restart", domain.RecoveryDecision{}, domain.RecoveryAttemptPending)
	inserted, err := daemon.state.RecordRecoveryAttempt(ctx, &attempt)
	require.NoError(t, err)
	require.True(t, inserted)
	claim, _ := f.storedLedger()

	// The restart finishes within the same second as its claim.
	completed, err := daemon.state.CompleteRecoveryAttemptWithHealthEvent(ctx, attempt.CorrelationID, domain.RecoveryAttemptSuccess, "", &domain.ManagedInstanceHealth{ManagedInstanceKey: key}, nil)
	require.NoError(t, err)
	require.True(t, completed)

	outcome, attempts := f.storedLedger()
	require.Greater(t, outcome.CreatedAt, claim.CreatedAt, "the outcome must replace the claim on its coordinate")
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)
	resumed, err := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil).state.ListRecentRecoveryAttempts(ctx, key, 10)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, resumed[0].Result)
}

func TestLocalManagedInstanceMaintenanceOverrideSurvivesRestart(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()

	first := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil)
	override, err := first.supervisor.SetMaintenanceOverride(ctx, key, "operator", "kernel upgrade", nil)
	require.NoError(t, err)

	// A restarted daemon still suppresses recovery.
	clock.Advance(time.Minute)
	suppressed := &sequenceRuntime{observations: []*runtime.InstanceObservation{observed(domain.InstanceHealthStatusUnhealthy, clock.Now())}}
	second := f.managedDaemon(clock, staticSpec(key, suppressed, testPolicy(false)), nil)
	active, err := second.state.GetActiveMaintenanceOverride(ctx, key, clock.Now())
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, override.ID, active.ID)
	require.NoError(t, second.supervisor.EvaluateOnce(ctx))
	require.Zero(t, suppressed.restarts, "restart must keep the canonical maintenance override")

	// Clearing it is canonical too.
	require.NoError(t, second.supervisor.ClearMaintenanceOverride(ctx, key, "operator"))
	clock.Advance(time.Minute)
	released := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusRunning, clock.Now().Add(time.Second)),
	}}
	third := f.managedDaemon(clock, staticSpec(key, released, testPolicy(false)), nil)
	// The instance has been failing since the override began; a restarted
	// daemon recovers it once the override is cleared.
	require.NoError(t, third.supervisor.EvaluateOnce(ctx))
	require.Equal(t, 1, released.restarts)
	require.Len(t, f.stored(kinds.CASControlState, managedMaintenanceSchema), 1, "one canonical maintenance record per instance")
}

// indexedManagedState is the SQL index of a deployment that predates the
// canonical ledger and maintenance records.
type indexedManagedState struct {
	*supervisorRepoFake
	ledgerReads, overrideReads int
}

func (i *indexedManagedState) ListAllHealth(context.Context) ([]domain.ManagedInstanceHealth, error) {
	return []domain.ManagedInstanceHealth{*i.health}, nil
}
func (i *indexedManagedState) ListRecentRecoveryAttempts(ctx context.Context, key domain.ManagedInstanceKey, limit int) ([]domain.RecoveryAttempt, error) {
	i.ledgerReads++
	return i.supervisorRepoFake.ListRecentRecoveryAttempts(ctx, key, limit)
}
func (i *indexedManagedState) GetActiveMaintenanceOverride(ctx context.Context, key domain.ManagedInstanceKey, at time.Time) (*domain.MaintenanceOverride, error) {
	i.overrideReads++
	return i.supervisorRepoFake.GetActiveMaintenanceOverride(ctx, key, at)
}

func TestLocalManagedInstanceBackfillPublishesIndexedLedgerAndOverrideOnce(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()
	index := &indexedManagedState{supervisorRepoFake: &supervisorRepoFake{
		health:   &domain.ManagedInstanceHealth{ManagedInstanceKey: key, Status: domain.InstanceHealthStatusUnhealthy},
		attempts: []domain.RecoveryAttempt{{ID: uuid.New(), ManagedInstanceKey: key, CorrelationID: "before-upgrade", RequestedAt: clock.Now().Add(-10 * time.Minute), Result: domain.RecoveryAttemptFailed}},
		override: &domain.MaintenanceOverride{ID: uuid.New(), ManagedInstanceKey: key, Actor: "operator", Reason: "migration", CreatedAt: clock.Now().Add(-time.Hour)},
	}}

	upgraded := f.managedDaemon(clock, StaticSupervisionSpecSource{}, index)
	require.NoError(t, upgraded.state.BackfillFromIndex(ctx))
	require.Equal(t, 1, upgraded.publisher.published(kinds.CASControlState, managedRecoveryLedgerSchema))
	require.Equal(t, 1, upgraded.publisher.published(kinds.CASControlState, managedMaintenanceSchema))

	// The backfilled records are what supervision now decides from.
	withoutSQL := f.managedDaemon(clock, StaticSupervisionSpecSource{}, nil)
	attempts, err := withoutSQL.state.ListRecentRecoveryAttempts(ctx, key, 10)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, "before-upgrade", attempts[0].CorrelationID)
	active, err := withoutSQL.state.GetActiveMaintenanceOverride(ctx, key, clock.Now())
	require.NoError(t, err)
	require.NotNil(t, active)
	require.Equal(t, index.override.ID, active.ID)

	// A later start finds the canonical records and reads nothing from SQL.
	index.ledgerReads, index.overrideReads = 0, 0
	later := f.managedDaemon(clock, StaticSupervisionSpecSource{}, index)
	require.NoError(t, later.state.BackfillFromIndex(ctx))
	require.Zero(t, later.publisher.total(), "backfill must not publish a record twice")
	require.Zero(t, index.ledgerReads)
	require.Zero(t, index.overrideReads)
}

// scriptedLock is a shared runtime apply lock with a scripted outcome.
type scriptedLock struct {
	acquired bool
	err      error
	unlocked int
}

func (l *scriptedLock) TryLock(context.Context, uuid.UUID) (func(), bool, error) {
	if l.err != nil || !l.acquired {
		return nil, false, l.err
	}
	return func() { l.unlocked++ }, true, nil
}

func TestSupervisionApplyLockSerializesPerEnvironmentAndSurvivesSharedLockOutage(t *testing.T) {
	ctx := context.Background()
	environment := uuid.New()

	local := NewSupervisionApplyLock(nil, nil)
	unlock, acquired, err := local.TryLock(ctx, environment)
	require.NoError(t, err)
	require.True(t, acquired)
	_, acquired, err = local.TryLock(ctx, environment)
	require.NoError(t, err)
	require.False(t, acquired, "one apply per environment")
	unlockOther, acquired, err := local.TryLock(ctx, uuid.New())
	require.NoError(t, err)
	require.True(t, acquired, "environments do not block each other")
	unlockOther()
	unlock()
	unlock, acquired, err = local.TryLock(ctx, environment)
	require.NoError(t, err)
	require.True(t, acquired)
	unlock()

	// A deploy holds the shared lock: the recovery waits for its next turn.
	shared := &scriptedLock{}
	withShared := NewSupervisionApplyLock(shared, nil)
	_, acquired, err = withShared.TryLock(ctx, environment)
	require.NoError(t, err)
	require.False(t, acquired)

	shared.acquired = true
	unlock, acquired, err = withShared.TryLock(ctx, environment)
	require.NoError(t, err)
	require.True(t, acquired)
	unlock()
	require.Equal(t, 1, shared.unlocked)

	require.False(t, withShared.Status().Fallback)

	// The database behind the shared lock is unreachable: recovery proceeds,
	// and the fallback is observable.
	shared.err = errors.New("connection refused")
	unlock, acquired, err = withShared.TryLock(ctx, environment)
	require.NoError(t, err)
	require.True(t, acquired, "an unreachable shared lock must not block recovery")
	status := withShared.Status()
	require.True(t, status.Shared)
	require.True(t, status.Fallback, "the fallback is reported while the shared lock is unreachable")
	require.False(t, status.FallbackSince.IsZero())
	require.Equal(t, "connection refused", status.LastError)
	_, acquired, err = withShared.TryLock(ctx, environment)
	require.NoError(t, err)
	require.False(t, acquired, "the process-local lock still serializes")
	unlock()

	// The shared lock answers again: the fallback ends.
	shared.err = nil
	unlock, acquired, err = withShared.TryLock(ctx, environment)
	require.NoError(t, err)
	require.True(t, acquired)
	unlock()
	require.False(t, withShared.Status().Fallback)
	require.False(t, local.Status().Shared, "a lock without a shared lock is never in fallback")
	require.False(t, local.Status().Fallback)
}

// signalObserver reports each observation on a channel.
type signalObserver struct{ observed chan struct{} }

func (o signalObserver) ObserveInstance(context.Context, domain.ManagedInstanceKey) (*runtime.InstanceObservation, error) {
	select {
	case o.observed <- struct{}{}:
	default:
	}
	return &runtime.InstanceObservation{Status: domain.InstanceHealthStatusHealthy, ObservedAt: time.Now().UTC()}, nil
}

func TestManagedInstanceSupervisorRunWaitsForLocalStoreReadiness(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	observer := signalObserver{observed: make(chan struct{}, 1)}
	source := StaticSupervisionSpecSource{{Key: testKey(), SupervisorType: domain.InstanceSupervisorDocker, DesiredRunning: true, Observer: observer, RecoveryPolicy: testPolicy(true)}}
	daemon := f.managedDaemon(clock, source, nil)
	gate := newReadinessGate()
	daemon.supervisor.SetReadiness(gate)

	// Before catch-up Run only waits: stopping it finds nothing observed.
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, daemon.supervisor.Run(stopped))
	require.Empty(t, observer.observed)
	require.Zero(t, daemon.publisher.total())

	// After catch-up the first evaluation runs at once.
	gate.open()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.supervisor.Run(ctx) }()
	<-observer.observed
	stop()
	require.NoError(t, <-done)
}

func TestLocalManagedInstanceHealthPublishFailureIsRetriedAndDoesNotBlockRecovery(t *testing.T) {
	f := newSupervisionFixture(t)
	clock := newTestClock()
	key := testKey()
	ctx := context.Background()
	instances := &sequenceRuntime{observations: []*runtime.InstanceObservation{
		observed(domain.InstanceHealthStatusUnhealthy, clock.Now()),
		observed(domain.InstanceHealthStatusRunning, clock.Now().Add(time.Second)),
	}}

	// The health projector's relay path refuses its observables; the recovery
	// ledger's stays healthy.
	bus := &eventBusFake{}
	refused := f.publisher()
	refused.failWith(errors.New("relay rejected the event"))
	state := NewLocalManagedInstanceState(f.state, nil, f.publisher(), nil)
	state.now = clock.Now
	supervisor, err := NewManagedInstanceSupervisor(staticSpec(key, instances, testPolicy(false)), state, NewSupervisionApplyLock(nil, nil), bus, time.Second, zap.NewNop())
	require.NoError(t, err)
	supervisor.now = clock.Now
	supervisor.SetCanonicalProjector(NewManagedInstanceHealthProjector(bus, refused, nil))

	require.Error(t, supervisor.EvaluateOnce(ctx))
	require.Equal(t, 1, instances.restarts, "an unpublishable observation must not hold back recovery")
	require.Zero(t, publishedEventCount(bus.published, events.EventRuntimeInstanceHealthChanged), "an unpublished observation is not announced")
	health, err := state.GetHealth(ctx, key)
	require.NoError(t, err)
	require.Nil(t, health, "an unpublished observation is not recorded")

	// The next evaluation publishes the observation once and settles the
	// restart it could not report, without restarting again.
	refused.failWith(nil)
	clock.Advance(time.Minute)
	require.NoError(t, supervisor.EvaluateOnce(ctx))
	require.Equal(t, 1, instances.restarts)
	require.Equal(t, 1, refused.published(kinds.CASControlState, managedHealthStateSchema))
	require.Equal(t, 1, publishedEventCount(bus.published, events.EventRuntimeInstanceHealthChanged))
	require.Equal(t, 1, publishedEventCount(bus.published, events.EventRuntimeRecoveryCompleted))
	_, attempts := f.storedLedger()
	require.Len(t, attempts, 1)
	require.Equal(t, domain.RecoveryAttemptSuccess, attempts[0].Result)

	require.NoError(t, supervisor.EvaluateOnce(ctx))
	require.Equal(t, 1, refused.published(kinds.CASControlState, managedHealthStateSchema), "an unchanged observation is not published again")
}
