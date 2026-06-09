package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// fakeK8sDriftObserver — DriftObserver for Kubernetes test scenarios.
// Distinct from fakeDriftObserver (drift_comparator_test.go) to keep
// K8s test intent explicit; both satisfy the same DriftObserver interface.
// ---------------------------------------------------------------------------

type fakeK8sDriftObserver struct {
	obs *domain.RuntimeObservation
	err error
}

func (f *fakeK8sDriftObserver) ObserveForDrift(_ context.Context, _ *domain.DesiredServiceSpec) (*domain.RuntimeObservation, error) {
	return f.obs, f.err
}

// ---------------------------------------------------------------------------
// k8sDriftTestSpec — minimal DesiredServiceSpec for K8s drift tests.
// Uses different UUIDs from driftTestDesiredSpec to avoid cross-test collisions.
// ---------------------------------------------------------------------------

func k8sDriftTestSpec() *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
		EnvironmentID:    uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"),
		ArtifactID:       uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003"),
		StableServiceKey: "k8s-svc",
		ImageRef:         "registry.example.com/app:v1.0.0",
		RestartPolicy:    "Always",
	}
	spec.ComputeDesiredHash()
	return spec
}

// k8sTestObservation builds a RuntimeObservation with Source: "kubernetes".
// hash is placed in both NormalizedState.ObservationHash and NormalizedHash
// to exercise both fallback paths in CompareDrift.
func k8sTestObservation(hash string, health domain.HealthStatus) *domain.RuntimeObservation {
	obs := &domain.RuntimeObservation{
		ID:           uuid.New(),
		HealthStatus: health,
		Source:       "kubernetes",
	}
	if hash != "" {
		obs.NormalizedState = &domain.NormalizedObservation{
			ObservationHash: hash,
		}
		obs.NormalizedHash = hash
	}
	return obs
}

// ---------------------------------------------------------------------------
// Kubernetes-specific drift tests
// ---------------------------------------------------------------------------

// TestKubernetesDrift_InSync verifies that matching hashes with healthy status
// produce DriftStatusInSync through the CompareDrift path.
func TestKubernetesDrift_InSync(t *testing.T) {
	t.Parallel()
	spec := k8sDriftTestSpec()

	observer := &fakeK8sDriftObserver{
		obs: k8sTestObservation(spec.DesiredHash, domain.HealthStatusHealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusInSync)
	}
	if state.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash = %q, want %q", state.DesiredHash, spec.DesiredHash)
	}
	if state.ObservedHash != spec.DesiredHash {
		t.Errorf("ObservedHash = %q, want %q", state.ObservedHash, spec.DesiredHash)
	}
	if state.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("HealthStatus = %q, want %q", state.HealthStatus, domain.HealthStatusHealthy)
	}
	if state.Reason == "" {
		t.Error("Reason should be non-empty for in_sync state")
	}
}

// TestKubernetesDrift_Drifted verifies that differing hashes (e.g. a stale
// Deployment manifest in the cluster) produce DriftStatusDrifted.
func TestKubernetesDrift_Drifted(t *testing.T) {
	t.Parallel()
	spec := k8sDriftTestSpec()

	// Observation reports a different hash — Deployment manifest in the
	// cluster was applied with an older desired spec.
	stalePodHash := "sha256:k8s-stale-deployment-hash"
	observer := &fakeK8sDriftObserver{
		obs: k8sTestObservation(stalePodHash, domain.HealthStatusHealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusDrifted)
	}
	if state.ObservedHash != stalePodHash {
		t.Errorf("ObservedHash = %q, want %q", state.ObservedHash, stalePodHash)
	}
	if state.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash = %q, want %q", state.DesiredHash, spec.DesiredHash)
	}
	if state.Reason == "" {
		t.Error("Reason should explain the drift")
	}
}

// TestKubernetesDrift_Unknown_NoObservation verifies that a nil observation
// (Deployment not yet created, or not yet scheduled) produces DriftStatusUnknown.
func TestKubernetesDrift_Unknown_NoObservation(t *testing.T) {
	t.Parallel()
	spec := k8sDriftTestSpec()

	// nil observation — Deployment resource not found in namespace.
	observer := &fakeK8sDriftObserver{
		obs: nil,
		err: nil,
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q (nil obs should yield unknown)", state.Status, domain.DriftStatusUnknown)
	}
	if state.DesiredHash == "" {
		t.Error("DesiredHash should still be set when observation is nil")
	}
	if state.ObservedHash != "" {
		t.Error("ObservedHash should be empty when observation is nil")
	}
	if state.Reason == "" {
		t.Error("Reason should explain why status is unknown")
	}
}

// TestKubernetesDrift_Unknown_ObservationError verifies that a kubectl/API
// error (cluster unreachable, permission denied) produces DriftStatusUnknown
// without propagating the error to the caller.
func TestKubernetesDrift_Unknown_ObservationError(t *testing.T) {
	t.Parallel()
	spec := k8sDriftTestSpec()

	observer := &fakeK8sDriftObserver{
		err: fmt.Errorf("kubectl: connection refused — is the cluster reachable?"),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("observation errors should not propagate as func errors: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusUnknown)
	}
	if state.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash should be set even on error: got %q", state.DesiredHash)
	}
	if state.ObservedHash != "" {
		t.Errorf("ObservedHash should be empty on error: got %q", state.ObservedHash)
	}
}

// TestKubernetesDrift_MissingDeployment verifies that an observation with no
// normalized hash (Deployment exists in cluster but has no Bahia-managed hash
// label) produces DriftStatusUnknown rather than falsely claiming in_sync.
func TestKubernetesDrift_MissingDeployment(t *testing.T) {
	t.Parallel()
	spec := k8sDriftTestSpec()

	// Observation is present but lacks normalized state — Deployment found in
	// cluster but not yet managed by bahia desired-state (no hash annotation).
	observer := &fakeK8sDriftObserver{
		obs: &domain.RuntimeObservation{
			ID:           uuid.New(),
			HealthStatus: domain.HealthStatusUnknown,
			Source:       "kubernetes",
			// No NormalizedState and no NormalizedHash — hash unavailable.
		},
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q (missing hash annotation should yield unknown)", state.Status, domain.DriftStatusUnknown)
	}
	if state.Reason == "" {
		t.Error("Reason should explain why status is unknown")
	}
}
