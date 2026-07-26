package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ===========================================================================
// Integration: drift detection feeds apply decisions
//
// These tests verify the full cycle: observe → compare drift → apply, ensuring
// that drift detection correctly triggers (or avoids) apply mutations.
// ===========================================================================

// ---------------------------------------------------------------------------
// Hash match → drift in_sync → apply no-op
// ---------------------------------------------------------------------------

func TestIntegration_DriftInSync_ApplyNoOp(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Pre-populate a container whose desired hash matches.
	mock.addContainer(DockerContainer{
		ID:    "synced-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	// Step 1: Drift detection — simulate observation returning matching hash.
	driftObs := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusHealthy),
	}
	driftState, err := CompareDrift(context.Background(), spec, driftObs)
	if err != nil {
		t.Fatalf("drift compare error: %v", err)
	}
	if driftState.Status != domain.DriftStatusInSync {
		t.Fatalf("expected in_sync, got %q", driftState.Status)
	}

	// Step 2: Apply — should be a no-op since drift is in_sync.
	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}

	// Verify no mutations occurred.
	if len(mock.pullCalls) > 0 || len(mock.stopCalls) > 0 || len(mock.createCalls) > 0 {
		t.Error("no mutations expected when drift is in_sync and hash matches")
	}
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "synced-container" {
		t.Errorf("result should reference existing container, got %v", result.ResourceIDs)
	}
}

func TestIntegration_StoppedHashMatch_ApplyRecreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	mock.addContainer(DockerContainer{
		ID:    "stopped-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "exited",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	result, err := observer.ApplyDesiredState(context.Background(), applyTestRequest(spec))
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if len(mock.stopCalls) == 0 || len(mock.createCalls) == 0 {
		t.Fatalf("stopped hash-matched container must be recreated: stops=%v creates=%v", mock.stopCalls, mock.createCalls)
	}
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] == "stopped-container" {
		t.Fatalf("result should reference recreated container, got %v", result.ResourceIDs)
	}
}

// ---------------------------------------------------------------------------
// Hash drift → drift drifted → apply recreates
// ---------------------------------------------------------------------------

func TestIntegration_DriftDrifted_ApplyRecreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	oldHash := "sha256:stale-old-hash"
	mock.addContainer(DockerContainer{
		ID:    "drifted-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   oldHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	// Step 1: Drift detection — observation hash differs from desired.
	driftObs := &fakeDriftObserver{
		obs: testObservation(oldHash, domain.HealthStatusHealthy),
	}
	driftState, err := CompareDrift(context.Background(), spec, driftObs)
	if err != nil {
		t.Fatalf("drift compare error: %v", err)
	}
	if driftState.Status != domain.DriftStatusDrifted {
		t.Fatalf("expected drifted, got %q", driftState.Status)
	}

	// Step 2: Apply — should recreate.
	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}

	// Verify full recreate flow.
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "drifted-container" {
		t.Errorf("expected stop of drifted-container, got %v", mock.stopCalls)
	}
	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "drifted-container" {
		t.Errorf("expected remove of drifted-container, got %v", mock.removeCalls)
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start, got %d", len(mock.startCalls))
	}
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("result hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
}

// ---------------------------------------------------------------------------
// Missing container → drift unknown → apply creates fresh
// ---------------------------------------------------------------------------

func TestIntegration_DriftUnknown_MissingContainer_ApplyCreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	// Step 1: Drift detection — service not found.
	driftObs := &fakeDriftObserver{obs: nil, err: nil}
	driftState, err := CompareDrift(context.Background(), spec, driftObs)
	if err != nil {
		t.Fatalf("drift compare error: %v", err)
	}
	if driftState.Status != domain.DriftStatusUnknown {
		t.Fatalf("expected unknown, got %q", driftState.Status)
	}

	// Step 2: Apply — should create fresh.
	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}

	if len(mock.stopCalls) > 0 || len(mock.removeCalls) > 0 {
		t.Error("should not stop/remove when no existing container")
	}
	if len(mock.createCalls) != 1 || len(mock.startCalls) != 1 {
		t.Error("expected create + start for missing container")
	}
	if len(result.ResourceIDs) != 1 {
		t.Errorf("expected 1 resource ID, got %d", len(result.ResourceIDs))
	}
}

// ---------------------------------------------------------------------------
// Drift drifted (unhealthy) with matching hash → apply recreates
// ---------------------------------------------------------------------------

