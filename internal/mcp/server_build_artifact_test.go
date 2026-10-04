package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testBuildRepo struct {
	builds map[uuid.UUID]*domain.Build
}

func newTestBuildRepo() *testBuildRepo {
	return &testBuildRepo{builds: make(map[uuid.UUID]*domain.Build)}
}

func (m *testBuildRepo) Create(_ context.Context, b *domain.Build) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	m.builds[b.ID] = b
	return nil
}

func (m *testBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	b, ok := m.builds[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return b, nil
}

func (m *testBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	for _, b := range m.builds {
		if b.CISystem == ciSystem && b.CIRunID == ciRunID {
			return b, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testBuildRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Build, error) {
	out := make([]domain.Build, 0)
	for _, b := range m.builds {
		if b.ServiceID == serviceID {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (m *testBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	b, ok := m.builds[id]
	if !ok {
		return repository.ErrNotFound
	}
	b.Status = status
	return nil
}

type testArtifactRepo struct {
	artifacts map[uuid.UUID]*domain.Artifact
}

func newTestArtifactRepo() *testArtifactRepo {
	return &testArtifactRepo{artifacts: make(map[uuid.UUID]*domain.Artifact)}
}

func (m *testArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	m.artifacts[a.ID] = a
	return nil
}

func (m *testArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	a, ok := m.artifacts[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return a, nil
}

func (m *testArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == repo && a.ImageDigest == digest {
			return a, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == imageRepo && a.ImageDigest == imageDigest {
			return a, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	out := make([]domain.Artifact, 0)
	for _, a := range m.artifacts {
		if a.ServiceID == serviceID {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *testArtifactRepo) ListByBuild(_ context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	out := make([]domain.Artifact, 0)
	for _, a := range m.artifacts {
		if a.BuildID == buildID {
			out = append(out, *a)
		}
	}
	return out, nil
}

type captureArtifactCommandPublisher struct {
	register *controlplane.ArtifactRegisterCommand
	err      error
}

func (p *captureArtifactCommandPublisher) PublishArtifactRegisterRequest(_ context.Context, cmd controlplane.ArtifactRegisterCommand) (*controlplane.ArtifactCommandReceipt, error) {
	p.register = &cmd
	if p.err != nil {
		return nil, p.err
	}
	return &controlplane.ArtifactCommandReceipt{RequestEventID: "artifact-event", RequestPubkey: "operator", RequestKind: controlplane.KindContextVMMessage, ResultKind: controlplane.KindContextVMMessage, RegistryKind: controlplane.KindCASControlState, Status: "submitted", PublishedRelays: 1, BuildID: cmd.BuildID.String(), ServiceID: cmd.ServiceID.String(), ImageDigest: cmd.ImageDigest}, nil
}

func newTestMCPBuildArtifactServer() *Server {
	buildRepo := newTestBuildRepo()
	artifactRepo := newTestArtifactRepo()
	registry := service.NewRegistryService(
		nil,
		nil,
		buildRepo,
		artifactRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	return newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{ArtifactCommandPublisher: &captureArtifactCommandPublisher{}})
}

func TestGetTools_ExcludesManualBuildWrites(t *testing.T) {
	server := newTestMCPBuildArtifactServer()
	foundArtifact := false
	for _, tool := range server.GetTools() {
		if tool.Name == "bahia_register_build" || tool.Name == "bahia_update_build_status" {
			t.Fatalf("manual build write tool remains exposed: %s", tool.Name)
		}
		if tool.Name == "bahia_register_artifact" {
			foundArtifact = true
		}
	}
	if !foundArtifact {
		t.Fatal("missing artifact registration tool")
	}
	for _, name := range []string{"bahia_register_build", "bahia_update_build_status"} {
		result, err := server.CallTool(authorizedMCPContext(), name, map[string]interface{}{})
		if err != nil || !result.IsError {
			t.Fatalf("%s must be rejected: %#v %v", name, result, err)
		}
	}
}

func TestCallTool_RegisterArtifact(t *testing.T) {
	server := newTestMCPBuildArtifactServer()
	buildID, serviceID := uuid.NewString(), uuid.NewString()
	artifactRes, err := server.CallTool(authorizedMCPContext(), "bahia_register_artifact", map[string]interface{}{
		"build_id": buildID, "service_id": serviceID,
		"image_repo": "registry.example.com/api", "image_tag": "v1.2.3",
		"image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"scan_status":  "clean",
	})
	if err != nil || artifactRes.IsError {
		t.Fatalf("register artifact: %v %#v", err, artifactRes)
	}
	payload := decodeResultMap(t, artifactRes)
	if payload["request_event_id"] != "artifact-event" || payload["build_id"] != buildID || payload["service_id"] != serviceID {
		t.Fatalf("unexpected receipt: %#v", payload)
	}
}

func TestArtifactRegisterUsesStableContextVMKey(t *testing.T) {
	server := newTestMCPBuildArtifactServer()
	publisher := server.artifactCommands.(*captureArtifactCommandPublisher)
	args := map[string]interface{}{
		"build_id": uuid.NewString(), "service_id": uuid.NewString(),
		"image_repo": "registry.example/api", "image_tag": "v1",
		"image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"scan_status":  "clean", "_meta": map[string]any{"progressToken": "artifact-call-1"},
	}
	var expectedKey string
	for i := 0; i < 2; i++ {
		result, err := server.CallTool(authorizedMCPContext(), "bahia_register_artifact", args)
		if err != nil || result.IsError {
			t.Fatalf("register artifact attempt %d: %v, %#v", i, err, result)
		}
		if publisher.register == nil || publisher.register.IdempotencyKey == "" {
			t.Fatal("missing ContextVM key")
		}
		if i == 0 {
			expectedKey = publisher.register.IdempotencyKey
		}
		if publisher.register.IdempotencyKey != expectedKey {
			t.Fatal("retry changed ContextVM key")
		}
	}
}

func TestCallTool_RegisterArtifact_ValidationErrors(t *testing.T) {
	server := newTestMCPBuildArtifactServer()
	result, err := server.CallTool(authorizedMCPContext(), "bahia_register_artifact", map[string]interface{}{
		"build_id": uuid.NewString(), "service_id": uuid.NewString(),
		"image_repo": "registry.example.com/api", "image_tag": "v1.2.3", "image_digest": "bad-digest",
	})
	if err != nil || !result.IsError {
		t.Fatalf("invalid artifact must be rejected: %#v %v", result, err)
	}
}
