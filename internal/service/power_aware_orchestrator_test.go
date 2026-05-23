package service

import (
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestInMemoryPowerAwareOrchestratorObservePowerStoresLatestByWorker(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	orchestrator := NewInMemoryPowerAwareOrchestrator(nil)

	orchestrator.ObservePower(domain.PowerObservation{
		Source:         " worker-agent ",
		WorkerPubKey:   " worker-a ",
		UPSRuntime:     8 * time.Minute,
		BatteryPercent: 35,
		ThermalState:   " CRITICAL ",
		ObservedAt:     now,
	})
	orchestrator.ObservePower(domain.PowerObservation{
		WorkerPubKey:   "worker-a",
		UPSRuntime:     30 * time.Minute,
		BatteryPercent: 90,
		ThermalState:   domain.PowerThermalStateNormal,
		ObservedAt:     now.Add(-time.Minute),
	})

	obs, ok := orchestrator.GetWorkerPowerState(" worker-a ")
	require.True(t, ok)
	require.Equal(t, "worker-agent", obs.Source)
	require.Equal(t, "worker-a", obs.WorkerPubKey)
	require.Equal(t, 8*time.Minute, obs.UPSRuntime)
	require.Equal(t, 35.0, obs.BatteryPercent)
	require.Equal(t, domain.PowerThermalStateCritical, obs.ThermalState)

	obs.BatteryPercent = 1
	again, ok := orchestrator.GetWorkerPowerState("worker-a")
	require.True(t, ok)
	require.Equal(t, 35.0, again.BatteryPercent)
}

func TestInMemoryPowerAwareOrchestratorRecommendDegradationAdvisoryFirst(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	orchestrator := NewInMemoryPowerAwareOrchestrator([]PowerManagedService{
		{ServiceKey: " svc-critical ", WorkerPubKey: "worker-ups", Critical: true},
		{ServiceKey: "svc-non-critical", WorkerPubKey: "worker-ups"},
		{ServiceKey: "svc-compute", WorkerPubKey: "worker-thermal", ComputeHeavy: true},
		{ServiceKey: "svc-light", WorkerPubKey: "worker-thermal"},
	})

	orchestrator.ObservePower(domain.PowerObservation{
		WorkerPubKey:   "worker-ups",
		UPSRuntime:     9 * time.Minute,
		BatteryPercent: 15,
		ThermalState:   domain.PowerThermalStateNormal,
		ObservedAt:     now,
	})
	orchestrator.ObservePower(domain.PowerObservation{
		WorkerPubKey:   "worker-thermal",
		UPSRuntime:     30 * time.Minute,
		BatteryPercent: 80,
		ThermalState:   domain.PowerThermalStateCritical,
		ObservedAt:     now,
	})

	recommendations := orchestrator.RecommendDegradation()
	require.Len(t, recommendations, 3)

	byService := map[string]domain.PowerRecommendation{}
	for _, rec := range recommendations {
		require.False(t, rec.AutoExecute, "power recommendations must be advisory unless explicitly enabled elsewhere")
		byService[rec.ServiceKey] = rec
	}

	require.Equal(t, domain.ContinuityModeDegraded, byService["svc-critical"].RecommendedMode)
	require.Contains(t, byService["svc-critical"].Reason, "battery below 20%")
	require.NotContains(t, byService["svc-critical"].Reason, "non-critical service")

	require.Equal(t, domain.ContinuityModeEmergency, byService["svc-non-critical"].RecommendedMode)
	require.Contains(t, byService["svc-non-critical"].Reason, "UPS runtime below 10m0s")
	require.Contains(t, byService["svc-non-critical"].Reason, "battery below 20%")

	require.Equal(t, domain.ContinuityModeDegraded, byService["svc-compute"].RecommendedMode)
	require.Contains(t, byService["svc-compute"].Reason, "critical thermal state")

	_, exists := byService["svc-light"]
	require.False(t, exists, "non-compute-heavy services should not degrade solely because of thermal critical state")
}

func TestInMemoryPowerAwareOrchestratorRecommendationOrderIsDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	orchestrator := NewInMemoryPowerAwareOrchestrator([]PowerManagedService{
		{ServiceKey: "svc-z", WorkerPubKey: "worker-a"},
		{ServiceKey: "svc-a", WorkerPubKey: "worker-a"},
	})
	orchestrator.ObservePower(domain.PowerObservation{
		WorkerPubKey:   "worker-a",
		UPSRuntime:     5 * time.Minute,
		BatteryPercent: 50,
		ObservedAt:     now,
	})

	recommendations := orchestrator.RecommendDegradation()
	require.Len(t, recommendations, 2)
	require.Equal(t, "svc-a", recommendations[0].ServiceKey)
	require.Equal(t, "svc-z", recommendations[1].ServiceKey)
}

func TestInMemoryPowerAwareOrchestratorSkipsUnknownWorkersAndMissingServices(t *testing.T) {
	orchestrator := NewInMemoryPowerAwareOrchestrator([]PowerManagedService{
		{ServiceKey: " ", WorkerPubKey: "worker-a"},
		{ServiceKey: "svc-a", WorkerPubKey: " "},
		{ServiceKey: "svc-b", WorkerPubKey: "worker-b"},
	})

	orchestrator.ObservePower(domain.PowerObservation{WorkerPubKey: "worker-a", ObservedAt: time.Now().UTC(), UPSRuntime: time.Minute})
	require.Empty(t, orchestrator.RecommendDegradation())

	_, ok := orchestrator.GetWorkerPowerState("missing")
	require.False(t, ok)
}

func TestPowerRecommendationForServiceCombinesReasonsWithoutAutoExecute(t *testing.T) {
	rec, ok := powerRecommendationForService(
		PowerManagedService{ServiceKey: "svc", WorkerPubKey: "worker", ComputeHeavy: true},
		domain.PowerObservation{UPSRuntime: time.Minute, BatteryPercent: 10, ThermalState: domain.PowerThermalStateCritical},
	)

	require.True(t, ok)
	require.Equal(t, domain.ContinuityModeEmergency, rec.RecommendedMode)
	require.False(t, rec.AutoExecute)
	require.True(t, strings.Contains(rec.Reason, "UPS runtime below 10m0s"))
	require.True(t, strings.Contains(rec.Reason, "battery below 20%"))
	require.True(t, strings.Contains(rec.Reason, "critical thermal state"))
}
