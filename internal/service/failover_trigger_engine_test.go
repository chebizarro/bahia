package service

import (
	"context"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestFailoverTriggerEngineTransitionsSuspectThenFiringAndPublishesOnce(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	store := triggerEngineStore(t, now, []domain.ReplicationTarget{{WorkerPubKey: "standby-a", Strategy: "event_mirror", RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}}})
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now.Add(-31 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	publisher := &recordingPublisher{}
	engine := newTestFailoverTriggerEngine(t, monitor, store, publisher, now)

	engine.EvaluateOnce(context.Background())
	state, ok := engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseSuspect, state.Phase)
	require.Empty(t, state.ActiveRunID)
	require.Empty(t, publisher.eventsOfType(EventFailoverRequested))

	engine.EvaluateOnce(context.Background())
	state, ok = engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseFiring, state.Phase)
	require.Equal(t, failoverRunID("svc-api", now), state.ActiveRunID)
	requested := publisher.eventsOfType(EventFailoverRequested)
	require.Len(t, requested, 1)
	payload, ok := requested[0].Data.(FailoverRequested)
	require.True(t, ok)
	require.Equal(t, "svc-api", payload.ServiceKey)
	require.Equal(t, "primary-a", payload.PrimaryWorkerPubKey)
	require.Equal(t, "standby-a", payload.StandbyWorkerPubKey)
	require.Equal(t, domain.RecipeTriggerTypeHeartbeatLoss, payload.TriggerType)
	require.True(t, payload.LastHeartbeatAt.Equal(now.Add(-31*time.Second)))

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now, Sequence: 2, ExpiresAfter: 30 * time.Second})
	engine.EvaluateOnce(context.Background())
	state, ok = engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseFiring, state.Phase)
	require.Len(t, publisher.eventsOfType(EventFailoverRequested), 1)
}

func TestFailoverTriggerEngineFreshHeartbeatKeepsHealthyAndClearsSuspect(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	store := triggerEngineStore(t, now, []domain.ReplicationTarget{{WorkerPubKey: "standby-a", Strategy: "event_mirror"}})
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now.Add(-31 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	publisher := &recordingPublisher{}
	engine := newTestFailoverTriggerEngine(t, monitor, store, publisher, now)

	engine.EvaluateOnce(context.Background())
	state, ok := engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseSuspect, state.Phase)

	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now, Sequence: 2, ExpiresAfter: 30 * time.Second})
	engine.EvaluateOnce(context.Background())
	state, ok = engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseHealthy, state.Phase)
	require.Empty(t, publisher.eventsOfType(EventFailoverRequested))
}

func TestFailoverTriggerEngineNoEligibleStandbyEmitsFailedOnce(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	store := triggerEngineStore(t, now, []domain.ReplicationTarget{{WorkerPubKey: "primary-a", Strategy: "event_mirror"}})
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now.Add(-31 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	publisher := &recordingPublisher{}
	engine := newTestFailoverTriggerEngine(t, monitor, store, publisher, now)

	engine.EvaluateOnce(context.Background())
	engine.EvaluateOnce(context.Background())
	state, ok := engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseFiring, state.Phase)
	require.NotEmpty(t, state.ActiveRunID)
	require.Empty(t, publisher.eventsOfType(EventFailoverRequested))
	failed := publisher.eventsOfType(EventFailoverRequestFailed)
	require.Len(t, failed, 1)
	payload, ok := failed[0].Data.(FailoverRequestFailed)
	require.True(t, ok)
	require.Equal(t, "svc-api", payload.ServiceKey)
	require.Equal(t, "primary-a", payload.PrimaryWorkerPubKey)
	require.Contains(t, payload.Reason, "no eligible standby")

	engine.EvaluateOnce(context.Background())
	require.Len(t, publisher.eventsOfType(EventFailoverRequestFailed), 1)
}

func TestFailoverTriggerEngineSkipsUnarmedServiceWithoutHeartbeatSnapshot(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	store := triggerEngineStore(t, now, []domain.ReplicationTarget{{WorkerPubKey: "standby-a", Strategy: "event_mirror"}})
	monitor := NewInMemoryHeartbeatMonitor()
	publisher := &recordingPublisher{}
	engine := newTestFailoverTriggerEngine(t, monitor, store, publisher, now)

	engine.EvaluateOnce(context.Background())
	_, ok := engine.State("svc-api")
	require.False(t, ok)
	require.Empty(t, publisher.eventsOfType(EventFailoverRequested))
	require.Empty(t, publisher.eventsOfType(EventFailoverRequestFailed))
}

func TestFailoverTriggerEngineMarkActiveOnlyForMatchingFiringRun(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	store := triggerEngineStore(t, now, []domain.ReplicationTarget{{WorkerPubKey: "standby-a", Strategy: "event_mirror"}})
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "primary-a", ObservedAt: now.Add(-31 * time.Second), Sequence: 1, ExpiresAfter: 30 * time.Second})
	publisher := &recordingPublisher{}
	engine := newTestFailoverTriggerEngine(t, monitor, store, publisher, now)

	engine.EvaluateOnce(context.Background())
	engine.EvaluateOnce(context.Background())
	state, ok := engine.State("svc-api")
	require.True(t, ok)
	require.False(t, engine.MarkActive("svc-api", "different-run", now.Add(time.Second)))
	require.True(t, engine.MarkActive("svc-api", state.ActiveRunID, now.Add(time.Second)))
	state, ok = engine.State("svc-api")
	require.True(t, ok)
	require.Equal(t, TriggerPhaseActive, state.Phase)
	require.True(t, state.LastChangeAt.Equal(now.Add(time.Second)))
}

func newTestFailoverTriggerEngine(t *testing.T, monitor HeartbeatMonitor, store ContinuityDefinitionStore, publisher events.Publisher, now time.Time) *FailoverTriggerEngine {
	t.Helper()
	engine, err := NewFailoverTriggerEngine(monitor, store, publisher, time.Minute, zap.NewNop())
	require.NoError(t, err)
	engine.clock = func() time.Time { return now }
	return engine
}

func triggerEngineStore(t *testing.T, now time.Time, targets []domain.ReplicationTarget) *InMemoryContinuityDefinitionStore {
	t.Helper()
	store := NewInMemoryContinuityDefinitionStore()
	stored, err := store.StoreProfile(validProfile("svc-api", "primary-a", now, "profile-event"))
	require.NoError(t, err)
	require.True(t, stored)
	stored, err = store.StoreRecipe(validFailoverRecipe("svc-api", "primary-loss", now, "recipe-event"))
	require.NoError(t, err)
	require.True(t, stored)
	stored, err = store.StoreReplicationPolicy(domain.ReplicationPolicy{
		ServiceKey:    "svc-api",
		UpdatedAt:     now,
		SourceEventID: "replication-event",
		Targets:       targets,
	})
	require.NoError(t, err)
	require.True(t, stored)
	return store
}