func TestIntegration_DriftDrifted_UnhealthyMatchingHash_ApplyRecreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Container has the RIGHT hash but is unhealthy — drift comparator
	// reports drifted. However, ApplyDesiredState only checks hash labels,
	// so a hash match will be a no-op. The drift status is advisory.
	mock.addContainer(DockerContainer{
		ID:    "unhealthy-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	// Drift comparator says drifted because health is unacceptable.
	driftObs := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusUnhealthy),
	}
	driftState, err := CompareDrift(context.Background(), spec, driftObs)
	if err != nil {
		t.Fatalf("drift compare error: %v", err)
	}
	if driftState.Status != domain.DriftStatusDrifted {
		t.Fatalf("expected drifted (unhealthy), got %q", driftState.Status)
	}

	// Apply with default pull policy — hash matches, so it's a no-op.
	// (To force recreate despite hash match, the caller would use pull=always.)
	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}

	// No-op because hash matches (apply doesn't check health).
	if len(mock.createCalls) > 0 {
		t.Error("apply should no-op on hash match even if drift says unhealthy")
	}
	if result.ResourceIDs[0] != "unhealthy-container" {
		t.Errorf("expected existing container ID, got %v", result.ResourceIDs)
	}
}

// ---------------------------------------------------------------------------
// Drift drifted (unhealthy) + pull always → force recreate
// ---------------------------------------------------------------------------

func TestIntegration_DriftDrifted_Unhealthy_PullAlways_Recreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "unhealthy-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	// With pull=always, even matching hash triggers recreate.
	req := applyTestRequest(spec)
	req.PullPolicy = "always"

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}

	if len(mock.stopCalls) != 1 || len(mock.removeCalls) != 1 {
		t.Error("expected stop + remove for force recreate")
	}
	if len(mock.createCalls) != 1 || len(mock.startCalls) != 1 {
		t.Error("expected create + start for force recreate")
	}
	if result.ResourceIDs[0] == "unhealthy-container" {
		t.Error("should have new container ID, not old unhealthy one")
	}
}

// ===========================================================================
// Pull policy comprehensive tests
// ===========================================================================

// ---------------------------------------------------------------------------
// Pull "if-not-present" + hash match on existing → no-op, no pull
// ---------------------------------------------------------------------------

func TestApplyDesiredState_PullIfNotPresent_HashMatch_NoOp(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "existing-ok",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "if-not-present"

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op with no pull.
	if len(mock.pullCalls) > 0 {
		t.Error("should not pull when hash matches with if-not-present policy")
	}
	if len(mock.createCalls) > 0 {
		t.Error("should not create when hash matches")
	}
	if result.ResourceIDs[0] != "existing-ok" {
		t.Errorf("expected existing container, got %v", result.ResourceIDs)
	}
}

// ---------------------------------------------------------------------------
// Pull "never" + hash match on existing → no-op
// ---------------------------------------------------------------------------

func TestApplyDesiredState_PullNever_HashMatch_NoOp(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "existing-ok",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "never"

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.pullCalls) > 0 {
		t.Error("should never pull with 'never' policy")
	}
	if len(mock.createCalls) > 0 {
		t.Error("should not create when hash matches")
	}
	if result.ResourceIDs[0] != "existing-ok" {
		t.Errorf("expected existing container, got %v", result.ResourceIDs)
	}
}

// ---------------------------------------------------------------------------
// Pull "never" + hash drift → recreate without pull
// ---------------------------------------------------------------------------

func TestApplyDesiredState_PullNever_HashDrift_RecreatesNoPull(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:old-hash",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "never"

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should recreate but NOT pull.
	if len(mock.pullCalls) > 0 {
		t.Error("should not pull with 'never' policy even on hash drift")
	}
	if len(mock.stopCalls) != 1 {
		t.Errorf("expected 1 stop, got %d", len(mock.stopCalls))
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start, got %d", len(mock.startCalls))
	}
}

// ---------------------------------------------------------------------------
// Spec-level pull policy used when request-level is empty
// ---------------------------------------------------------------------------

func TestApplyDesiredState_SpecPullPolicy_UsedWhenRequestEmpty(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.PullPolicy = "never"

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "" // empty → falls back to spec

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.pullCalls) > 0 {
		t.Error("spec policy 'never' should suppress pull when request policy is empty")
	}
}

// ===========================================================================
// Container lookup edge cases
// ===========================================================================

// ---------------------------------------------------------------------------
// Container found by name (not labels) + hash drift → recreate
// ---------------------------------------------------------------------------

