package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type f74aPublishCapture struct {
	releases, signatures, sboms, packages, observations int
	lastRelease                                         domain.LLMRelease
}

func (c *f74aPublishCapture) PublishLLMRelease(_ context.Context, release *domain.LLMRelease) error {
	c.releases++
	c.lastRelease = *release
	return nil
}
func (c *f74aPublishCapture) PublishArtifactSignature(context.Context, *domain.ArtifactSignature) error {
	c.signatures++
	return nil
}
func (c *f74aPublishCapture) PublishArtifactSBOM(context.Context, *domain.ArtifactSBOM) error {
	c.sboms++
	return nil
}
func (c *f74aPublishCapture) PublishSBOMPackage(context.Context, *domain.SBOMPackage) error {
	c.packages++
	return nil
}
func (c *f74aPublishCapture) PublishRuntimeObservation(context.Context, *domain.RuntimeObservation) error {
	c.observations++
	return nil
}

type f74aSignatureRepo struct {
	repository.ArtifactSignatureRepository
	creates int
}

func (r *f74aSignatureRepo) Create(_ context.Context, sig *domain.ArtifactSignature) error {
	r.creates++
	if sig.ID == uuid.Nil {
		sig.ID = uuid.New()
	}
	sig.CreatedAt = time.Now().UTC()
	return nil
}

type f74aSBOMRepo struct {
	repository.SBOMRepository
	sboms, packages int
}

func (r *f74aSBOMRepo) CreateSBOM(_ context.Context, sbom *domain.ArtifactSBOM) error {
	r.sboms++
	if sbom.ID == uuid.Nil {
		sbom.ID = uuid.New()
	}
	return nil
}
func (r *f74aSBOMRepo) CreatePackages(_ context.Context, packages []domain.SBOMPackage) error {
	r.packages += len(packages)
	for i := range packages {
		if packages[i].ID == uuid.Nil {
			packages[i].ID = uuid.New()
		}
	}
	return nil
}

type f74aRouteRepo struct {
	repository.LLMRouteRepository
	route *domain.LLMRoute
}

func (r f74aRouteRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	if id == r.route.ID {
		return r.route, nil
	}
	return nil, repository.ErrNotFound
}

type f74aReleaseRepo struct {
	repository.LLMReleaseRepository
	creates int
}

func (r *f74aReleaseRepo) Create(_ context.Context, release *domain.LLMRelease) error {
	r.creates++
	if release.ID == uuid.Nil {
		release.ID = uuid.New()
	}
	release.CreatedAt = time.Now().UTC()
	return nil
}

func TestF74aMutationBoundariesPublishExactlyOnce(t *testing.T) {
	ctx := context.Background()
	capture := &f74aPublishCapture{}
	sigRepo := &f74aSignatureRepo{}
	sig := &domain.ArtifactSignature{ArtifactID: uuid.New()}
	if err := NewCanonicalSignatureRepository(sigRepo, capture, zap.NewNop()).Create(ctx, sig); err != nil {
		t.Fatal(err)
	}
	if sigRepo.creates != 1 || capture.signatures != 1 || sig.ID == uuid.Nil {
		t.Fatalf("signature DB/publish counts: %d/%d", sigRepo.creates, capture.signatures)
	}
	sbomRepo := &f74aSBOMRepo{}
	wrapped := NewCanonicalSBOMRepository(sbomRepo, capture, zap.NewNop())
	sbom := &domain.ArtifactSBOM{ArtifactID: sig.ArtifactID}
	if err := wrapped.CreateSBOM(ctx, sbom); err != nil {
		t.Fatal(err)
	}
	packages := []domain.SBOMPackage{{SBOMID: sbom.ID, Name: "a"}, {SBOMID: sbom.ID, Name: "b"}}
	if err := wrapped.CreatePackages(ctx, packages); err != nil {
		t.Fatal(err)
	}
	if sbomRepo.sboms != 1 || sbomRepo.packages != 2 || capture.sboms != 1 || capture.packages != 2 {
		t.Fatalf("SBOM DB/publish counts: %d/%d/%d/%d", sbomRepo.sboms, sbomRepo.packages, capture.sboms, capture.packages)
	}
	route := &domain.LLMRoute{ID: uuid.New(), Name: "chat"}
	releases := &f74aReleaseRepo{}
	registry := NewLLMRegistryService(f74aRouteRepo{route: route}, releases, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	registry.SetReleaseCPStatePublisher(capture)
	release := &domain.LLMRelease{RouteID: route.ID, Version: "v1", ModelRef: "model/test", ModelSource: domain.ModelSourceHuggingFace, BackendPreferences: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "example/image", ContainerPort: 8000, HostPort: 18000, HealthPath: "/health"}}
	if err := registry.CreateRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	if releases.creates != 1 || capture.releases != 1 {
		t.Fatalf("release DB/publish counts: %d/%d", releases.creates, capture.releases)
	}
}

type f74aMLRepo struct{ *fakeMLRegistryRepo }

func (r *f74aMLRepo) UpsertModelVersion(ctx context.Context, version *domain.MLModelVersion) error {
	version.CreatedAt = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if version.ID == uuid.Nil {
		version.ID = uuid.New()
	}
	return r.fakeMLRegistryRepo.UpsertModelVersion(ctx, version)
}

