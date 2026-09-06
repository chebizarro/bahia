package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Test helper: fake DriftObserver
// ---------------------------------------------------------------------------

type fakeDriftObserver struct {
	obs *domain.RuntimeObservation
	err error
}

func (f *fakeDriftObserver) ObserveForDrift(_ context.Context, _ *domain.DesiredServiceSpec) (*domain.RuntimeObservation, error) {
	return f.obs, f.err
}

// ---------------------------------------------------------------------------
// Test helper: build a minimal DesiredServiceSpec with a computed hash
// ---------------------------------------------------------------------------

func driftTestDesiredSpec(imageRef string) *domain.DesiredServiceSpec {
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EnvironmentID:    uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ArtifactID:       uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		StableServiceKey: "test-svc",
		ImageRef:         imageRef,
		RestartPolicy:    "unless-stopped",
	}
	spec.ComputeDesiredHash()
	return spec
}

// testObservation builds a RuntimeObservation with a NormalizedState whose
// ObservationHash is set to the given hash. If hash is empty, no normalized
// state is attached.
func testObservation(hash string, health domain.HealthStatus) *domain.RuntimeObservation {
	obs := &domain.RuntimeObservation{
		ID:           uuid.New(),
		HealthStatus: health,
		Source:       "test",
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
// Tests
// ---------------------------------------------------------------------------

func TestCompareDrift_InSync(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusHealthy),
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
}

func TestCompareDrift_InSync_Starting(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Starting health is acceptable for in_sync.
	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusStarting),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("Status = %q, want %q (starting health should be acceptable)", state.Status, domain.DriftStatusInSync)
	}
}

func TestCompareDrift_RouteCarryingDesiredAcceptsPreRouteHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")
	preRouteHash := spec.DesiredHash
	spec.PublicRoute = &domain.DesiredPublicRoutePlan{Hostname: "api.example.com"}
	spec.ComputeDesiredHash()
	observer := &fakeDriftObserver{obs: testObservation(preRouteHash, domain.HealthStatusHealthy)}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Fatalf("Status = %q, want in_sync", state.Status)
	}
	if state.DesiredHash != spec.DesiredHash || state.ObservedHash != preRouteHash {
		t.Fatalf("unexpected convergence hashes: %#v", state)
	}
}

func TestCompareDrift_Drifted_HashMismatch(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		obs: testObservation("sha256:different", domain.HealthStatusHealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusDrifted)
	}
	if state.ObservedHash != "sha256:different" {
		t.Errorf("ObservedHash = %q, want %q", state.ObservedHash, "sha256:different")
	}
	if state.Reason == "" {
		t.Error("Reason should explain the drift")
	}
}

func TestCompareDrift_Drifted_UnhealthyMatchingHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Hashes match but health is unhealthy — should report drifted.
	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusUnhealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("Status = %q, want %q (unhealthy with matching hash should drift)", state.Status, domain.DriftStatusDrifted)
	}
	if state.HealthStatus != domain.HealthStatusUnhealthy {
		t.Errorf("HealthStatus = %q, want %q", state.HealthStatus, domain.HealthStatusUnhealthy)
	}
}

func TestCompareDrift_Drifted_StoppedMatchingHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Hashes match but container is stopped — should report drifted.
	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusStopped),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("Status = %q, want %q (stopped with matching hash should drift)", state.Status, domain.DriftStatusDrifted)
	}
}

func TestCompareDrift_Unknown_ObservationError(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		err: fmt.Errorf("connection refused"),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("observation errors should not propagate: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusUnknown)
	}
	if state.DesiredHash != spec.DesiredHash {
		t.Errorf("DesiredHash should still be set: got %q", state.DesiredHash)
	}
	if state.ObservedHash != "" {
		t.Errorf("ObservedHash should be empty on error: got %q", state.ObservedHash)
	}
}

func TestCompareDrift_Unknown_NilObservation(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Observer returns nil observation (service not found).
	observer := &fakeDriftObserver{
		obs: nil,
		err: nil,
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusUnknown)
	}
	if state.Reason == "" {
		t.Error("Reason should explain why status is unknown")
	}
}

func TestCompareDrift_Unknown_MissingNormalizedHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Observation exists but without normalized hash.
	observer := &fakeDriftObserver{
		obs: &domain.RuntimeObservation{
			ID:           uuid.New(),
			HealthStatus: domain.HealthStatusHealthy,
			Source:       "test",
			// No NormalizedState, no NormalizedHash
		},
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("Status = %q, want %q (no normalized hash available)", state.Status, domain.DriftStatusUnknown)
	}
	if state.HealthStatus != domain.HealthStatusHealthy {
		t.Errorf("HealthStatus should still be captured: got %q", state.HealthStatus)
	}
}

