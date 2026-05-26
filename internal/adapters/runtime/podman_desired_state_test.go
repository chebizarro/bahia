package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ===========================================================================
// Capability detection
// ===========================================================================

func TestPodmanObserver_SupportsDesiredState(t *testing.T) {
	t.Parallel()
	observer := NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())
	if !observer.SupportsDesiredState() {
		t.Error("PodmanObserver should support desired state")
	}
}

func TestPodmanObserver_Type(t *testing.T) {
	t.Parallel()
	observer := NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())
	if observer.Type() != domain.RuntimeTypePodman {
		t.Errorf("Type() = %q, want %q", observer.Type(), domain.RuntimeTypePodman)
	}
}

func TestAsDesiredStateApplier_Podman(t *testing.T) {
	t.Parallel()
	observer := NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())
	applier, ok := AsDesiredStateApplier(observer)
	if !ok {
		t.Fatal("PodmanObserver should be recognized as DesiredStateApplier")
	}
	if applier == nil {
		t.Fatal("applier should not be nil")
	}
}

// ===========================================================================
// Delegation to Docker — successful apply
// ===========================================================================

func TestPodmanApplyDesiredState_DelegatesToDocker(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Renderer should be relabeled as "podman".
	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}

	// Should have called through to Docker's implementation.
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create call, got %d", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start call, got %d", len(mock.startCalls))
	}

	// Desired hash should be propagated.
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("desired_hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
}

// ===========================================================================
// No-op hash match through Podman
// ===========================================================================

func TestPodmanApplyDesiredState_NoOp_HashMatch(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Pre-populate a container with matching desired hash.
	mock.addContainer(DockerContainer{
		ID:    "podman-existing-123",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "podman-existing-123" {
		t.Errorf("resource_ids = %v, want [podman-existing-123]", result.ResourceIDs)
	}

	// No mutations.
	if len(mock.pullCalls) > 0 || len(mock.createCalls) > 0 || len(mock.startCalls) > 0 {
		t.Error("expected no-op, but mutations occurred")
	}
}

// ===========================================================================
// Hash drift recreate through Podman
// ===========================================================================

func TestPodmanApplyDesiredState_HashDrift_Recreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "podman-old-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:old-podman-hash",
		},
	})

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
	if len(mock.stopCalls) != 1 {
		t.Errorf("expected 1 stop call, got %d", len(mock.stopCalls))
	}
	if len(mock.removeCalls) != 1 {
		t.Errorf("expected 1 remove call, got %d", len(mock.removeCalls))
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create call, got %d", len(mock.createCalls))
	}
}

// ===========================================================================
// Dry run through Podman
// ===========================================================================

func TestPodmanApplyDesiredState_DryRun(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)
	req.DryRun = true

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
	if len(mock.createCalls) > 0 || len(mock.startCalls) > 0 {
		t.Error("dry run should not mutate anything")
	}
}

// ===========================================================================
// Nil spec error
// ===========================================================================

func TestPodmanApplyDesiredState_NilSpec_Error(t *testing.T) {
	t.Parallel()
	observer := NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())
	_, err := observer.ApplyDesiredState(context.Background(), DesiredStateApplyRequest{})
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
	if !contains(err.Error(), "nil") {
		t.Errorf("error should mention nil, got: %v", err)
	}
}

// ===========================================================================
// Compatibility validation — rejected configurations
// ===========================================================================

func TestPodmanApplyDesiredState_RejectsComposeExtension(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.ComposeExtension = &domain.ComposeExtension{
		ProjectName: "test-project",
	}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	_, err := podman.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for Compose extension on Podman")
	}
	if !errors.Is(err, ErrPodmanIncompatible) {
		t.Errorf("expected ErrPodmanIncompatible, got: %v", err)
	}
	if !contains(err.Error(), "compose_extension") {
		t.Errorf("error should mention compose_extension, got: %v", err)
	}
	if !contains(err.Error(), "Compose runtime target") {
		t.Errorf("error should give actionable guidance, got: %v", err)
	}

	// No Docker API calls should have been made.
	if len(mock.createCalls) > 0 {
		t.Error("should not call Docker API when compatibility check fails")
	}
}

func TestPodmanApplyDesiredState_RejectsKubernetesExtension(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.KubernetesExtension = &domain.KubernetesExtension{}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	_, err := podman.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for Kubernetes extension on Podman")
	}
	if !errors.Is(err, ErrPodmanIncompatible) {
		t.Errorf("expected ErrPodmanIncompatible, got: %v", err)
	}
	if !contains(err.Error(), "kubernetes_extension") {
		t.Errorf("error should mention kubernetes_extension, got: %v", err)
	}
}