func TestF74aMLBackedReleasePublishesPersistedTimestampOnce(t *testing.T) {
	ctx := context.Background()
	repo := &f74aMLRepo{newFakeMLRegistryRepo()}
	routeID := uuid.New()
	repo.models[routeID] = &domain.MLModel{ID: routeID}
	capture := &f74aPublishCapture{}
	registry := NewLLMRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
	registry.ml = NewMLRegistryService(repo, nil, zap.NewNop())
	registry.SetReleaseCPStatePublisher(capture)
	release := &domain.LLMRelease{RouteID: routeID, Version: "v1", ModelRef: "model/test", ModelSource: domain.ModelSourceHuggingFace, BackendPreferences: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "example/image", ContainerPort: 8000, HostPort: 18000, HealthPath: "/health"}}
	if err := registry.CreateRelease(ctx, release); err != nil {
		t.Fatal(err)
	}
	if capture.releases != 1 || capture.lastRelease.CreatedAt.IsZero() || capture.lastRelease.CreatedAt != repo.versions[release.ID].CreatedAt {
		t.Fatalf("ML-backed release publication count/timestamp = %d/%s", capture.releases, capture.lastRelease.CreatedAt)
	}
}

type f74aCompatibilityRepo struct {
	repository.SBOMRepository
	sbom     *domain.ArtifactSBOM
	packages []domain.SBOMPackage
}

func (r *f74aCompatibilityRepo) GetSBOMByHash(_ context.Context, hash string) (*domain.ArtifactSBOM, error) {
	if r.sbom != nil && r.sbom.RawHash == hash {
		return r.sbom, nil
	}
	return nil, repository.ErrNotFound
}
func (r *f74aCompatibilityRepo) GetSBOMByArtifact(_ context.Context, id uuid.UUID) (*domain.ArtifactSBOM, error) {
	if r.sbom != nil && r.sbom.ArtifactID == id {
		return r.sbom, nil
	}
	return nil, repository.ErrNotFound
}
func (r *f74aCompatibilityRepo) ListPackagesBySBOM(_ context.Context, id uuid.UUID) ([]domain.SBOMPackage, error) {
	if r.sbom != nil && r.sbom.ID == id {
		return r.packages, nil
	}
	return nil, repository.ErrNotFound
}

type f74aManifestRepo struct {
	repository.SBOMManifestRepository
	compatibility     *f74aCompatibilityRepo
	artifactID        uuid.UUID
	projects, updates int
}

func (r *f74aManifestRepo) ProjectManifest(_ context.Context, m *domain.SBOMManifest, pkgs []domain.SBOMManifestPackage) error {
	r.projects++
	if r.compatibility.sbom == nil {
		r.compatibility.sbom = &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: r.artifactID, RawHash: m.PayloadSHA256}
	}
	for _, p := range pkgs {
		r.compatibility.packages = append(r.compatibility.packages, domain.SBOMPackage{ID: uuid.New(), SBOMID: r.compatibility.sbom.ID, Name: p.Name})
	}
	return nil
}
func (r *f74aManifestRepo) UpdateCompatibilityVulnerabilityCounts(_ context.Context, _ uuid.UUID, _ string, _ domain.SecuritySeverityCounts, total int) error {
	r.updates++
	r.compatibility.sbom.VulnerabilityCount = total
	return nil
}
func TestF74aManifestProjectionPublishesCompatibilityIndex(t *testing.T) {
	ctx := context.Background()
	artifactID := uuid.New()
	compat := &f74aCompatibilityRepo{}
	repo := &f74aManifestRepo{compatibility: compat, artifactID: artifactID}
	capture := &f74aPublishCapture{}
	wrapped := NewCanonicalSBOMManifestRepository(repo, compat, capture, zap.NewNop())
	manifest := &domain.SBOMManifest{Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String()}, PayloadSHA256: "sha256:test"}
	pkgs := []domain.SBOMManifestPackage{{Name: "alpha"}, {Name: "beta"}}
	if err := wrapped.ProjectManifest(ctx, manifest, pkgs); err != nil {
		t.Fatal(err)
	}
	if repo.projects != 1 || capture.sboms != 1 || capture.packages != 2 {
		t.Fatalf("manifest projection counts %d/%d/%d", repo.projects, capture.sboms, capture.packages)
	}
	if err := wrapped.ProjectManifest(ctx, manifest, []domain.SBOMManifestPackage{{Name: "gamma"}}); err != nil {
		t.Fatal(err)
	}
	if repo.projects != 2 || capture.sboms != 2 || capture.packages != 3 {
		t.Fatalf("reprojection duplicated old package events: %d/%d/%d", repo.projects, capture.sboms, capture.packages)
	}
	if err := wrapped.UpdateCompatibilityVulnerabilityCounts(ctx, artifactID, manifest.PayloadSHA256, domain.SecuritySeverityCounts{}, 4); err != nil {
		t.Fatal(err)
	}
	if repo.updates != 1 || capture.sboms != 3 || capture.packages != 3 {
		t.Fatalf("count update publications %d/%d/%d", repo.updates, capture.sboms, capture.packages)
	}
}
