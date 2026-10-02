package reconcile

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// captureStatePublisher records calls to PublishState and PublishStateTombstone.
type captureStatePublisher struct {
	mu         sync.Mutex
	states     []capturedState
	tombstones []capturedTombstone
}

type capturedState struct {
	State       domain.EnvironmentServiceState
	Observation *domain.RuntimeObservation
}

type capturedTombstone struct {
	ServiceID     uuid.UUID
	EnvironmentID uuid.UUID
}

func (c *captureStatePublisher) PublishState(_ context.Context, state *domain.EnvironmentServiceState, obs *domain.RuntimeObservation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states = append(c.states, capturedState{State: *state, Observation: obs})
	return nil
}

func (c *captureStatePublisher) PublishStateTombstone(_ context.Context, serviceID, envID uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tombstones = append(c.tombstones, capturedTombstone{ServiceID: serviceID, EnvironmentID: envID})
	return nil
}

func (c *captureStatePublisher) stateCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.states)
}

func (c *captureStatePublisher) tombstoneCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tombstones)
}

// TestReconcilerPublishesOneStateEventOnMaterialChange verifies that a runtime
// observation that transitions the material state publishes exactly one
// canonical state event through the RuntimeStatePublisher.
func TestReconcilerPublishesOneStateEventOnMaterialChange(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	desiredHash := "sha256:" + strings.Repeat("a", 64)
	observedHash := "sha256:" + strings.Repeat("b", 64) // different → drifted
	stateKey := stateMapKey(serviceID, envID)

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID: serviceID, EnvironmentID: envID,
			DesiredHash: desiredHash, DriftStatus: domain.DriftStatusInSync,
		},
	}}
	rt := &mockRuntime{observeNormHash: observedHash, observeHealth: domain.HealthStatusHealthy}
	capture := &captureStatePublisher{}
	bus := &mockPublisher{}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: rt},
		bus, time.Minute, zap.NewNop(),
	)
	reconciler.SetRuntimeStatePublisher(capture)

	require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))

	// Material state changed (in_sync → drifted) so exactly one state event
	// must be published.
	require.Equal(t, 1, capture.stateCount(), "exactly one state event on material change")
	require.Equal(t, 0, capture.tombstoneCount(), "no tombstone for live state")

	// The bus event is also published.
	bus.mu.Lock()
	stateEvents := 0
	for _, e := range bus.events {
		if e.Type == events.EventEnvironmentServiceStateChanged {
			stateEvents++
		}
	}
	bus.mu.Unlock()
	require.Equal(t, 1, stateEvents, "bus event must also be published")
}

// TestReconcilerPublishesZeroEventsOnUnchangedState verifies that a repeat
// observation with unchanged material state publishes zero state events.
func TestReconcilerPublishesZeroEventsOnUnchangedState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	desiredHash := "sha256:" + strings.Repeat("a", 64)
	stateKey := stateMapKey(serviceID, envID)

	obsID := uuid.New()
	now := time.Now().UTC()
	// Seed an existing observation so the first reconcile does not see a
	// "no observation → observation" material transition.
	obsRepo := &mockObservationRepo{
		observations: []domain.RuntimeObservation{{
			ID: obsID, ServiceID: serviceID, EnvironmentID: envID,
			NormalizedHash: desiredHash, HealthStatus: domain.HealthStatusHealthy,
			Source: "mock", ObservedAt: now,
		}},
	}
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID: serviceID, EnvironmentID: envID,
			DesiredHash: desiredHash, DriftStatus: domain.DriftStatusInSync,
			CurrentObservationID: &obsID,
			LastReconciledAt:     &now,
		},
	}}
	rt := &mockRuntime{observeNormHash: desiredHash, observeHealth: domain.HealthStatusHealthy}
	capture := &captureStatePublisher{}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		obsRepo, stateRepo,
		&mockRuntimeResolver{rt: rt},
		&mockPublisher{}, time.Minute, zap.NewNop(),
	)
	reconciler.SetRuntimeStatePublisher(capture)

	// First observation: in_sync → in_sync (same hash, healthy, same
	// observation-linked state). No material change.
	require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))
	first := capture.stateCount()

	// Second observation: identical material state.
	require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))
	second := capture.stateCount()

	// Neither observation should have produced a state event because the
	// material state (drift status, health, hashes) did not change.
	require.Equal(t, first, second, "repeat observation produces zero additional events")
	require.Equal(t, 0, second, "no material change → zero events total")
}