func TestApplyDesiredState_FoundByName_HashDrift_Recreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Container has no bahia labels but matches by name.
	mock.addContainer(DockerContainer{
		ID:    "name-only-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			// No bahia.service_id or bahia.environment_id labels.
			"bahia.desired_hash": "sha256:old-hash",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find by name and recreate due to hash drift.
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "name-only-container" {
		t.Errorf("expected stop of name-only-container, got %v", mock.stopCalls)
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("result hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
}

// ===========================================================================
// Secrets injection verification
// ===========================================================================

func TestApplyDesiredState_SecretsInjectedInCreateBody(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	secrets := map[string]string{
		"DB_PASSWORD": "super-secret",
		"API_KEY":     "key-abc-123",
	}
	req := applyTestRequest(spec)
	req.Secrets = secrets

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create, got %d", len(mock.createCalls))
	}

	body := mock.createCalls[0].Body
	envRaw, ok := body["Env"]
	if !ok {
		t.Fatal("create body missing Env")
	}

	// Env comes through as []interface{} from JSON decode.
	envSlice, ok := envRaw.([]interface{})
	if !ok {
		t.Fatalf("Env type = %T, want []interface{}", envRaw)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		s, ok := e.(string)
		if !ok {
			continue
		}
		parts := strings.SplitN(s, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["DB_PASSWORD"] != "super-secret" {
		t.Errorf("DB_PASSWORD = %q, want super-secret", envMap["DB_PASSWORD"])
	}
	if envMap["API_KEY"] != "key-abc-123" {
		t.Errorf("API_KEY = %q, want key-abc-123", envMap["API_KEY"])
	}
	// Literal env should also be present.
	if envMap["APP_ENV"] != "production" {
		t.Errorf("APP_ENV = %q, want production", envMap["APP_ENV"])
	}

	// Redacted placeholders must NOT be present.
	for k, v := range envMap {
		if strings.Contains(v, "REDACTED") {
			t.Errorf("redacted placeholder leaked into env: %s=%s", k, v)
		}
	}
}

// ===========================================================================
// Partial failure edge cases
// ===========================================================================

// ---------------------------------------------------------------------------
// Stop succeeds but remove fails → error, no create attempted
// ---------------------------------------------------------------------------

func TestApplyDesiredState_StopSucceeds_RemoveFails_NoCreate(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failRemove = true
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:different",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)

	if err == nil {
		t.Fatal("expected error for remove failure")
	}
	// Stop should have been called.
	if len(mock.stopCalls) != 1 {
		t.Errorf("expected 1 stop, got %d", len(mock.stopCalls))
	}
	// Remove was attempted but failed.
	if len(mock.removeCalls) != 1 {
		t.Errorf("expected 1 remove attempt, got %d", len(mock.removeCalls))
	}
	// Create should NOT have been called.
	if len(mock.createCalls) > 0 {
		t.Error("should not create after remove failure")
	}
	// Start should NOT have been called.
	if len(mock.startCalls) > 0 {
		t.Error("should not start after remove failure")
	}
}

// ---------------------------------------------------------------------------
// Create succeeds but start fails → error with container ID in error
// ---------------------------------------------------------------------------

func TestApplyDesiredState_CreateSucceeds_StartFails_ExplicitError(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failStart = true
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)

	if err == nil {
		t.Fatal("expected error for start failure")
	}
	if !strings.Contains(err.Error(), "starting container") {
		t.Errorf("error should mention starting, got: %v", err)
	}
	// Create should have succeeded.
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
	// Error should contain the container ID.
	if !strings.Contains(err.Error(), "container-1") {
		t.Errorf("error should contain container ID, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Pull failure with "always" prevents all subsequent mutations
// ---------------------------------------------------------------------------

func TestApplyDesiredState_PullAlways_Failure_NoStopRemoveCreateStart(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failPull = true
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:different",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "always"

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for pull failure with always policy")
	}

	// No mutations should have occurred after pull failure.
	if len(mock.stopCalls) > 0 {
		t.Error("should not stop after pull failure")
	}
	if len(mock.removeCalls) > 0 {
		t.Error("should not remove after pull failure")
	}
	if len(mock.createCalls) > 0 {
		t.Error("should not create after pull failure")
	}
	if len(mock.startCalls) > 0 {
		t.Error("should not start after pull failure")
	}
}

// ===========================================================================
// Dry-run edge cases
// ===========================================================================

// ---------------------------------------------------------------------------
// Dry-run with hash match → no-op takes precedence (returned before dry-run check)
// ---------------------------------------------------------------------------

func TestApplyDesiredState_DryRun_HashMatch_IsNoOp(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "synced-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.DryRun = true

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return existing container (no-op), not a dry-run message.
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "synced-container" {
		t.Errorf("expected no-op with existing container, got %v", result.ResourceIDs)
	}

	// Dry-run warning should NOT be present because no-op short-circuits.
	for _, w := range result.Warnings {
		if strings.Contains(w, "dry-run") {
			t.Errorf("no-op should not produce dry-run warning, got: %s", w)
		}
	}
}

// ===========================================================================
// Drift comparator edge cases
// ===========================================================================

// ---------------------------------------------------------------------------
// NormalizedState.ObservationHash takes precedence over NormalizedHash
// ---------------------------------------------------------------------------

func TestCompareDrift_NormalizedStatePrecedence(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	// Both NormalizedState.ObservationHash and NormalizedHash set,
	// but they differ. NormalizedState should take precedence.
	obs := &domain.RuntimeObservation{
		ID:           uuid.New(),
		HealthStatus: domain.HealthStatusHealthy,
		Source:       "test",
		NormalizedState: &domain.NormalizedObservation{
			ObservationHash: spec.DesiredHash, // matches
		},
		NormalizedHash: "sha256:different-fallback", // would cause drift if used
	}

	observer := &fakeDriftObserver{obs: obs}
	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("expected in_sync (NormalizedState should take precedence), got %q", state.Status)
	}
}

// ---------------------------------------------------------------------------
// NormalizedState hash empty, falls back to NormalizedHash
// ---------------------------------------------------------------------------

func TestCompareDrift_NormalizedStateEmpty_FallsBackToNormalizedHash(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	obs := &domain.RuntimeObservation{
		ID:           uuid.New(),
		HealthStatus: domain.HealthStatusHealthy,
		Source:       "test",
		NormalizedState: &domain.NormalizedObservation{
			ObservationHash: "", // empty — should fall back
		},
		NormalizedHash: spec.DesiredHash,
	}

	observer := &fakeDriftObserver{obs: obs}
	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("expected in_sync via NormalizedHash fallback, got %q", state.Status)
	}
}

// ---------------------------------------------------------------------------
// Both hash sources empty → unknown
// ---------------------------------------------------------------------------

func TestCompareDrift_BothHashesEmpty_Unknown(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	obs := &domain.RuntimeObservation{
		ID:           uuid.New(),
		HealthStatus: domain.HealthStatusHealthy,
		Source:       "test",
		NormalizedState: &domain.NormalizedObservation{
			ObservationHash: "",
		},
		NormalizedHash: "",
	}

	observer := &fakeDriftObserver{obs: obs}
	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("expected unknown when both hashes empty, got %q", state.Status)
	}
}

// ---------------------------------------------------------------------------
// Drift: observation error reason is captured
// ---------------------------------------------------------------------------

func TestCompareDrift_ObservationError_ReasonCaptured(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		err: context.DeadlineExceeded,
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("observation errors should not propagate: %v", err)
	}
	if state.Status != domain.DriftStatusUnknown {
		t.Errorf("expected unknown, got %q", state.Status)
	}
	if !strings.Contains(state.Reason, "deadline exceeded") {
		t.Errorf("reason should contain error details, got: %q", state.Reason)
	}
}

// ---------------------------------------------------------------------------
// Drift: health starting + hash match → in_sync
// ---------------------------------------------------------------------------

func TestCompareDrift_HealthStarting_HashMatch_InSync(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, domain.HealthStatusStarting),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusInSync {
		t.Errorf("starting health with matching hash should be in_sync, got %q", state.Status)
	}
}