func TestPodmanApplyDesiredState_RejectsOverlayNetworkDriver(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.DockerExtension = &domain.DockerExtension{
		NetworkingConfig: map[string]any{
			"Driver": "overlay",
		},
	}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	_, err := podman.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for overlay network on Podman")
	}
	if !errors.Is(err, ErrPodmanIncompatible) {
		t.Errorf("expected ErrPodmanIncompatible, got: %v", err)
	}
	if !contains(err.Error(), "overlay") {
		t.Errorf("error should mention overlay, got: %v", err)
	}
}

func TestPodmanApplyDesiredState_RejectsUnsupportedHostConfig(t *testing.T) {
	t.Parallel()

	for _, field := range podmanUnsupportedHostConfig {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			mock := newApplyMockState()
			spec := applyTestSpec()
			spec.DockerExtension = &domain.DockerExtension{
				HostConfig: map[string]any{
					field: "some-value",
				},
			}

			server, dockerObserver := setupApplyTest(mock)
			defer server.Close()

			podman := &PodmanObserver{DockerObserver: dockerObserver}
			req := applyTestRequest(spec)

			_, err := podman.ApplyDesiredState(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error for unsupported host config field %q", field)
			}
			if !errors.Is(err, ErrPodmanIncompatible) {
				t.Errorf("expected ErrPodmanIncompatible, got: %v", err)
			}
			if !contains(err.Error(), field) {
				t.Errorf("error should mention %q, got: %v", field, err)
			}
		})
	}
}

// ===========================================================================
// Compatibility validation — accepted configurations
// ===========================================================================

func TestPodmanApplyDesiredState_AcceptsDockerExtension_SupportedFields(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.DockerExtension = &domain.DockerExtension{
		HostConfig: map[string]any{
			"Memory": int64(536870912), // 512MB — supported by Podman
		},
	}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error for supported Docker extension: %v", err)
	}
	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
}

func TestPodmanApplyDesiredState_AcceptsPodmanExtension(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.PodmanExtension = &domain.PodmanExtension{}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error for PodmanExtension: %v", err)
	}
	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
}

func TestPodmanApplyDesiredState_AcceptsBridgeNetwork(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.DockerExtension = &domain.DockerExtension{
		NetworkingConfig: map[string]any{
			"Driver": "bridge",
		},
	}

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error for bridge network: %v", err)
	}
	if result.Renderer != "podman" {
		t.Errorf("renderer = %q, want podman", result.Renderer)
	}
}

// ===========================================================================
// validatePodmanCompatibility unit tests
// ===========================================================================

func TestValidatePodmanCompatibility_NilSpec(t *testing.T) {
	t.Parallel()
	if err := validatePodmanCompatibility(nil); err != nil {
		t.Errorf("nil spec should be compatible, got: %v", err)
	}
}

func TestValidatePodmanCompatibility_CleanSpec(t *testing.T) {
	t.Parallel()
	spec := applyTestSpec()
	if err := validatePodmanCompatibility(spec); err != nil {
		t.Errorf("clean spec should be compatible, got: %v", err)
	}
}

func TestValidatePodmanCompatibility_OverlayCaseInsensitive(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		DockerExtension: &domain.DockerExtension{
			NetworkingConfig: map[string]any{
				"Driver": "OVERLAY",
			},
		},
	}
	err := validatePodmanCompatibility(spec)
	if err == nil {
		t.Fatal("should reject OVERLAY (case-insensitive)")
	}
	if !errors.Is(err, ErrPodmanIncompatible) {
		t.Errorf("expected ErrPodmanIncompatible, got: %v", err)
	}
}

// ===========================================================================
// Environment revision propagation through Podman
// ===========================================================================

func TestPodmanApplyDesiredState_PropagatesEnvironmentRevision(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, dockerObserver := setupApplyTest(mock)
	defer server.Close()

	podman := &PodmanObserver{DockerObserver: dockerObserver}
	req := applyTestRequest(spec)

	result, err := podman.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EnvironmentRevision == "" {
		t.Error("expected non-empty environment revision")
	}
	if result.EnvironmentRevision != req.EnvironmentPlan.RevisionHash {
		t.Errorf("revision = %q, want %q", result.EnvironmentRevision, req.EnvironmentPlan.RevisionHash)
	}
}

// Helper: reuses contains() from factory_test.go (same package).
