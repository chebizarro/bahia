package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
)

const testSubjectDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSBOMOrchestratorGeneratePublishesProjectsAndAudits(t *testing.T) {
	ctx := context.Background()
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: true, Reason: "stored"}}}
	repo := newFakeSBOMManifestRepo()
	repo.eventCount = func() int { return len(publisher.events) }
	orchestrator := newTestSBOMOrchestrator(t, publisher, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	result, err := orchestrator.Generate(ctx, SBOMGenerateRequest{
		IDempotencyKey: "run-1",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest, DisplayName: "artifact"},
		Source:         sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"},
		Formats:        []domain.SBOMFormat{domain.SBOMFormatSPDX},
		Generator:      sbomadapter.GeneratorSyft,
		Storage:        domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result.PublishState != domain.SBOMPublishPublished || len(result.ManifestIDs) != 1 || len(result.ReferenceEventIDs) != 1 || result.AvailabilityID == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(repo.projected) != 1 || len(repo.packages) != 1 {
		t.Fatalf("projection counts manifests=%d packages=%d", len(repo.projected), len(repo.packages))
	}
	manifest := repo.projected[0]
	if manifest.StorageType != domain.SBOMStorageBlossom || manifest.ReferenceDTag == "" || manifest.AvailabilityDTag == "" || manifest.PayloadSHA256 == "" || manifest.PublishState != domain.SBOMPublishPublished {
		t.Fatalf("manifest projection missing canonical publication fields: %#v", manifest)
	}
	if len(repo.projectEventCounts) != 1 || repo.projectEventCounts[0] <= publisher.firstKindIndex(sbomadapter.KindSBOMAvailabilityList) {
		t.Fatalf("projection happened before accepted availability list publication: project counts=%v", repo.projectEventCounts)
	}
	assertPublishedKinds(t, publisher, []int{KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, KindSBOMStatus, sbomadapter.KindSBOMReference, KindSBOMStatus, sbomadapter.KindSBOMAvailabilityList, KindSBOMStatus, KindSBOMAudit, KindSBOMStatus})
	if publisher.containsKind(sbomadapter.KindLegacySBOMIndex) {
		t.Fatalf("legacy 30079 publication observed")
	}

	cached, err := orchestrator.Generate(ctx, SBOMGenerateRequest{IDempotencyKey: "run-1", Subject: manifest.Subject, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err != nil {
		t.Fatalf("cached Generate() error = %v", err)
	}
	if cached.AvailabilityID != result.AvailabilityID || len(publisher.events) != 11 {
		t.Fatalf("idempotency did not return cached result without new events")
	}
}

func TestSBOMOrchestratorStopsOnRelayRejection(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Reason: "auth-required: restricted write"}}}
	repo := newFakeSBOMManifestRepo()
	orchestrator := newTestSBOMOrchestrator(t, publisher, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-auth", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "auth-required") {
		t.Fatalf("Generate() error = %v, want auth-required rejection", err)
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after relay rejection")
	}
}