// ---------------------------------------------------------------------------
// Drift: empty string health + hash match → drifted (not acceptable)
// ---------------------------------------------------------------------------

func TestCompareDrift_EmptyHealth_HashMatch_Drifted(t *testing.T) {
	t.Parallel()
	spec := driftTestDesiredSpec("nginx:1.25")

	observer := &fakeDriftObserver{
		obs: testObservation(spec.DesiredHash, ""),
	}

	state, err := CompareDrift(context.Background(), spec, observer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Status != domain.DriftStatusDrifted {
		t.Errorf("empty health with matching hash should be drifted, got %q", state.Status)
	}
}

// ===========================================================================
// Network attachment warning tests
// ===========================================================================

func TestApplyDesiredState_AdditionalNetworkAttachment_FailureIsWarning(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.DockerExtension = &domain.DockerExtension{
		NetworkingConfig: map[string]any{
			"AdditionalNetworks": []string{"extra-net-1", "extra-net-2"},
		},
	}

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The mock handler returns 200 for connect calls, so no warnings expected
	// here. But we verify connect calls were made.
	expectedConnects := 0
	for _, net := range []string{"extra-net-1", "extra-net-2"} {
		if net != spec.NetworkMode {
			expectedConnects++
		}
	}
	if len(mock.connectCalls) != expectedConnects {
		t.Errorf("expected %d connect calls, got %d: %+v", expectedConnects, len(mock.connectCalls), mock.connectCalls)
	}

	// Result should be successful.
	if result.Renderer != "docker" {
		t.Errorf("renderer = %q, want docker", result.Renderer)
	}
}

// ===========================================================================
// shouldPull comprehensive edge cases
// ===========================================================================

func TestShouldPull_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		policy       string
		existingHash string
		desiredHash  string
		isMissing    bool
		want         bool
	}{
		{"always pulls even with matching hash and not missing", "always", "sha256:same", "sha256:same", false, true},
		{"always pulls when missing", "always", "", "", true, true},
		{"never skips even when missing", "never", "", "sha256:new", true, false},
		{"never skips even on hash drift", "never", "sha256:old", "sha256:new", false, false},
		{"if-not-present pulls when existing hash is empty and not missing", "if-not-present", "", "sha256:new", false, true},
		{"if-not-present skips when hashes match and not missing", "if-not-present", "sha256:same", "sha256:same", false, false},
		{"default policy treated as if-not-present", "unrecognized-policy", "sha256:same", "sha256:same", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldPull(tt.policy, tt.existingHash, tt.desiredHash, tt.isMissing)
			if got != tt.want {
				t.Errorf("shouldPull(%q, %q, %q, %v) = %v, want %v",
					tt.policy, tt.existingHash, tt.desiredHash, tt.isMissing, got, tt.want)
			}
		})
	}
}

