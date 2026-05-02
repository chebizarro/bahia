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
	server := NewServerWithOptions(registry, zap.NewNop(), ServerDeps{SBOMs: sbomRepo})
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

func TestCallTool_IngestGetListAndSearchSBOM(t *testing.T) {
	ctx := context.Background()
	server, repo, artifactID := newTestMCPSBOMServer()
	sbomData := `{
		"bomFormat": "CycloneDX",
		"specVersion": "1.5",
		"version": 1,
		"components": [
			{"type": "library", "name": "lodash", "version": "4.17.21", "purl": "pkg:npm/lodash@4.17.21", "licenses": [{"license": {"id": "MIT"}}]},
			{"type": "library", "name": "log4j-core", "version": "2.14.1", "purl": "pkg:maven/org.apache.logging.log4j/log4j-core@2.14.1"}
		],
		"vulnerabilities": [
			{"id": "CVE-2021-44228", "ratings": [{"severity": "critical"}]}
		]
	}`

	ingestRes, err := server.CallTool(ctx, "bahia_ingest_sbom", map[string]interface{}{
		"artifact_id": artifactID.String(),
		"sbom_data":   sbomData,
		"source_url":  "oci://registry.example.com/api@sha256:sbom",
	})
	if err != nil {
		t.Fatalf("ingest sbom err: %v", err)
	}
	if ingestRes.IsError {
		t.Fatalf("ingest sbom returned error: %s", ingestRes.Content[0].Text)
	}
	ingestPayload := decodeResultMap(t, ingestRes)
	if ingestPayload["status"] != "created" {
		t.Fatalf("status = %v, want created", ingestPayload["status"])
	}
	if int(ingestPayload["package_count"].(float64)) != 2 {
		t.Fatalf("package_count = %v, want 2", ingestPayload["package_count"])
	}
	if len(repo.sboms) != 1 || len(repo.packages) != 2 {
		t.Fatalf("repo stored %d sboms and %d packages, want 1 and 2", len(repo.sboms), len(repo.packages))
	}

	getRes, err := server.CallTool(ctx, "bahia_get_sbom", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("get sbom err: %v", err)
	}
	if getRes.IsError {
		t.Fatalf("get sbom returned error: %s", getRes.Content[0].Text)
	}
	getPayload := decodeResultMap(t, getRes)
	if getPayload["artifact_id"] != artifactID.String() {
		t.Fatalf("artifact_id = %v, want %s", getPayload["artifact_id"], artifactID.String())
	}
	if getPayload["source_url"] != "oci://registry.example.com/api@sha256:sbom" {
		t.Fatalf("source_url = %v", getPayload["source_url"])
	}
	if int(getPayload["critical_count"].(float64)) != 1 {
		t.Fatalf("critical_count = %v, want 1", getPayload["critical_count"])
	}

	packagesRes, err := server.CallTool(ctx, "bahia_get_sbom_packages", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("get sbom packages err: %v", err)
	}
	if packagesRes.IsError {
		t.Fatalf("get sbom packages returned error: %s", packagesRes.Content[0].Text)
	}
	packagesPayload := decodeResultMap(t, packagesRes)
	if int(packagesPayload["total"].(float64)) != 2 {
		t.Fatalf("packages total = %v, want 2", packagesPayload["total"])
	}

	searchRes, err := server.CallTool(ctx, "bahia_search_sbom_packages", map[string]interface{}{"query": "lodash", "limit": 10})
	if err != nil {
		t.Fatalf("search sbom packages err: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("search sbom packages returned error: %s", searchRes.Content[0].Text)
	}
	searchPayload := decodeResultMap(t, searchRes)
	if int(searchPayload["total"].(float64)) != 1 {
		t.Fatalf("search total = %v, want 1", searchPayload["total"])
	}
	packages := searchPayload["packages"].([]interface{})
	pkg := packages[0].(map[string]interface{})
	if pkg["ecosystem"] != "npm" || pkg["license"] != "MIT" {
		t.Fatalf("unexpected package payload: %#v", pkg)
	}

	duplicateRes, err := server.CallTool(ctx, "bahia_ingest_sbom", map[string]interface{}{
		"artifact_id": artifactID.String(),
		"sbom_data":   sbomData,
	})
	if err != nil {
		t.Fatalf("duplicate ingest err: %v", err)
	}
	if duplicateRes.IsError {
		t.Fatalf("duplicate ingest returned error: %s", duplicateRes.Content[0].Text)
	}
	duplicatePayload := decodeResultMap(t, duplicateRes)
	if duplicatePayload["status"] != "existing" {
		t.Fatalf("duplicate status = %v, want existing", duplicatePayload["status"])
	}
	if len(repo.sboms) != 1 || len(repo.packages) != 2 {
		t.Fatalf("duplicate ingest changed storage to %d sboms and %d packages", len(repo.sboms), len(repo.packages))
	}
}

func TestCallTool_SBOMValidationAndConfigurationErrors(t *testing.T) {
	ctx := context.Background()
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

	unconfigured := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := unconfigured.CallTool(ctx, "bahia_get_sbom", map[string]interface{}{"artifact_id": artifactID.String()})
	if err != nil {
		t.Fatalf("unconfigured get err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected unconfigured SBOM tools to fail")
	}
}
