package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestIngestSBOMDelegatesToImportServiceAndReturnsCompatibilityProjection(t *testing.T) {
	artifactID := uuid.New()
	payload := []byte(`{"spdxVersion":"SPDX-2.3"}`)
	rawHash := sha256HexForTest(payload)
	projection := &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: artifactID, Format: domain.SBOMFormatSPDX, RawHash: rawHash, CreatedAt: time.Now().UTC()}
	sbomRepo := &fakeHandlerSBOMRepo{byHash: map[string]*domain.ArtifactSBOM{}}
	artifactRepo := &fakeHandlerArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ImageRepo: "registry.example/app", ImageTag: "1.0.0", ImageDigest: "sha256:artifactdigest"}}
	importer := &fakeHandlerSBOMImporter{onImport: func(req service.SBOMImportRequest) {
		sbomRepo.byHash[rawHash] = projection
	}}
	h := NewSBOMHandler(sbomRepo, artifactRepo, importer)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactID.String()+"/sbom", bytes.NewReader(payload))
	req = withURLParam(req, "id", artifactID.String())
	rr := httptest.NewRecorder()

	h.IngestSBOM(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if importer.calls != 1 {
		t.Fatalf("importer calls = %d, want 1", importer.calls)
	}
	if got := importer.request.IDempotencyKey; got != "rest-artifact-sbom:"+artifactID.String()+":"+rawHash {
		t.Fatalf("idempotency key = %q", got)
	}
	if importer.request.Subject.Type != domain.SBOMSubjectArtifact || importer.request.Subject.ID != artifactID.String() || importer.request.Subject.Digest != "sha256:artifactdigest" {
		t.Fatalf("unexpected subject: %+v", importer.request.Subject)
	}
	if string(importer.request.Payload) != string(payload) {
		t.Fatalf("import payload = %q", string(importer.request.Payload))
	}
	if importer.request.Storage != domain.SBOMStorageBlossom {
		t.Fatalf("storage = %q", importer.request.Storage)
	}
	if sbomRepo.createSBOMCalls != 0 || sbomRepo.createPackageCalls != 0 {
		t.Fatalf("direct storage calls = sbom:%d packages:%d, want none", sbomRepo.createSBOMCalls, sbomRepo.createPackageCalls)
	}
}

func TestIngestSBOMReturnsExistingProjectionWithoutReimport(t *testing.T) {
	artifactID := uuid.New()
	payload := []byte(`{"bomFormat":"CycloneDX"}`)
	rawHash := sha256HexForTest(payload)
	existing := &domain.ArtifactSBOM{ID: uuid.New(), ArtifactID: artifactID, Format: domain.SBOMFormatCycloneDX, RawHash: rawHash}
	sbomRepo := &fakeHandlerSBOMRepo{byHash: map[string]*domain.ArtifactSBOM{rawHash: existing}}
	artifactRepo := &fakeHandlerArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ImageDigest: "sha256:artifactdigest"}}
	importer := &fakeHandlerSBOMImporter{}
	h := NewSBOMHandler(sbomRepo, artifactRepo, importer)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactID.String()+"/sbom", bytes.NewReader(payload))
	req = withURLParam(req, "id", artifactID.String())
	rr := httptest.NewRecorder()

	h.IngestSBOM(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if importer.calls != 0 {
		t.Fatalf("importer calls = %d, want 0 for existing projection", importer.calls)
	}
}

func TestIngestSBOMRejectsOversizedPayloadWithoutImport(t *testing.T) {
	artifactID := uuid.New()
	payload := bytes.Repeat([]byte("a"), maxSBOMIngestBytes+1)
	sbomRepo := &fakeHandlerSBOMRepo{byHash: map[string]*domain.ArtifactSBOM{}}
	artifactRepo := &fakeHandlerArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ImageDigest: "sha256:artifactdigest"}}
	importer := &fakeHandlerSBOMImporter{}
	h := NewSBOMHandler(sbomRepo, artifactRepo, importer)

	req := httptest.NewRequest(http.MethodPost, "/artifacts/"+artifactID.String()+"/sbom", bytes.NewReader(payload))
	req = withURLParam(req, "id", artifactID.String())
	rr := httptest.NewRecorder()

	h.IngestSBOM(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if importer.calls != 0 {
		t.Fatalf("importer calls = %d, want 0 for oversized payload", importer.calls)
	}
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func sha256HexForTest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type fakeHandlerSBOMImporter struct {
	calls    int
	request  service.SBOMImportRequest
	onImport func(service.SBOMImportRequest)
	err      error
}

func (f *fakeHandlerSBOMImporter) Import(_ context.Context, req service.SBOMImportRequest) (*service.SBOMRunResult, error) {
	f.calls++
	f.request = req
	if f.onImport != nil {
		f.onImport(req)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &service.SBOMRunResult{RunID: req.IDempotencyKey, Subject: req.Subject, PublishState: domain.SBOMPublishPublished}, nil
}

type fakeHandlerArtifactRepo struct {
	artifact *domain.Artifact
	err      error
}

func (f *fakeHandlerArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (f *fakeHandlerArtifactRepo) GetByID(context.Context, uuid.UUID) (*domain.Artifact, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.artifact == nil {
		return nil, repository.ErrNotFound
	}
	return f.artifact, nil
}
func (f *fakeHandlerArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeHandlerArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeHandlerArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (f *fakeHandlerArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type fakeHandlerSBOMRepo struct {
	byHash             map[string]*domain.ArtifactSBOM
	createSBOMCalls    int
	createPackageCalls int
}

func (f *fakeHandlerSBOMRepo) CreateSBOM(context.Context, *domain.ArtifactSBOM) error {
	f.createSBOMCalls++
	return nil
}
func (f *fakeHandlerSBOMRepo) GetSBOMByID(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeHandlerSBOMRepo) GetSBOMByArtifact(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeHandlerSBOMRepo) GetSBOMByHash(_ context.Context, rawHash string) (*domain.ArtifactSBOM, error) {
	if f.byHash != nil {
		if sbom := f.byHash[rawHash]; sbom != nil {
			return sbom, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (f *fakeHandlerSBOMRepo) CreatePackages(context.Context, []domain.SBOMPackage) error {
	f.createPackageCalls++
	return nil
}
func (f *fakeHandlerSBOMRepo) ListPackagesBySBOM(context.Context, uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (f *fakeHandlerSBOMRepo) SearchPackagesByName(context.Context, string, int) ([]domain.SBOMPackage, error) {
	return nil, nil
}