func TestSBOMOrchestratorFailsOnClosedLikePublishError(t *testing.T) {
	publisher := &fakeSBOMPublisher{results: []sbomadapter.PublishOKResult{{RelayURL: "wss://relay.example", Accepted: false, Error: errors.New("CLOSED: policy violation")}}}
	repo := newFakeSBOMManifestRepo()
	orchestrator := newTestSBOMOrchestrator(t, publisher, repo, fakeGenerator{payload: testSPDXPayload(t), generatorID: sbomadapter.GeneratorSyft})

	_, err := orchestrator.Generate(context.Background(), SBOMGenerateRequest{IDempotencyKey: "run-closed", Subject: domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: "artifact-1", Digest: testSubjectDigest}, Source: sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/tmp/source"}, Formats: []domain.SBOMFormat{domain.SBOMFormatSPDX}, Generator: sbomadapter.GeneratorSyft})
	if err == nil || !strings.Contains(err.Error(), "CLOSED") {
		t.Fatalf("Generate() error = %v, want CLOSED-like failure", err)
	}
	if len(repo.projected) != 0 {
		t.Fatalf("projection occurred after CLOSED-like failure")
	}
}

func TestSBOMSubjectResolverArtifactAndDeployment(t *testing.T) {
	artifactID := uuid.New()
	intentID := uuid.New()
	resolver := SBOMSubjectResolver{Artifacts: fakeArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ImageRepo: "ghcr.io/acme/api", ImageTag: "v1", ImageDigest: testSubjectDigest}}, Deployments: fakeDeploymentRepo{intent: &domain.DeploymentIntent{ID: intentID, DesiredHash: testSubjectDigest}}}

	artifact, err := resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectArtifact, ID: artifactID.String()})
	if err != nil || artifact.Digest != testSubjectDigest || artifact.DisplayName != "ghcr.io/acme/api:v1" {
		t.Fatalf("artifact Resolve() = %#v, %v", artifact, err)
	}
	deployment, err := resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectDeployment, ID: intentID.String()})
	if err != nil || deployment.Digest != testSubjectDigest {
		t.Fatalf("deployment Resolve() = %#v, %v", deployment, err)
	}
	_, err = resolver.Resolve(context.Background(), domain.SBOMSubject{Type: domain.SBOMSubjectRepository, ID: "repo"})
	if err == nil || !strings.Contains(err.Error(), "provide subject.digest") {
		t.Fatalf("repository ambiguity error = %v", err)
	}
}

func newTestSBOMOrchestrator(t *testing.T, publisher *fakeSBOMPublisher, repo *fakeSBOMManifestRepo, generator fakeGenerator) *SBOMOrchestrator {
	t.Helper()
	registry, err := sbomadapter.NewGeneratorRegistry(generator, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := nostr.Generate()
	publisher.signer = secret
	return NewSBOMOrchestrator(SBOMOrchestratorConfig{Generators: registry, Storage: sbomadapter.NewStorageResolver(&sbomadapter.MockBlossomClient{Blobs: map[string][]byte{}}, nil, nil, slog.Default()), Repo: repo, Publisher: publisher, Pubkey: secret.Public().Hex()})
}

type fakeGenerator struct {
	payload     []byte
	generatorID sbomadapter.GeneratorID
}

func (g fakeGenerator) ID() sbomadapter.GeneratorID { return g.generatorID }
func (g fakeGenerator) GenerateSBOM(_ context.Context, req sbomadapter.GenerateRequest) (*sbomadapter.GenerateResult, error) {
	return &sbomadapter.GenerateResult{Subject: req.Subject, Format: req.Format, MediaType: sbomadapter.MediaTypeForFormat(req.Format), Payload: g.payload, Generator: domain.SBOMGenerator{ID: string(g.generatorID), Version: "test"}, Source: req.Source}, nil
}

type fakeSBOMPublisher struct {
	results []sbomadapter.PublishOKResult
	events  []nostr.Event
	signer  nostr.SecretKey
}

func (p *fakeSBOMPublisher) PublishSignedEventWithResults(_ context.Context, ev *nostr.Event) ([]sbomadapter.PublishOKResult, error) {
	if err := ev.Sign(p.signer); err != nil {
		return nil, err
	}
	p.events = append(p.events, *ev)
	return p.results, nil
}

func (p *fakeSBOMPublisher) containsKind(kind int) bool {
	return p.firstKindIndex(kind) >= 0
}

func (p *fakeSBOMPublisher) firstKindIndex(kind int) int {
	for i, ev := range p.events {
		if int(ev.Kind) == kind {
			return i
		}
	}
	return -1
}

func assertPublishedKinds(t *testing.T, publisher *fakeSBOMPublisher, kinds []int) {
	t.Helper()
	if len(publisher.events) != len(kinds) {
		t.Fatalf("published %d events, want %d", len(publisher.events), len(kinds))
	}
	for i, want := range kinds {
		if int(publisher.events[i].Kind) != want {
			t.Fatalf("event %d kind = %d, want %d", i, publisher.events[i].Kind, want)
		}
	}
}

type fakeSBOMManifestRepo struct {
	projected          []domain.SBOMManifest
	packages           []domain.SBOMManifestPackage
	eventCount         func() int
	projectEventCounts []int
}

func newFakeSBOMManifestRepo() *fakeSBOMManifestRepo { return &fakeSBOMManifestRepo{} }
func (r *fakeSBOMManifestRepo) CreateManifest(context.Context, *domain.SBOMManifest) error {
	return nil
}
func (r *fakeSBOMManifestRepo) ProjectManifest(_ context.Context, manifest *domain.SBOMManifest, packages []domain.SBOMManifestPackage) error {
	if r.eventCount != nil {
		r.projectEventCounts = append(r.projectEventCounts, r.eventCount())
	}
	r.projected = append(r.projected, *manifest)
	r.packages = append(r.packages, packages...)
	return nil
}
func (r *fakeSBOMManifestRepo) UpdateCompatibilityVulnerabilityCounts(context.Context, uuid.UUID, string, domain.SecuritySeverityCounts, int) error {
	return nil
}
func (r *fakeSBOMManifestRepo) GetManifestByID(_ context.Context, id uuid.UUID) (*domain.SBOMManifest, error) {
	return nil, nil
}
func (r *fakeSBOMManifestRepo) ListManifestsBySubject(_ context.Context, subject domain.SBOMSubject, limit int) ([]domain.SBOMManifest, error) {
	return append([]domain.SBOMManifest(nil), r.projected...), nil
}
func (r *fakeSBOMManifestRepo) UpdateManifestPublishState(_ context.Context, id uuid.UUID, state domain.SBOMPublishState, referenceEventID, availabilityEventID, publishError string) error {
	return nil
}
func (r *fakeSBOMManifestRepo) CreateManifestPackages(context.Context, []domain.SBOMManifestPackage) error {
	return nil
}
func (r *fakeSBOMManifestRepo) ListPackagesByManifest(context.Context, uuid.UUID) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}
func (r *fakeSBOMManifestRepo) SearchManifestPackagesByName(context.Context, string, int) ([]domain.SBOMManifestPackage, error) {
	return nil, nil
}

