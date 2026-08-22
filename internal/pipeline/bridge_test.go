package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	registryadapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type mockHiveRepo struct {
	runs    map[string]*domain.HiveCIWorkflowRun
	results map[string]*domain.HiveCIWorkflowResult
	policy  *domain.HiveCIPipelinePolicy
	updated map[string]domain.HiveCIProcessingState
}

func newMockHiveRepo() *mockHiveRepo {
	return &mockHiveRepo{
		runs:    map[string]*domain.HiveCIWorkflowRun{},
		results: map[string]*domain.HiveCIWorkflowResult{},
		updated: map[string]domain.HiveCIProcessingState{},
	}
}

func (m *mockHiveRepo) UpsertWorkflowRun(_ context.Context, _ domain.HiveCIWorkflowRun) error {
	return nil
}
func (m *mockHiveRepo) UpsertWorkflowResult(_ context.Context, _ domain.HiveCIWorkflowResult) error {
	return nil
}
func (m *mockHiveRepo) GetRunByEventID(_ context.Context, eventID string) (*domain.HiveCIWorkflowRun, error) {
	return m.runs[eventID], nil
}
func (m *mockHiveRepo) GetResultByEventID(_ context.Context, eventID string) (*domain.HiveCIWorkflowResult, error) {
	return m.results[eventID], nil
}
func (m *mockHiveRepo) GetLatestResultByRunEventID(_ context.Context, runEventID string) (*domain.HiveCIWorkflowResult, error) {
	var latest *domain.HiveCIWorkflowResult
	for _, result := range m.results {
		if result.RunEventID == runEventID && (latest == nil || result.EventCreatedAt.After(latest.EventCreatedAt)) {
			latest = result
		}
	}
	return latest, nil
}
func (m *mockHiveRepo) ListPendingResults(_ context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (m *mockHiveRepo) ListOrphanedResultsByRun(_ context.Context, _ string) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (m *mockHiveRepo) UpdateResultState(_ context.Context, eventID string, newState domain.HiveCIProcessingState) error {
	m.updated[eventID] = newState
	return nil
}
func (m *mockHiveRepo) IncrementResultRetry(_ context.Context, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (m *mockHiveRepo) MarkResultFailed(_ context.Context, eventID, _ string) error {
	m.updated[eventID] = domain.HiveCIProcessingStateFailed
	return nil
}
func (m *mockHiveRepo) ListPolicies(_ context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return nil, nil
}
func (m *mockHiveRepo) GetPolicyByRepoAndWorkflow(_ context.Context, _, _ string) (*domain.HiveCIPipelinePolicy, error) {
	return m.policy, nil
}
func (m *mockHiveRepo) EnsurePipelinePolicy(_ context.Context, _ domain.HiveCIPipelinePolicy) error {
	return nil
}
func (m *mockHiveRepo) LookupRepositoryCI(_ context.Context, _ []string, _ bool) ([]domain.RepositoryCILookup, error) {
	return nil, nil
}

type mockBuildRepo struct {
	byRun map[string]*domain.Build
}

func newMockBuildRepo() *mockBuildRepo {
	return &mockBuildRepo{byRun: map[string]*domain.Build{}}
}

func (m *mockBuildRepo) Create(_ context.Context, b *domain.Build) error {
	cp := *b
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
		b.ID = cp.ID
	}
	m.byRun[b.CIRunID] = &cp
	return nil
}
func (m *mockBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	for _, build := range m.byRun {
		if build.ID == id {
			return build, nil
		}
	}
	return nil, nil
}
func (m *mockBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	if ciSystem != domain.CISystemHiveCI && ciSystem != domain.CISystemHiveCILegacy {
		return nil, nil
	}
	build := m.byRun[ciRunID]
	if build == nil || build.CISystem != ciSystem {
		return nil, nil
	}
	return build, nil
}
func (m *mockBuildRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Build, error) {
	return nil, nil
}
func (m *mockBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	for _, build := range m.byRun {
		if build.ID == id {
			build.Status = status
		}
	}
	return nil
}

type mockServiceRepo struct {
	service *domain.Service
}

func (m *mockServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	m.service = svc
	return nil
}
func (m *mockServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if m.service != nil && m.service.ID == id {
		return m.service, nil
	}
	return nil, nil
}
func (m *mockServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (m *mockServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (m *mockServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (m *mockServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (m *mockServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type mockArtifactRepo struct {
	byDigest map[string]*domain.Artifact
	created  int
}

func newMockArtifactRepo() *mockArtifactRepo {
	return &mockArtifactRepo{byDigest: map[string]*domain.Artifact{}}
}

func artifactKey(repo, digest string) string { return repo + "@" + digest }

func (m *mockArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	cp := *a
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
		a.ID = cp.ID
	}
	m.byDigest[artifactKey(cp.ImageRepo, cp.ImageDigest)] = &cp
	m.created++
	return nil
}
func (m *mockArtifactRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Artifact, error) {
	return nil, nil
}
func (m *mockArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	return m.byDigest[artifactKey(repo, digest)], nil
}
func (m *mockArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	return m.byDigest[artifactKey(imageRepo, imageDigest)], nil
}
func (m *mockArtifactRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Artifact, error) {
	return nil, nil
}
func (m *mockArtifactRepo) ListByBuild(_ context.Context, _ uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type mockOCIRepo struct {
	manifests map[string]*domain.OCIManifest
}

func newMockOCIRepo() *mockOCIRepo {
	return &mockOCIRepo{manifests: map[string]*domain.OCIManifest{}}
}

func (m *mockOCIRepo) EnsureRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return &domain.OCIRepository{}, nil
}
func (m *mockOCIRepo) GetRepository(_ context.Context, _ string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (m *mockOCIRepo) GetManifestByDigest(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (m *mockOCIRepo) GetManifestByTag(_ context.Context, _, _ string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (m *mockOCIRepo) PutManifest(_ context.Context, _ domain.OCIManifest, _ string) error {
	return nil
}
func (m *mockOCIRepo) GetBlob(_ context.Context, _ string) (*domain.OCIBlob, error) {
	return nil, nil
}
func (m *mockOCIRepo) BlobExistsInRepo(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (m *mockOCIRepo) FinalizeBlob(_ context.Context, _ domain.OCIBlobUpload) error {
	return nil
}
func (m *mockOCIRepo) LinkBlobToRepo(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockOCIRepo) UpsertBlob(_ context.Context, _, _, _, _ string, _ int64) error {
	return nil
}
func (m *mockOCIRepo) ListTags(_ context.Context, _, _ string, _ int) ([]string, error) {
	return nil, nil
}
func (m *mockOCIRepo) ListReferrers(_ context.Context, _, _, _ string) ([]domain.OCIReferrerDescriptor, error) {
	return nil, nil
}
func (m *mockOCIRepo) GetManifest(_ context.Context, repoName, reference string) (*domain.OCIManifest, error) {
	return m.manifests[artifactKey(repoName, reference)], nil
}

func seedRunResult(h *mockHiveRepo, status, runPublisher, resultPublisher string) {
	h.runs["run-1"] = &domain.HiveCIWorkflowRun{
		RunEventID:      "run-1",
		RepoCoordinate:  "github.com/acme/api",
		CommitSHA:       "abc123",
		Branch:          "main",
		WorkflowPath:    ".github/workflows/ci.yml",
		PublisherPubkey: runPublisher,
	}
	h.results["res-1"] = &domain.HiveCIWorkflowResult{
		ResultEventID:   "res-1",
		RunEventID:      "run-1",
		Status:          status,
		PublisherPubkey: resultPublisher,
		ImageRepo:       "ghcr.io/acme/api",
		ImageTag:        "main",
		ImageDigest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

type mockIntentRepo struct {
	byResultEventID map[string]*domain.DeploymentIntent
	created         int
}

func newMockIntentRepo() *mockIntentRepo {
	return &mockIntentRepo{byResultEventID: map[string]*domain.DeploymentIntent{}}
}

func (m *mockIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	cp := *di
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
		di.ID = cp.ID
	}
	if cp.Metadata != nil {
		if v, ok := cp.Metadata["hive_ci_result_event_id"].(string); ok && v != "" {
			m.byResultEventID[v] = &cp
		}
	}
	m.created++
	return nil
}
func (m *mockIntentRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (m *mockIntentRepo) GetByHiveResultEventID(_ context.Context, eventID string) (*domain.DeploymentIntent, error) {
	return m.byResultEventID[eventID], nil
}
func (m *mockIntentRepo) ListByServiceEnv(_ context.Context, _, _ uuid.UUID, _, _ int) ([]domain.DeploymentIntent, error) {
	return nil, nil
}
func (m *mockIntentRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.DeploymentIntentStatus) error {
	return nil
}
func (m *mockIntentRepo) UpdateApproval(_ context.Context, _ uuid.UUID, _ domain.ApprovalStatus) error {
	return nil
}
func (m *mockIntentRepo) UpdateDesiredState(context.Context, uuid.UUID, *domain.DesiredServiceSpec, string) error {
	return nil
}

type mockEnvRepo struct {
	byID   map[uuid.UUID]*domain.Environment
	byName map[string]*domain.Environment
}

func newMockEnvRepo() *mockEnvRepo {
	return &mockEnvRepo{byID: map[uuid.UUID]*domain.Environment{}, byName: map[string]*domain.Environment{}}
}

func (m *mockEnvRepo) Create(_ context.Context, _ *domain.Environment) error { return nil }
func (m *mockEnvRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return m.byID[id], nil
}
func (m *mockEnvRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	return m.byName[name], nil
}
func (m *mockEnvRepo) List(_ context.Context) ([]domain.Environment, error) { return nil, nil }
func (m *mockEnvRepo) ListByOrg(_ context.Context, _ uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (m *mockEnvRepo) Update(_ context.Context, _ *domain.Environment) error { return nil }
func (m *mockEnvRepo) Delete(_ context.Context, _ uuid.UUID) error           { return nil }

type mockRegistryInspector struct {
	inspections map[string]*registryadapter.ImageInspection
}

func (m *mockRegistryInspector) InspectImage(_ context.Context, repo, reference string) (*registryadapter.ImageInspection, error) {
	if m == nil {
		return nil, nil
	}
	return m.inspections[artifactKey(repo, reference)], nil
}
func (m *mockRegistryInspector) ListTags(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (m *mockRegistryInspector) GetReferrers(_ context.Context, _, _ string) ([]registryadapter.Referrer, error) {
	return nil, nil
}

type mockCanonicalRegistry struct {
	builds    *mockBuildRepo
	artifacts *mockArtifactRepo
	lastProof service.ArtifactVerificationProof
}

func (m *mockCanonicalRegistry) RegisterBuild(ctx context.Context, build *domain.Build) error {
	return m.builds.Create(ctx, build)
}
func (m *mockCanonicalRegistry) UpdateBuildStatus(ctx context.Context, id uuid.UUID, status domain.BuildStatus) error {
	return m.builds.UpdateStatus(ctx, id, status)
}
func (m *mockCanonicalRegistry) RegisterVerifiedArtifact(ctx context.Context, artifact *domain.Artifact, proof service.ArtifactVerificationProof) error {
	m.lastProof = proof
	if existing := m.artifacts.byDigest[artifactKey(artifact.ImageRepo, artifact.ImageDigest)]; existing != nil {
		if existing.BuildID != artifact.BuildID || existing.ServiceID != artifact.ServiceID || existing.ImageTag != artifact.ImageTag {
			return context.Canceled
		}
		*artifact = *existing
		return nil
	}
	artifact.ManifestMediaType = proof.MediaType
	artifact.Metadata["verification"] = map[string]any{"source": proof.Source, "manifest_digest": proof.ManifestDigest, "state": "verified"}
	artifact.Metadata["supply_chain"] = map[string]any{"policy_state": proof.PolicyState}
	return m.artifacts.Create(ctx, artifact)
}

func (m *mockCanonicalRegistry) RegisterReleaseArtifact(ctx context.Context, artifact *domain.Artifact, proof service.ReleaseArtifactVerificationProof) error {
	if artifact.ImageTag != "" {
		return context.Canceled
	}
	if existing := m.artifacts.byDigest[artifactKey(artifact.ImageRepo, artifact.ImageDigest)]; existing != nil {
		*artifact = *existing
		return nil
	}
	artifact.Metadata = map[string]any{
		"registration_mode": "hiveci_release_digest",
		"signed_image_tag":  proof.Release.Result.ImageTag,
		"lineage":           proof.Release.Result.Lineage,
	}
	return m.artifacts.Create(ctx, artifact)
}

func newBridgeForTest(h *mockHiveRepo, b *mockBuildRepo, a *mockArtifactRepo, i *mockIntentRepo, e *mockEnvRepo, o *mockOCIRepo) *Bridge {
	serviceID := uuid.Nil
	imageRepo := ""
	if h.policy != nil {
		serviceID = h.policy.ServiceID
	}
	if result := h.results["res-1"]; result != nil {
		imageRepo = result.ImageRepo
	}
	services := &mockServiceRepo{service: &domain.Service{ID: serviceID, ArtifactRepo: imageRepo}}
	registry := &mockCanonicalRegistry{builds: b, artifacts: a}
	return NewBridge(h, services, b, a, i, e, o, nil, registry, []string{"trusted-pub"}, true, nil)
}

func TestBridge_SuccessWithImagePresentCreatesArtifact(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 1 {
		t.Fatalf("expected one artifact created, got %d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateProcessed {
		t.Fatalf("expected result state processed, got %q", h.updated["res-1"])
	}
}

func TestBridge_TrustsDistinctRunAndResultPublishers(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-trigger", "trusted-worker")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	services := &mockServiceRepo{service: &domain.Service{ID: h.policy.ServiceID, ArtifactRepo: "ghcr.io/acme/api"}}
	registry := &mockCanonicalRegistry{builds: b, artifacts: a}
	bridge := NewBridge(h, services, b, a, newMockIntentRepo(), newMockEnvRepo(), o, nil, registry, []string{"trusted-trigger", "trusted-worker"}, true, nil)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 1 {
		t.Fatalf("expected one artifact created, got %d", a.created)
	}
}

func TestBridge_SuccessWithImageMissingMarksArtifactPending(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 0 {
		t.Fatalf("expected no artifact created, got %d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateArtifactPending {
		t.Fatalf("expected artifact_pending state, got %q", h.updated["res-1"])
	}
}

func TestBridge_SuccessWithHarborInspectorFallbackCreatesArtifact(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	h.results["res-1"].ImageRepo = "harbor.sharegap.net/cascadia/ddgs"
	h.results["res-1"].ImageTag = "pilot-v1"
	h.results["res-1"].ImageDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	inspector := &mockRegistryInspector{inspections: map[string]*registryadapter.ImageInspection{
		artifactKey("cascadia/ddgs", "pilot-v1"): {
			Exists:    true,
			Digest:    "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			MediaType: "application/vnd.docker.distribution.manifest.v2+json",
			Size:      456,
		},
	}}
	services := &mockServiceRepo{service: &domain.Service{ID: h.policy.ServiceID, ArtifactRepo: h.results["res-1"].ImageRepo}}
	registry := &mockCanonicalRegistry{builds: b, artifacts: a}
	bridge := NewBridge(h, services, b, a, newMockIntentRepo(), newMockEnvRepo(), nil, inspector, registry, []string{"trusted-pub"}, true, nil)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 1 {
		t.Fatalf("expected one artifact created via harbor inspector fallback, got %d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateProcessed {
		t.Fatalf("expected result state processed, got %q", h.updated["res-1"])
	}
	if registry.lastProof.ManifestDigest != h.results["res-1"].ImageDigest || registry.lastProof.Source != "registry_manifest" {
		t.Fatalf("expected registry manifest provenance, got %+v", registry.lastProof)
	}
}

func TestBridge_FailureSkipsArtifactRegistration(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "failure", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 0 {
		t.Fatalf("expected no artifact created on failure, got %d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateProcessed {
		t.Fatalf("expected processed state, got %q", h.updated["res-1"])
	}
}

func TestBridge_DuplicateArtifactReused(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	build := &domain.Build{
		ID: uuid.New(), ServiceID: h.policy.ServiceID, GitSHA: "abc123",
		CISystem: domain.CISystemHiveCI, CIRunID: "run-1", Status: domain.BuildStatusSucceeded,
	}
	b.byRun["run-1"] = build
	a := newMockArtifactRepo()
	a.byDigest[artifactKey("ghcr.io/acme/api", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")] = &domain.Artifact{
		ID: uuid.New(), BuildID: build.ID, ServiceID: build.ServiceID,
		ImageRepo: "ghcr.io/acme/api", ImageTag: "main",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 0 {
		t.Fatalf("expected existing artifact reuse, created=%d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateProcessed {
		t.Fatalf("expected processed state, got %q", h.updated["res-1"])
	}
}

func TestBridge_CISuccessNeverCreatesStagingIntent(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	envID := uuid.New()
	h.policy = &domain.HiveCIPipelinePolicy{
		ServiceID:     uuid.New(),
		EnvironmentID: envID,
		Metadata: map[string]any{
			"auto_deploy_staging": true,
			"staging_environment": "edge-01-staging",
		},
	}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	i := newMockIntentRepo()
	e := newMockEnvRepo()
	e.byName["edge-01-staging"] = &domain.Environment{ID: envID, Name: "edge-01-staging", Protected: false}
	bridge := newBridgeForTest(h, b, a, i, e, o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if i.created != 0 || len(i.byResultEventID) != 0 {
		t.Fatalf("CI success mutated promotion authority: created=%d intents=%d", i.created, len(i.byResultEventID))
	}
}

func TestBridge_CISuccessNeverCreatesProtectedEnvironmentIntent(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	envID := uuid.New()
	h.policy = &domain.HiveCIPipelinePolicy{
		ServiceID:     uuid.New(),
		EnvironmentID: envID,
		Metadata:      map[string]any{"auto_deploy_staging": true},
	}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	i := newMockIntentRepo()
	e := newMockEnvRepo()
	e.byID[envID] = &domain.Environment{ID: envID, Name: "edge-01-staging", Protected: true}
	bridge := newBridgeForTest(h, b, a, i, e, o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if i.created != 0 || len(i.byResultEventID) != 0 {
		t.Fatalf("CI success mutated protected environment intent state: created=%d intents=%d", i.created, len(i.byResultEventID))
	}
}

func TestBridge_DuplicateIntentReused(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	envID := uuid.New()
	h.policy = &domain.HiveCIPipelinePolicy{
		ServiceID:     uuid.New(),
		EnvironmentID: envID,
		Metadata:      map[string]any{"auto_deploy_staging": true},
	}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	i := newMockIntentRepo()
	i.byResultEventID["res-1"] = &domain.DeploymentIntent{ID: uuid.New()}
	e := newMockEnvRepo()
	e.byID[envID] = &domain.Environment{ID: envID, Name: "edge-01-staging"}
	bridge := newBridgeForTest(h, b, a, i, e, o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if i.created != 0 {
		t.Fatalf("expected duplicate intent reuse, created=%d", i.created)
	}
}

func TestBridge_CorrelatesLegacyBrowserBuildWithoutDuplication(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	legacy := &domain.Build{
		ID: uuid.New(), ServiceID: h.policy.ServiceID, GitSHA: "abc123",
		CISystem: domain.CISystemHiveCILegacy, CIRunID: "run-1", Status: domain.BuildStatusQueued,
	}
	b.byRun[legacy.CIRunID] = legacy
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest: h.results["res-1"].ImageDigest, MediaType: "application/vnd.oci.image.manifest.v1+json",
	}
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult legacy correlation: %v", err)
	}
	if len(b.byRun) != 1 || b.byRun["run-1"].ID != legacy.ID || b.byRun["run-1"].Status != domain.BuildStatusSucceeded {
		t.Fatalf("legacy browser build did not converge: %#v", b.byRun)
	}
}

func TestBridge_RegisterBuildResultDeduplicatesVerifiedArtifact(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	build := &domain.Build{
		ID: uuid.New(), ServiceID: h.policy.ServiceID, GitSHA: "abc123",
		CISystem: domain.CISystemHiveCI, CIRunID: "run-1", Status: domain.BuildStatusSucceeded,
	}
	b.byRun[build.CIRunID] = build
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest: h.results["res-1"].ImageDigest, MediaType: "application/vnd.oci.image.manifest.v1+json",
	}
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	first, err := bridge.RegisterBuildResult(context.Background(), build.ID)
	if err != nil {
		t.Fatalf("RegisterBuildResult first call: %v", err)
	}
	second, err := bridge.RegisterBuildResult(context.Background(), build.ID)
	if err != nil {
		t.Fatalf("RegisterBuildResult duplicate call: %v", err)
	}
	if a.created != 1 {
		t.Fatalf("expected one deduplicated artifact, got %d", a.created)
	}
	if first == nil || second == nil || first.ID != second.ID {
		t.Fatalf("expected duplicate action to return the same artifact")
	}
}

func TestBridge_RejectsMutableOnlyBuildResult(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.results["res-1"].ImageDigest = "latest"
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	build := &domain.Build{
		ID: uuid.New(), ServiceID: h.policy.ServiceID, GitSHA: "abc123",
		CISystem: domain.CISystemHiveCI, CIRunID: "run-1", Status: domain.BuildStatusSucceeded,
	}
	b.byRun[build.CIRunID] = build
	a := newMockArtifactRepo()
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), newMockOCIRepo())

	if _, err := bridge.RegisterBuildResult(context.Background(), build.ID); err == nil {
		t.Fatal("expected mutable-only build result to be refused")
	}
	if a.created != 0 || h.updated["res-1"] != domain.HiveCIProcessingStateRejected {
		t.Fatalf("mutable-only result created artifact or was not rejected")
	}
}

func TestBridge_RejectsTagDigestMismatch(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
	}
	bridge := newBridgeForTest(h, b, a, newMockIntentRepo(), newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err == nil {
		t.Fatal("expected tag-to-manifest digest mismatch to be refused")
	}
	if a.created != 0 {
		t.Fatalf("expected no artifact for mismatched tag/digest, got %d", a.created)
	}
}

func TestBridge_NoAutoDeployPolicySkipped(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{
		ServiceID:     uuid.New(),
		EnvironmentID: uuid.New(),
		Metadata:      map[string]any{"auto_deploy_staging": false},
	}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "main")] = &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		SizeBytes: 123,
	}
	i := newMockIntentRepo()
	bridge := newBridgeForTest(h, b, a, i, newMockEnvRepo(), o)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if i.created != 0 {
		t.Fatalf("expected no intent for disabled auto-deploy, created=%d", i.created)
	}
}

type recordingReleaseAuditor struct {
	decisions []string
}

func (a *recordingReleaseAuditor) AuditReleaseRegistration(_ context.Context, _ domain.HiveCIAcceptedRelease, _ *domain.Artifact, decision string, _ error) error {
	a.decisions = append(a.decisions, decision)
	return nil
}

func TestBridge_RegisterAcceptedReleaseIsExactlyOnceDigestOnlyAndDoesNotPromote(t *testing.T) {
	serviceID := uuid.New()
	policyID := uuid.New()
	repositoryName := "harbor.example/team/bahia"
	release := domain.HiveCIAcceptedRelease{
		Result: domain.HiveCIReleaseResult{
			ReleaseIdentity: domain.HiveCIReleaseIdentityPrefix + "identity",
			ImageTag:        "release-latest",
			Manifest:        domain.HiveCIReleaseArtifact{Repository: repositoryName, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			SBOM:            domain.HiveCIReleaseArtifact{Repository: repositoryName, Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			Provenance:      domain.HiveCIReleaseArtifact{Repository: repositoryName, Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
			Lineage: domain.HiveCIReleaseLineage{
				WorkflowRunEventID: "run-release", RepoAddress: "30617:publisher:bahia",
				Commit: "0123456789012345678901234567890123456789",
			},
		},
		ResultEventID: "release-result", Workflow: ".gitea/workflows/release.yml", Branch: "main",
		PolicyID: policyID.String(),
		Policy: domain.HiveCIPipelinePolicy{
			ID: policyID, ServiceID: serviceID, RepoCoordinate: "30617:publisher:bahia",
			WorkflowPath: ".gitea/workflows/release.yml",
		},
	}
	builds := newMockBuildRepo()
	artifacts := newMockArtifactRepo()
	intents := newMockIntentRepo()
	registry := &mockCanonicalRegistry{builds: builds, artifacts: artifacts}
	bridge := NewBridge(newMockHiveRepo(), &mockServiceRepo{service: &domain.Service{ID: serviceID, ArtifactRepo: repositoryName}},
		builds, artifacts, intents, newMockEnvRepo(), nil, nil, registry, nil, true, nil)
	auditor := &recordingReleaseAuditor{}
	bridge.SetReleaseRegistrationAuditor(auditor)

	first, err := bridge.RegisterAcceptedRelease(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.RegisterAcceptedRelease(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.created != 1 || first.ID != second.ID {
		t.Fatalf("registration was not exactly once: created=%d first=%s second=%s", artifacts.created, first.ID, second.ID)
	}
	if first.ImageTag != "" || first.ImageDigest != release.Result.Manifest.Digest {
		t.Fatalf("artifact is not digest-only: %+v", first)
	}
	if got := first.Metadata["signed_image_tag"]; got != release.Result.ImageTag {
		t.Fatalf("signed tag evidence=%v", got)
	}
	if intents.created != 0 || len(intents.byResultEventID) != 0 {
		t.Fatalf("registration created a promotion intent")
	}
	if len(auditor.decisions) != 2 || auditor.decisions[0] != "accepted" || auditor.decisions[1] != "accepted" {
		t.Fatalf("audit decisions=%v", auditor.decisions)
	}
}
