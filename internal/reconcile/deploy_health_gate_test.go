package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
)

// newHealthGateReconciler builds a reconciler whose desired hash matches the
// observed normalized hash, so convergence is decided purely by runtime health.
func newHealthGateReconciler(t *testing.T, health domain.HealthStatus, opts ...Option) (*Reconciler, *mockStateRepo, string) {
	t.Helper()
	serviceID := uuid.New()
	envID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	const desiredHash = "sha256:matching-desired-state"

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredHash: desiredHash},
	}}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "astillero"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: &mockRuntime{observeNormHash: desiredHash, observeHealth: health}},
		&mockPublisher{}, time.Minute, zap.NewNop(), opts...,
	)
	return reconciler, stateRepo, stateKey
}

// TestDeployDoesNotConvergeWhileRuntimeUnhealthy is the regression for the live
// Astillero incident: the desired configuration was applied (hashes matched) but
// the container was crash-looping on a missing secret mount, and Bahia still
// reported in_sync. A matching hash must never be sufficient on its own.
func TestDeployDoesNotConvergeWhileRuntimeUnhealthy(t *testing.T) {
	reconciler, stateRepo, stateKey := newHealthGateReconciler(t, domain.HealthStatusUnhealthy)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}

	state := stateRepo.states[stateKey]
	if state.DriftStatus == domain.DriftStatusInSync {
		t.Fatal("a crash-looping container must not report in_sync even when the desired hash matches")
	}
	evidence, ok := state.ReconcileFailureMetadata[unhealthyEvidenceKey].(string)
	if !ok || evidence == "" {
		t.Fatalf("expected operator-visible evidence, got %#v", state.ReconcileFailureMetadata)
	}
	if got := state.ReconcileFailureMetadata["observed_health"]; got != string(domain.HealthStatusUnhealthy) {
		t.Fatalf("observed_health = %v, want unhealthy", got)
	}
}

// TestHealthyDeployStillConverges guards against over-correction.
func TestHealthyDeployStillConverges(t *testing.T) {
	reconciler, stateRepo, stateKey := newHealthGateReconciler(t, domain.HealthStatusHealthy)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}
	if got := stateRepo.states[stateKey].DriftStatus; got != domain.DriftStatusInSync {
		t.Fatalf("healthy deploy drift status = %q, want in_sync", got)
	}
}

// TestStartingIsPendingNotFailure covers a container that is still coming up:
// it must be reported as deploying, never as a success and never as a failure.
func TestStartingIsPendingNotFailure(t *testing.T) {
	reconciler, stateRepo, stateKey := newHealthGateReconciler(t, domain.HealthStatusStarting)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("reconcileOne() error = %v", err)
	}
	state := stateRepo.states[stateKey]
	if got := state.DriftStatus; got == domain.DriftStatusInSync {
		t.Fatalf("starting drift status = %q, must not claim success", got)
	}
	if got := state.DriftStatus; got == domain.DriftStatusDeploying {
		t.Fatal("starting must not park in deploying; reconcileOne early-returns for deploying units, which would stop health from ever being re-evaluated")
	}
	if _, ok := state.ReconcileFailureMetadata[startingSinceKey]; !ok {
		t.Fatal("expected a starting_since marker so the startup budget can be measured")
	}
	if _, failed := state.ReconcileFailureMetadata[unhealthyEvidenceKey]; failed {
		t.Fatal("a starting container must not be recorded as failed")
	}
}

// TestStartingTimeoutSurfacesFailure proves the startup budget is bounded: a
// unit stuck in "starting" eventually stops being reported as progress.
func TestStartingTimeoutSurfacesFailure(t *testing.T) {
	reconciler, stateRepo, stateKey := newHealthGateReconciler(t, domain.HealthStatusStarting, WithStartingTimeout(time.Minute))

	// First pass records the marker and stays pending.
	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("first reconcileOne() error = %v", err)
	}
	if got := stateRepo.states[stateKey].DriftStatus; got == domain.DriftStatusInSync {
		t.Fatalf("first pass drift status = %q, must not claim success", got)
	}

	// Backdate the marker beyond the budget rather than sleeping.
	stateRepo.states[stateKey].ReconcileFailureMetadata[startingSinceKey] =
		time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("second reconcileOne() error = %v", err)
	}
	state := stateRepo.states[stateKey]
	if state.DriftStatus != domain.DriftStatusDrifted && state.DriftStatus != domain.DriftStatusRemediationNeeded {
		t.Fatalf("a unit past its startup budget must be reported as failing, got %q", state.DriftStatus)
	}
	evidence, _ := state.ReconcileFailureMetadata[unhealthyEvidenceKey].(string)
	if evidence == "" {
		t.Fatal("startup timeout must record why the unit never became healthy")
	}
}

// TestStartingTimeoutMarkerIsNotResetEachPass guards the measurement itself: the
// marker must record when starting began, not when it was last observed, or the
// budget could never elapse.
func TestStartingTimeoutMarkerIsNotResetEachPass(t *testing.T) {
	reconciler, stateRepo, stateKey := newHealthGateReconciler(t, domain.HealthStatusStarting)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("first reconcileOne() error = %v", err)
	}
	first := stateRepo.states[stateKey].ReconcileFailureMetadata[startingSinceKey]

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("second reconcileOne() error = %v", err)
	}
	if second := stateRepo.states[stateKey].ReconcileFailureMetadata[startingSinceKey]; second != first {
		t.Fatalf("starting_since moved between passes: %v -> %v", first, second)
	}
}

// TestConvergenceClearsStartingMarker ensures the marker cannot leak into a
// later deployment episode once the unit becomes healthy.
func TestConvergenceClearsStartingMarker(t *testing.T) {
	rt := &mockRuntime{observeNormHash: "sha256:matching-desired-state", observeHealth: domain.HealthStatusStarting}
	serviceID := uuid.New()
	envID := uuid.New()
	stateKey := stateMapKey(serviceID, envID)
	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateKey: {ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "sha256:matching-desired-state"},
	}}
	reconciler := NewReconciler(
		&mockServiceRepo{services: map[uuid.UUID]*domain.Service{serviceID: {ID: serviceID, Name: "astillero"}}},
		&mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{envID: {ID: envID, Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultReconcileMode: domain.ReconcileModeObserveOnly}}}},
		&mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}},
		&mockDeploymentUnitRepo{units: map[uuid.UUID]*domain.DeploymentUnit{}},
		&mockObservationRepo{}, stateRepo,
		&mockRuntimeResolver{rt: rt}, &mockPublisher{}, time.Minute, zap.NewNop(),
	)

	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("starting pass error = %v", err)
	}
	if _, ok := stateRepo.states[stateKey].ReconcileFailureMetadata[startingSinceKey]; !ok {
		t.Fatal("expected a starting marker after the first pass")
	}

	rt.observeHealth = domain.HealthStatusHealthy
	if err := reconciler.reconcileOne(context.Background(), stateRepo.states[stateKey]); err != nil {
		t.Fatalf("healthy pass error = %v", err)
	}
	state := stateRepo.states[stateKey]
	if state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("drift status = %q, want in_sync once healthy", state.DriftStatus)
	}
	if state.ReconcileFailureMetadata != nil {
		t.Fatalf("convergence must clear non-converged diagnostics, got %#v", state.ReconcileFailureMetadata)
	}
}