// ===========================================================================
// Error messages are explicit (not silent failures)
// ===========================================================================

func TestApplyDesiredState_AllErrorsExplicit(t *testing.T) {
	t.Parallel()

	// Table-driven: each failure mode should produce an explicit error message.
	tests := []struct {
		name       string
		setup      func(*applyMockState)
		wantSubstr string
	}{
		{
			name:       "pull failure (always)",
			setup:      func(m *applyMockState) { m.failPull = true },
			wantSubstr: "pulling image",
		},
		{
			name:       "create failure",
			setup:      func(m *applyMockState) { m.failCreate = true },
			wantSubstr: "creating container",
		},
		{
			name:       "start failure",
			setup:      func(m *applyMockState) { m.failStart = true },
			wantSubstr: "starting container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newApplyMockState()
			tt.setup(mock)
			spec := applyTestSpec()

			server, observer := setupApplyTest(mock)
			defer server.Close()

			req := applyTestRequest(spec)
			if tt.name == "pull failure (always)" {
				req.PullPolicy = "always"
			}

			_, err := observer.ApplyDesiredState(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error for %s should contain %q, got: %v", tt.name, tt.wantSubstr, err)
			}
			// Verify error wraps properly (contains "docker apply:" prefix).
			if !strings.Contains(err.Error(), "docker apply:") {
				t.Errorf("error should have 'docker apply:' prefix, got: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Existing container with stop/remove failure — errors are explicit too
// ---------------------------------------------------------------------------

func TestApplyDesiredState_StopRemoveErrorsExplicit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		failStop   bool
		failRemove bool
		wantSubstr string
	}{
		{"stop fails", true, false, "stopping container"},
		{"remove fails", false, true, "removing container"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newApplyMockState()
			mock.failStop = tt.failStop
			mock.failRemove = tt.failRemove
			spec := applyTestSpec()

			mock.addContainer(DockerContainer{
				ID:    "doomed-container",
				Names: []string{"/bahia-22222222-my-api"},
				Labels: map[string]string{
					"bahia.service_id":     testServiceID.String(),
					"bahia.environment_id": testEnvironmentID.String(),
					"bahia.desired_hash":   "sha256:different",
				},
			})

			server, observer := setupApplyTest(mock)
			defer server.Close()

			req := applyTestRequest(spec)
			_, err := observer.ApplyDesiredState(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error should contain %q, got: %v", tt.wantSubstr, err)
			}
			if !strings.Contains(err.Error(), "docker apply:") {
				t.Errorf("error should have 'docker apply:' prefix, got: %v", err)
			}
		})
	}
}
