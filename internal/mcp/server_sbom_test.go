package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type testMCPSBOMRepo struct {
	sboms    map[uuid.UUID]*domain.ArtifactSBOM
	packages []domain.SBOMPackage
}

func newTestMCPSBOMRepo() *testMCPSBOMRepo {
	return &testMCPSBOMRepo{sboms: make(map[uuid.UUID]*domain.ArtifactSBOM)}
}

func (m *testMCPSBOMRepo) CreateSBOM(_ context.Context, s *domain.ArtifactSBOM) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	copy := *s
	m.sboms[s.ID] = &copy
	return nil
}

func (m *testMCPSBOMRepo) GetSBOMByID(_ context.Context, id uuid.UUID) (*domain.ArtifactSBOM, error) {
	s, ok := m.sboms[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return s, nil
}

func (m *testMCPSBOMRepo) GetSBOMByArtifact(_ context.Context, artifactID uuid.UUID) (*domain.ArtifactSBOM, error) {
	for _, s := range m.sboms {
		if s.ArtifactID == artifactID {
			return s, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testMCPSBOMRepo) GetSBOMByHash(_ context.Context, rawHash string) (*domain.ArtifactSBOM, error) {
	for _, s := range m.sboms {
		if s.RawHash == rawHash {
			return s, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *testMCPSBOMRepo) CreatePackages(_ context.Context, packages []domain.SBOMPackage) error {
	for _, p := range packages {
		if p.ID == uuid.Nil {
			p.ID = uuid.New()
		}
		m.packages = append(m.packages, p)
	}
	return nil
}

func (m *testMCPSBOMRepo) ListPackagesBySBOM(_ context.Context, sbomID uuid.UUID) ([]domain.SBOMPackage, error) {
	out := make([]domain.SBOMPackage, 0)
	for _, p := range m.packages {
		if p.SBOMID == sbomID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *testMCPSBOMRepo) SearchPackagesByName(_ context.Context, name string, limit int) ([]domain.SBOMPackage, error) {
	if limit <= 0 {
		limit = 100
	}
	needle := strings.ToLower(name)
	out := make([]domain.SBOMPackage, 0)
	for _, p := range m.packages {
		if strings.Contains(strings.ToLower(p.Name), needle) {
			out = append(out, p)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func newTestMCPSBOMServer() (*Server, *testMCPSBOMRepo, uuid.UUID) {
	artifactRepo := newTestArtifactRepo()
	artifactID := uuid.New()
	artifactRepo.artifacts[artifactID] = &domain.Artifact{
		ID:          artifactID,
		BuildID:     uuid.New(),
		ServiceID:   uuid.New(),
		ImageRepo:   "registry.example.com/api",
		ImageTag:    "v1.0.0",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ScanStatus:  domain.ScanStatusClean,
	}

	registry := service.NewRegistryService(
		nil,
		nil,
		nil,
		artifactRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		events.NewInProcessPublisher(zap.NewNop()),
		zap.NewNop(),
	)
	sbomRepo := newTestMCPSBOMRepo()
	server := newTestServerWithOptions(registry, zap.NewNop(), ServerDeps{})
	return server, sbomRepo, artifactID
}

func TestGetTools_IncludesSBOMTools(t *testing.T) {
	server, _, _ := newTestMCPSBOMServer()
	required := map[string]bool{
		"bahia_get_sbom":             false,
		"bahia_get_sbom_packages":    false,
		"bahia_search_sbom_packages": false,
		"bahia_ingest_sbom":          false,
	}
	for _, tool := range server.GetTools() {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing %s tool", name)
		}
	}
}

func TestCallTool_SBOMValidationAndConfigurationErrors(t *testing.T) {
	ctx := authorizedMCPContext()
	server, _, artifactID := newTestMCPSBOMServer()

	missingData, err := server.CallTool(ctx, "bahia_ingest_sbom", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("missing data err: %v", err)
	}
	if !missingData.IsError {
		t.Fatalf("expected missing sbom_data to fail")
	}

	missingArtifact, err := server.CallTool(ctx, "bahia_ingest_sbom", map[string]interface{}{
		"artifact_id": uuid.New().String(),
		"sbom_data":   `{"bomFormat":"CycloneDX"}`,
	})
	if err != nil {
		t.Fatalf("missing artifact err: %v", err)
	}
	if !missingArtifact.IsError {
		t.Fatalf("expected missing artifact to fail")
	}

	unconfigured := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := unconfigured.CallTool(ctx, "bahia_get_sbom", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("unconfigured get err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected unconfigured SBOM tools to fail")
	}
}
