package service

import (
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestContinuityGraphAssessServiceSelectsHealthyStandbyAndReportsDegradedOnly(t *testing.T) {
	now := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)
	store := newGraphDefinitionStore(t, now, domain.ServiceContinuityProfile{
		ServiceKey:          "svc-api",
		PrimaryWorkerPubKey: "primary-a",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeFull:     {Requires: []string{"primary-db"}},
			domain.ContinuityModeDegraded: {Requires: []string{"postgres-replica"}},
		},
	}, domain.ReplicationPolicy{
		ServiceKey: "svc-api",
		Targets: []domain.ReplicationTarget{
			{WorkerPubKey: "standby-a", Strategy: "event_mirror", MaxStaleness: time.Minute, RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}},
		},
	})
	monitor := newGraphHeartbeatMonitor(now, "standby-a", domain.HeartbeatStatusFresh)
	statuses := NewInMemoryContinuityStatusStore()
	workers := graphWorkerReader{workers: []domain.Worker{
		graphWorker("standby-a", domain.StandbyTierWarm, []domain.ContinuityMode{domain.ContinuityModeFull, domain.ContinuityModeDegraded}, []string{"postgres-replica"}),
	}}
	graph := newTestContinuityGraph(t, store, monitor, statuses, workers, now)

	assessment := graph.AssessService("svc-api")
	require.Equal(t, "svc-api", assessment.ServiceKey)
	require.Equal(t, SurvivabilityDegradedOnly, assessment.Survivability)
	require.Equal(t, []domain.ContinuityMode{domain.ContinuityModeDegraded}, assessment.AvailableProfiles)
	require.Equal(t, "standby-a", assessment.SelectedStandby)
	require.Equal(t, ReplicationFreshnessFresh, assessment.ReplicationFreshness)
	require.Equal(t, StandbyHealthHealthy, assessment.StandbyHealth)
	require.Contains(t, assessment.MissingDependencies, "primary-db")
	require.Contains(t, assessment.MissingDependencies, "replication_target:standby-a")
}

func TestContinuityGraphAssessServiceReportsStaleHeartbeatAsUnsatisfied(t *testing.T) {
	now := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)
	store := newGraphDefinitionStore(t, now, graphProfile("svc-api", "primary-a", domain.ContinuityModeDegraded), domain.ReplicationPolicy{
		ServiceKey: "svc-api",
		Targets: []domain.ReplicationTarget{
			{WorkerPubKey: "standby-a", Strategy: "event_mirror", MaxStaleness: time.Minute, RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}},
		},
	})
	monitor := newGraphHeartbeatMonitor(now, "standby-a", domain.HeartbeatStatusStale)
	workers := graphWorkerReader{workers: []domain.Worker{
		graphWorker("standby-a", domain.StandbyTierHot, []domain.ContinuityMode{domain.ContinuityModeDegraded}, []string{"postgres-replica"}),
	}}
	graph := newTestContinuityGraph(t, store, monitor, NewInMemoryContinuityStatusStore(), workers, now)

	assessment := graph.AssessService("svc-api")
	require.Equal(t, SurvivabilityUnsatisfied, assessment.Survivability)
	require.Empty(t, assessment.AvailableProfiles)
	require.Equal(t, "standby-a", assessment.SelectedStandby)
	require.Equal(t, StandbyHealthStale, assessment.StandbyHealth)
	require.Equal(t, ReplicationFreshnessFresh, assessment.ReplicationFreshness)
	require.Equal(t, []string{"standby_health:standby-a"}, assessment.MissingDependencies)
}

func TestContinuityGraphAssessServiceReportsStaleReplicationAsUnsatisfied(t *testing.T) {
	now := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)
	store := newGraphDefinitionStore(t, now.Add(-2*time.Minute), graphProfile("svc-api", "primary-a", domain.ContinuityModeDegraded), domain.ReplicationPolicy{
		ServiceKey: "svc-api",
		Targets: []domain.ReplicationTarget{
			{WorkerPubKey: "standby-a", Strategy: "event_mirror", MaxStaleness: time.Minute, RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}},
		},
	})
	monitor := newGraphHeartbeatMonitor(now, "standby-a", domain.HeartbeatStatusFresh)
	workers := graphWorkerReader{workers: []domain.Worker{
		graphWorker("standby-a", domain.StandbyTierHot, []domain.ContinuityMode{domain.ContinuityModeDegraded}, []string{"postgres-replica"}),
	}}
	graph := newTestContinuityGraph(t, store, monitor, NewInMemoryContinuityStatusStore(), workers, now)

	assessment := graph.AssessService("svc-api")
	require.Equal(t, SurvivabilityUnsatisfied, assessment.Survivability)
	require.Equal(t, ReplicationFreshnessStale, assessment.ReplicationFreshness)
	require.Equal(t, StandbyHealthHealthy, assessment.StandbyHealth)
	require.Equal(t, []string{"replication_freshness:standby-a"}, assessment.MissingDependencies)
}

func TestContinuityGraphAssessAllIncludesStatusOnlyServicesInDeterministicOrder(t *testing.T) {
	now := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)
	store := NewInMemoryContinuityDefinitionStore()
	stored, err := store.StoreProfile(graphProfile("svc-b", "primary-b", domain.ContinuityModeEmergency))
	require.NoError(t, err)
	require.True(t, stored)
	statuses := NewInMemoryContinuityStatusStore()
	statuses.Update(ContinuityStatus{ServiceKey: "svc-a", ActiveProfile: domain.ContinuityModeFull, OperationState: ContinuityOperationSteady})
	graph := newTestContinuityGraph(t, store, NewInMemoryHeartbeatMonitor(), statuses, graphWorkerReader{}, now)

	assessments := graph.AssessAll()
	require.Len(t, assessments, 2)
	require.Equal(t, "svc-a", assessments[0].ServiceKey)
	require.Equal(t, []string{"continuity_profile"}, assessments[0].MissingDependencies)
	require.Equal(t, "svc-b", assessments[1].ServiceKey)
	require.Equal(t, []string{"standby_worker"}, assessments[1].MissingDependencies)
}

