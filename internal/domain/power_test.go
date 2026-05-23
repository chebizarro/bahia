package domain

import (
	"testing"
	"time"
)

func TestPowerObservationCarriesTelemetry(t *testing.T) {
	observedAt := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	obs := PowerObservation{
		Source:         "worker-agent",
		WorkerPubKey:   "worker-pubkey",
		UPSRuntime:     9 * time.Minute,
		BatteryPercent: 17.5,
		ThermalState:   PowerThermalStateCritical,
		ObservedAt:     observedAt,
	}

	if obs.Source != "worker-agent" {
		t.Fatalf("Source = %q", obs.Source)
	}
	if obs.WorkerPubKey != "worker-pubkey" {
		t.Fatalf("WorkerPubKey = %q", obs.WorkerPubKey)
	}
	if obs.UPSRuntime != 9*time.Minute {
		t.Fatalf("UPSRuntime = %s", obs.UPSRuntime)
	}
	if obs.BatteryPercent != 17.5 {
		t.Fatalf("BatteryPercent = %f", obs.BatteryPercent)
	}
	if obs.ThermalState != PowerThermalStateCritical {
		t.Fatalf("ThermalState = %q", obs.ThermalState)
	}
	if !obs.ObservedAt.Equal(observedAt) {
		t.Fatalf("ObservedAt = %s", obs.ObservedAt)
	}
}

func TestPowerRecommendationCarriesAdvisoryContinuityMode(t *testing.T) {
	rec := PowerRecommendation{
		ServiceKey:      "svc-api",
		RecommendedMode: ContinuityModeDegraded,
		Reason:          "battery below 20%",
		AutoExecute:     false,
	}

	if rec.ServiceKey != "svc-api" {
		t.Fatalf("ServiceKey = %q", rec.ServiceKey)
	}
	if rec.RecommendedMode != ContinuityModeDegraded {
		t.Fatalf("RecommendedMode = %q", rec.RecommendedMode)
	}
	if rec.Reason != "battery below 20%" {
		t.Fatalf("Reason = %q", rec.Reason)
	}
	if rec.AutoExecute {
		t.Fatalf("AutoExecute must default to advisory false")
	}
}