type fakeArtifactRepo struct{ artifact *domain.Artifact }

func (r fakeArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (r fakeArtifactRepo) GetByID(context.Context, uuid.UUID) (*domain.Artifact, error) {
	return r.artifact, nil
}
func (r fakeArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r fakeArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type fakeDeploymentRepo struct{ intent *domain.DeploymentIntent }

func (r fakeDeploymentRepo) Create(context.Context, *domain.DeploymentIntent) error { return nil }
func (r fakeDeploymentRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentIntent, error) {
	return r.intent, nil
}
func (r fakeDeploymentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r fakeDeploymentRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (r fakeDeploymentRepo) UpdateStatus(context.Context, uuid.UUID, domain.DeploymentIntentStatus) error {
	return nil
}
func (r fakeDeploymentRepo) UpdateApproval(context.Context, uuid.UUID, domain.ApprovalStatus) error {
	return nil
}
func (r fakeDeploymentRepo) UpdateDesiredState(context.Context, uuid.UUID, *domain.DesiredServiceSpec, string) error {
	return nil
}

func testSPDXPayload(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"fixture","creationInfo":{"created":"2026-06-13T00:00:00Z","creators":["Tool: test"]},"packages":[{"name":"pkg","SPDXID":"SPDXRef-Package-pkg","versionInfo":"1.0.0","supplier":"Organization: Acme","licenseConcluded":"MIT","externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:generic/pkg@1.0.0"}]}],"relationships":[{"spdxElementId":"SPDXRef-DOCUMENT","relationshipType":"DESCRIBES","relatedSpdxElement":"SPDXRef-Package-pkg"}]}`)
}