// TestStateTombstoneHandlerPublishesTombstoneOnDelete verifies that the
// StateTombstoneHandler publishes a tombstone when it receives an
// EventEnvironmentServiceStateChanged with Deleted: true.
func TestStateTombstoneHandlerPublishesTombstoneOnDelete(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	capture := &captureStatePublisher{}
	handler := NewStateTombstoneHandler(capture, zap.NewNop())

	// Call the handler directly (InProcessPublisher dispatches async; calling
	// directly avoids race timing in a unit test).
	handler.handleEvent(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: serviceID.String() + ":" + envID.String(),
		Data: events.ResourceData{
			ServiceID:     serviceID.String(),
			EnvironmentID: envID.String(),
			Deleted:       true,
		},
	})

	require.Equal(t, 1, capture.tombstoneCount(), "tombstone published on delete")
	require.Equal(t, 0, capture.stateCount(), "no live state published on delete")
	capture.mu.Lock()
	require.Equal(t, serviceID, capture.tombstones[0].ServiceID)
	require.Equal(t, envID, capture.tombstones[0].EnvironmentID)
	capture.mu.Unlock()
}

// TestStateTombstoneHandlerIgnoresNonDeletedEvents verifies that the handler
// does not publish tombstones for non-deleted state changes.
func TestStateTombstoneHandlerIgnoresNonDeletedEvents(t *testing.T) {
	ctx := context.Background()
	capture := &captureStatePublisher{}
	handler := NewStateTombstoneHandler(capture, zap.NewNop())

	handler.handleEvent(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: uuid.New().String() + ":" + uuid.New().String(),
		Data: events.ResourceData{
			ServiceID:     uuid.New().String(),
			EnvironmentID: uuid.New().String(),
			Deleted:       false,
		},
	})

	require.Equal(t, 0, capture.tombstoneCount(), "no tombstone for non-deleted event")
}

// TestReconcilerRepairStuckRouteOnlyPublishesState verifies that when the
// reconciler repairs a stuck route-only deploying state, it publishes the
// repaired state via the RuntimeStatePublisher.
func TestReconcilerRepairStuckRouteOnlyPublishesState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	intentID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {
			ServiceID: serviceID, EnvironmentID: envID, DesiredIntentID: &intentID,
			DriftStatus: domain.DriftStatusDeploying,
		},
	}}
	intentRepo := &mockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		intentID: {ID: intentID, ServiceID: serviceID, EnvironmentID: envID, Status: domain.IntentStatusDeployed},
	}}
	now := time.Now().UTC()
	run := domain.DeploymentRun{
		ID: uuid.New(), DeploymentIntentID: intentID,
		LoomJobID: domain.RouteOnlyDeploymentRunLoomJobID, Status: domain.RunStatusSucceeded,
		CreatedAt: now, UpdatedAt: now,
	}
	capture := &captureStatePublisher{}
	reconciler := NewReconciler(nil, nil, nil, nil, &mockObservationRepo{}, stateRepo, nil,
		&mockPublisher{}, time.Minute, zap.NewNop(),
		WithDeploymentHistory(intentRepo, &reconcilerRunRepo{runs: []domain.DeploymentRun{run}}),
	)
	reconciler.SetRuntimeStatePublisher(capture)

	require.NoError(t, reconciler.reconcileOne(ctx, stateRepo.states[stateKey]))

	require.Equal(t, 1, capture.stateCount(), "repaired state published")
	capture.mu.Lock()
	require.Equal(t, domain.DriftStatusInSync, capture.states[0].State.DriftStatus)
	capture.mu.Unlock()
}