func TestCompareDrift_NilSpec_ReturnsError(t *testing.T) {
	t.Parallel()
	observer := &fakeDriftObserver{}

	_, err := CompareDrift(context.Background(), nil, observer)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestCompareDrift_ComputesHashIfMissing(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EnvironmentID:    uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ArtifactID:       uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		StableServiceKey: "test-svc",
		ImageRef:         "nginx:1.25",
		RestartPolicy:    "unless-stopped",
		// DesiredHash intentionally left empty
	}

	// Pre-compute expected hash.
	expected := spec.ComputeDesiredHash()
	spec.DesiredHash = "" // Reset to test auto-compute.

	observer := &fakeDriftObserver{
		obs: testObservation(expected, domain.HealthStatusHealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusInSync)
	}
	if state.DesiredHash != expected {
		t.Errorf("DesiredHash = %q, want %q", state.DesiredHash, expected)
	}
}

func TestCompareDrift_FallsBackToNormalizedHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Observation with NormalizedHash on the observation but no NormalizedState.
	observer := &fakeDriftObserver{
		obs: &domain.RuntimeObservation{
			ID:             uuid.New(),
			HealthStatus:   domain.HealthStatusHealthy,
			Source:         "test",
			NormalizedHash: spec.DesiredHash,
		},
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("Status = %q, want %q (should fall back to NormalizedHash)", state.Status, domain.DriftStatusInSync)
	}
}

func TestCompareDrift_SecretRedaction(t *testing.T) {
	t.Parallel()

	// Build a spec with secret refs to verify secrets don't affect hash comparison.
	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EnvironmentID:    uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ArtifactID:       uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		StableServiceKey: "test-svc",
		ImageRef:         "postgres:16",
		SecretRefs: []domain.DesiredSecretRef{
			{
				EnvVar:        "DB_PASSWORD",
				Name:          "db-password",
				SecretID:      uuid.MustParse("00000000-0000-0000-0000-000000000099"),
				RedactedValue: domain.RedactedPlaceholder("db-password"),
			},
		},
	}
	spec.ComputeDesiredHash()

	// Verify the redacted value is correct.
	if spec.SecretRefs[0].RedactedValue != "REDACTED(db-password)" {
		t.Errorf("RedactedValue = %q, want REDACTED(db-password)", spec.SecretRefs[0].RedactedValue)
	}

	// Verify the spec does not contain plaintext.
	if spec.ContainsPlaintextSecret() {
		t.Error("spec should not contain plaintext secrets")
	}

	// Hash should only use secret key presence, not values.
	// Build a second spec with same secret key but different redacted placeholder — hash should match.
	spec2 := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		EnvironmentID:    uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		ArtifactID:       uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		StableServiceKey: "test-svc",
		ImageRef:         "postgres:16",
		SecretRefs: []domain.DesiredSecretRef{
			{
				EnvVar:        "DB_PASSWORD",
				Name:          "db-password-different-name",
				SecretID:      uuid.MustParse("00000000-0000-0000-0000-000000000088"),
				RedactedValue: "REDACTED(different)",
			},
		},
	}
	spec2.ComputeDesiredHash()

	// Hashes should match because ComputeDesiredHash only uses env var names.
	if spec.DesiredHash != spec2.DesiredHash {
		t.Errorf("hashes should match when secret env var names are the same: %q vs %q", spec.DesiredHash, spec2.DesiredHash)
	}

	// Now compare drift with matching observation.
	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusHealthy),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("Status = %q, want %q", state.Status, domain.DriftStatusInSync)
	}
}

func TestCompareDrift_UnknownHealth_MatchingHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Unknown health with matching hash — should drift because health isn't acceptable.
	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusUnknown),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("Status = %q, want %q (unknown health with matching hash should drift)", state.Status, domain.DriftStatusDrifted)
	}
}

func TestIsAcceptableHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		health   domain.HealthStatus
		expected bool
	}{
		{domain.HealthStatusHealthy, true},
		{domain.HealthStatusStarting, true},
		{domain.HealthStatusUnhealthy, false},
		{domain.HealthStatusStopped, false},
		{domain.HealthStatusUnknown, false},
		{"", false},
	}

	for _, tc := range tests {
		if got := isAcceptableHealth(tc.health); got != tc.expected {
			t.Errorf("isAcceptableHealth(%q) = %v, want %v", tc.health, got, tc.expected)
		}
	}
}
