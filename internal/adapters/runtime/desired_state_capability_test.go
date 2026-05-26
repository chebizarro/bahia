package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Interface compilation tests
// ---------------------------------------------------------------------------

// TestDesiredStateApplierInterfaceCompiles verifies that the DesiredStateApplier
// interface and all concrete adapter stubs compile correctly.
func TestDesiredStateApplierInterfaceCompiles(t *testing.T) {
	// The compile-time var _ assertions in the main file handle this,
	// but we also verify via explicit type assertion at runtime.
	var appliers []DesiredStateApplier

	docker := NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())
	appliers = append(appliers, docker)

	compose := NewComposeRuntime("/tmp/test", zap.NewNop())
	appliers = append(appliers, compose)

	k8s := NewKubernetesRuntime("", "default", "", zap.NewNop())
	appliers = append(appliers, k8s)

	podman := NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())
	appliers = append(appliers, podman)

	if len(appliers) != 4 {
		t.Fatalf("expected 4 appliers, got %d", len(appliers))
	}
}

// ---------------------------------------------------------------------------
// Request / Result construction tests
// ---------------------------------------------------------------------------

func TestDesiredStateApplyRequestConstruction(t *testing.T) {
	envID := uuid.New()
	svcID := uuid.New()
	artID := uuid.New()

	spec := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        svcID,
		EnvironmentID:    envID,
		ArtifactID:       artID,
		StableServiceKey: "my-service",
		ImageRef:         "registry.example.com/app:v1.2.3",
		Env:              map[string]string{"APP_ENV": "production"},
		Ports:            []string{"8080:80/tcp"},
		RestartPolicy:    "unless-stopped",
	}
	spec.ComputeDesiredHash()

	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: envID,
		Services:      []domain.DesiredServiceSpec{*spec},
	}
	plan.ComputeRevisionHash()

	req := DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   spec,
		Secrets:         map[string]string{"DB_PASSWORD": "s3cret"},
		PullPolicy:      "always",
		DryRun:          true,
	}

	if req.EnvironmentPlan.EnvironmentID != envID {
		t.Errorf("expected environment ID %s, got %s", envID, req.EnvironmentPlan.EnvironmentID)
	}
	if req.TargetService.StableServiceKey != "my-service" {
		t.Errorf("expected service key 'my-service', got %q", req.TargetService.StableServiceKey)
	}
	if req.Secrets["DB_PASSWORD"] != "s3cret" {
		t.Error("expected secret to be present in request")
	}
	if req.PullPolicy != "always" {
		t.Errorf("expected pull policy 'always', got %q", req.PullPolicy)
	}
	if !req.DryRun {
		t.Error("expected DryRun to be true")
	}
	if req.TargetService.DesiredHash == "" {
		t.Error("expected DesiredHash to be computed")
	}
}

func TestDesiredStateApplyResultConstruction(t *testing.T) {
	result := &DesiredStateApplyResult{
		Renderer:            "compose",
		DesiredHash:         "sha256:abc123",
		EnvironmentRevision: "sha256:def456",
		ResourceIDs:         []string{"container-abc123"},
		ResourceNames:       []string{"my-service"},
		ObservationHints: &ObservationHints{
			ContainerID: "container-abc123",
			NetworkIDs:  []string{"net-1", "net-2"},
			VolumeNames: []string{"data-vol"},
		},
		Warnings: []string{"image pull fell back to local cache"},
	}

	if result.Renderer != "compose" {
		t.Errorf("expected renderer 'compose', got %q", result.Renderer)
	}
	if result.DesiredHash != "sha256:abc123" {
		t.Errorf("unexpected desired hash: %s", result.DesiredHash)
	}
	if result.EnvironmentRevision != "sha256:def456" {
		t.Errorf("unexpected environment revision: %s", result.EnvironmentRevision)
	}
	if len(result.ResourceIDs) != 1 {
		t.Fatalf("expected 1 resource ID, got %d", len(result.ResourceIDs))
	}
	if len(result.ResourceNames) != 1 {
		t.Fatalf("expected 1 resource name, got %d", len(result.ResourceNames))
	}
	if result.ObservationHints == nil {
		t.Fatal("expected observation hints to be non-nil")
	}
	if result.ObservationHints.ContainerID != "container-abc123" {
		t.Errorf("unexpected container ID hint: %s", result.ObservationHints.ContainerID)
	}
	if len(result.ObservationHints.NetworkIDs) != 2 {
		t.Errorf("expected 2 network IDs, got %d", len(result.ObservationHints.NetworkIDs))
	}
	if len(result.ObservationHints.VolumeNames) != 1 {
		t.Errorf("expected 1 volume name, got %d", len(result.ObservationHints.VolumeNames))
	}
	if len(result.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(result.Warnings))
	}
}