func TestContinuityGraphSimulateFailureExcludesFailedStandby(t *testing.T) {
	now := time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC)
	store := newGraphDefinitionStore(t, now, domain.ServiceContinuityProfile{
		ServiceKey:          "svc-api",
		PrimaryWorkerPubKey: "primary-a",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			domain.ContinuityModeDegraded:  {Requires: []string{"postgres-replica"}},
			domain.ContinuityModeEmergency: {Requires: []string{"static-cache"}},
		},
	}, domain.ReplicationPolicy{
		ServiceKey: "svc-api",
		Targets: []domain.ReplicationTarget{
			{WorkerPubKey: "standby-a", Strategy: "event_mirror", MaxStaleness: time.Minute, RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeDegraded}},
			{WorkerPubKey: "standby-b", Strategy: "snapshot", MaxStaleness: time.Minute, RequiredForModes: []domain.ContinuityMode{domain.ContinuityModeEmergency}},
		},
	})
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "standby-a", ObservedAt: now, Sequence: 1, ExpiresAfter: time.Minute})
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: "standby-b", ObservedAt: now, Sequence: 1, ExpiresAfter: time.Minute})
	workers := graphWorkerReader{workers: []domain.Worker{
		graphWorker("standby-a", domain.StandbyTierHot, []domain.ContinuityMode{domain.ContinuityModeDegraded}, []string{"postgres-replica"}),
		graphWorker("standby-b", domain.StandbyTierWarm, []domain.ContinuityMode{domain.ContinuityModeEmergency}, []string{"static-cache"}),
	}}
	graph := newTestContinuityGraph(t, store, monitor, NewInMemoryContinuityStatusStore(), workers, now)

	baseline := graph.AssessService("svc-api")
	require.Equal(t, SurvivabilityDegradedOnly, baseline.Survivability)
	require.Equal(t, "standby-a", baseline.SelectedStandby)

	simulated := graph.SimulateFailure("standby-a")
	require.Len(t, simulated, 1)
	require.Equal(t, SurvivabilityEmergencyOnly, simulated[0].Survivability)
	require.Equal(t, []domain.ContinuityMode{domain.ContinuityModeEmergency}, simulated[0].AvailableProfiles)
	require.Equal(t, "standby-b", simulated[0].SelectedStandby)
}

func newTestContinuityGraph(t *testing.T, definitions ContinuityDefinitionStore, heartbeats HeartbeatMonitor, statuses ContinuityStatusReader, workers WorkerReader, now time.Time) *ContinuityGraph {
	t.Helper()
	graph, err := NewContinuityGraph(definitions, heartbeats, statuses, workers)
	require.NoError(t, err)
	graph.clock = func() time.Time { return now }
	return graph
}

func newGraphDefinitionStore(t *testing.T, updatedAt time.Time, profile domain.ServiceContinuityProfile, policy domain.ReplicationPolicy) *InMemoryContinuityDefinitionStore {
	t.Helper()
	store := NewInMemoryContinuityDefinitionStore()
	profile.UpdatedAt = updatedAt
	profile.SourceEventID = "profile-event"
	stored, err := store.StoreProfile(profile)
	require.NoError(t, err)
	require.True(t, stored)
	policy.UpdatedAt = updatedAt
	policy.SourceEventID = "replication-event"
	stored, err = store.StoreReplicationPolicy(policy)
	require.NoError(t, err)
	require.True(t, stored)
	return store
}

func graphProfile(serviceKey string, primary string, mode domain.ContinuityMode) domain.ServiceContinuityProfile {
	return domain.ServiceContinuityProfile{
		ServiceKey:          serviceKey,
		PrimaryWorkerPubKey: primary,
		UpdatedAt:           time.Date(2026, 5, 23, 13, 0, 0, 0, time.UTC),
		SourceEventID:       "profile-event",
		Profiles: map[domain.ContinuityMode]domain.ContinuityProfileSpec{
			mode: {Requires: []string{"postgres-replica"}},
		},
	}
}

func graphWorker(pubkey string, tier domain.StandbyTier, profiles []domain.ContinuityMode, features []string) domain.Worker {
	return domain.Worker{
		PubKey: pubkey,
		Capabilities: domain.WorkerCapabilities{
			Features: features,
		},
		StandbyAssignments: []domain.WorkerStandbyAssignment{
			{ServiceKey: "svc-api", Tier: tier, SupportedProfiles: profiles},
		},
	}
}

func newGraphHeartbeatMonitor(now time.Time, pubkey string, status domain.HeartbeatStatus) *InMemoryHeartbeatMonitor {
	monitor := NewInMemoryHeartbeatMonitor()
	monitor.clock = func() time.Time { return now }
	observedAt := now
	switch status {
	case domain.HeartbeatStatusStale:
		observedAt = now.Add(-20 * time.Second)
	case domain.HeartbeatStatusExpired:
		observedAt = now.Add(-2 * time.Minute)
	}
	monitor.Observe(domain.HeartbeatObservation{WorkerPubKey: pubkey, ObservedAt: observedAt, Sequence: 1, ExpiresAfter: 30 * time.Second})
	return monitor
}

type graphWorkerReader struct {
	workers []domain.Worker
}

func (r graphWorkerReader) ListWorkers() []domain.Worker {
	out := make([]domain.Worker, len(r.workers))
	copy(out, r.workers)
	return out
}
