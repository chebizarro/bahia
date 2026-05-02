package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
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
	return NewServer(registry, zap.NewNop())
}

func TestGetTools_IncludesBuildArtifactRegister(t *testing.T) {
	server := newTestMCPBuildArtifactServer()
	tools := server.GetTools()
	foundBuild := false
	foundUpdateBuildStatus := false
	foundArtifact := false
	for _, tool := range tools {
		if tool.Name == "bahia_register_build" {
			foundBuild = true
		}
		if tool.Name == "bahia_update_build_status" {
			foundUpdateBuildStatus = true
		}
		if tool.Name == "bahia_register_artifact" {
			foundArtifact = true
		}
	}
	if !foundBuild {
		t.Fatalf("missing bahia_register_build tool")
	}
	if !foundUpdateBuildStatus {
		t.Fatalf("missing bahia_update_build_status tool")
	}
	if !foundArtifact {
		t.Fatalf("missing bahia_register_artifact tool")
	}
}

func TestCallTool_RegisterBuild_AndRegisterArtifact(t *testing.T) {
	ctx := context.Background()
	server := newTestMCPBuildArtifactServer()
	serviceID := uuid.New().String()

	buildRes, err := server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": serviceID,
		"git_sha":    "abc1234",
		"git_ref":    "refs/heads/main",
		"ci_run_id":  "run-1",
	})
	if err != nil {
		t.Fatalf("register build err: %v", err)
	}
	if buildRes.IsError {
		t.Fatalf("register build returned error: %s", buildRes.Content[0].Text)
	}
	buildPayload := decodeResultMap(t, buildRes)
	buildID := buildPayload["build_id"].(string)
	build := buildPayload["build"].(map[string]interface{})
	if build["ci_system"] != "hive-ci" {
		t.Fatalf("expected default ci_system hive-ci, got %v", build["ci_system"])
	}

	artifactRes, err := server.CallTool(ctx, "bahia_register_artifact", map[string]interface{}{
		"build_id":     buildID,
		"service_id":   serviceID,
		"image_repo":   "registry.example.com/api",
		"image_tag":    "v1.2.3",
		"image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"scan_status":  "clean",
	})
	if err != nil {
		t.Fatalf("register artifact err: %v", err)
	}
	if artifactRes.IsError {
		t.Fatalf("register artifact returned error: %s", artifactRes.Content[0].Text)
	}
	artifactPayload := decodeResultMap(t, artifactRes)
	artifact := artifactPayload["artifact"].(map[string]interface{})
	if artifact["build_id"] != buildID {
		t.Fatalf("expected build_id %s, got %v", buildID, artifact["build_id"])
	}
	if artifact["scan_status"] != "clean" {
		t.Fatalf("expected scan_status clean, got %v", artifact["scan_status"])
	}
}

func TestCallTool_RegisterBuildAndArtifact_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	server := newTestMCPBuildArtifactServer()

	buildRes, err := server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": uuid.New().String(),
		"git_sha":    "not-hex",
		"git_ref":    "refs/heads/main",
		"ci_run_id":  "run-1",
	})
	if err != nil {
		t.Fatalf("register build err: %v", err)
	}
	if !buildRes.IsError {
		t.Fatalf("expected register build to fail validation")
	}

	artifactRes, err := server.CallTool(ctx, "bahia_register_artifact", map[string]interface{}{
		"build_id":     uuid.New().String(),
		"service_id":   uuid.New().String(),
		"image_repo":   "registry.example.com/api",
		"image_tag":    "v1.2.3",
		"image_digest": "bad-digest",
	})
	if err != nil {
		t.Fatalf("register artifact err: %v", err)
	}
	if !artifactRes.IsError {
		t.Fatalf("expected register artifact to fail validation")
	}
}

func TestCallTool_UpdateBuildStatus(t *testing.T) {
	ctx := context.Background()
	server := newTestMCPBuildArtifactServer()
	serviceID := uuid.New().String()

	buildRes, err := server.CallTool(ctx, "bahia_register_build", map[string]interface{}{
		"service_id": serviceID,
		"git_sha":    "abc1234",
		"git_ref":    "refs/heads/main",
		"ci_run_id":  "run-2",
	})
	if err != nil {
		t.Fatalf("register build err: %v", err)
	}
	if buildRes.IsError {
		t.Fatalf("register build returned error: %s", buildRes.Content[0].Text)
	}
	buildID := decodeResultMap(t, buildRes)["build_id"].(string)

	updateRes, err := server.CallTool(ctx, "bahia_update_build_status", map[string]interface{}{
		"build_id": buildID,
		"status":   "running",
	})
	if err != nil {
		t.Fatalf("update build status err: %v", err)
	}
	if updateRes.IsError {
		t.Fatalf("update build status returned error: %s", updateRes.Content[0].Text)
	}
	payload := decodeResultMap(t, updateRes)
	build := payload["build"].(map[string]interface{})
	if build["status"] != "running" {
		t.Fatalf("expected status running, got %v", build["status"])
	}
}

func TestCallTool_UpdateBuildStatus_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	server := newTestMCPBuildArtifactServer()

	res, err := server.CallTool(ctx, "bahia_update_build_status", map[string]interface{}{
		"build_id": "not-a-uuid",
		"status":   "running",
	})
	if err != nil {
		t.Fatalf("update build status err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected update build status to fail for invalid build_id")
	}

	res, err = server.CallTool(ctx, "bahia_update_build_status", map[string]interface{}{
		"build_id": uuid.New().String(),
		"status":   "bogus",
	})
	if err != nil {
		t.Fatalf("update build status err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected update build status to fail for invalid status")
	}

	res, err = server.CallTool(ctx, "bahia_update_build_status", map[string]interface{}{
		"build_id": uuid.New().String(),
		"status":   "running",
	})
	if err != nil {
		t.Fatalf("update build status err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected update build status to fail for missing build")
	}
}