// ---------------------------------------------------------------------------
// Unsupported runtime error tests
// ---------------------------------------------------------------------------

func TestUnsupportedRuntimesReturnExplicitError(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		applier DesiredStateApplier
	}{
		{"docker", NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())},
		{"compose", NewComposeRuntime("/tmp/test", zap.NewNop())},
		{"kubernetes", NewKubernetesRuntime("", "default", "", zap.NewNop())},
		{"podman", NewPodmanObserver("unix:///run/podman/podman.sock", zap.NewNop())},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.applier.SupportsDesiredState() {
				t.Errorf("%s: expected SupportsDesiredState() = false", tc.name)
			}

			result, err := tc.applier.ApplyDesiredState(ctx, DesiredStateApplyRequest{})
			if result != nil {
				t.Errorf("%s: expected nil result for unsupported runtime", tc.name)
			}
			if !errors.Is(err, ErrDesiredStateNotSupported) {
				t.Errorf("%s: expected ErrDesiredStateNotSupported, got %v", tc.name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Capability probe helper test
// ---------------------------------------------------------------------------

func TestAsDesiredStateApplier(t *testing.T) {
	docker := NewDockerObserver("unix:///var/run/docker.sock", zap.NewNop())

	// Docker implements DesiredStateApplier but SupportsDesiredState() = false,
	// so the helper should return (applier, false).
	applier, supported := AsDesiredStateApplier(docker)
	if applier == nil {
		t.Fatal("expected non-nil applier from AsDesiredStateApplier")
	}
	if supported {
		t.Error("expected supported = false for docker stub")
	}
}

func TestAsDesiredStateApplierWithNonImplementor(t *testing.T) {
	// A type that implements Runtime but NOT DesiredStateApplier should
	// return (nil, false). We use a mock here.
	var rt Runtime = &mockRuntimeNoDesiredState{}
	applier, supported := AsDesiredStateApplier(rt)
	if applier != nil {
		t.Error("expected nil applier for non-implementor")
	}
	if supported {
		t.Error("expected supported = false for non-implementor")
	}
}

// mockRuntimeNoDesiredState is a minimal Runtime that does NOT implement
// DesiredStateApplier, used to test the capability probe negative case.
type mockRuntimeNoDesiredState struct{}

func (m *mockRuntimeNoDesiredState) Type() domain.RuntimeType                { return "mock" }
func (m *mockRuntimeNoDesiredState) Observe(_ context.Context, _, _ uuid.UUID, _ string) (*domain.RuntimeObservation, error) {
	return nil, nil
}
func (m *mockRuntimeNoDesiredState) Deploy(_ context.Context, _, _ string, _ DeployOptions) error {
	return nil
}
func (m *mockRuntimeNoDesiredState) Undeploy(_ context.Context, _ string) error { return nil }
func (m *mockRuntimeNoDesiredState) StreamLogs(_ context.Context, _ string, _ LogOptions) (<-chan LogEntry, error) {
	return nil, nil
}
