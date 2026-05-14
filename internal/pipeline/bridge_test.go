package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	registryadapter "github.com/openagentsinc/bahia/internal/adapters/registry"
	"github.com/openagentsinc/bahia/internal/domain"
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
func (m *mockBuildRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Build, error) {
	return nil, nil
}
func (m *mockBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	if ciSystem != ciSystemHiveCI {
		return nil, nil
	}
	return m.byRun[ciRunID], nil
}
func (m *mockBuildRepo) ListByService(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.Build, error) {
	return nil, nil
}
func (m *mockBuildRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.BuildStatus) error {
	return nil
}

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
		ImageDigest:     "sha256:abc",
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

func newBridgeForTest(h *mockHiveRepo, b *mockBuildRepo, a *mockArtifactRepo, i *mockIntentRepo, e *mockEnvRepo, o *mockOCIRepo) *Bridge {
	return NewBridge(h, b, a, i, e, o, nil, []string{"trusted-pub"}, nil)
}

func TestBridge_SuccessWithImagePresentCreatesArtifact(t *testing.T) {
	h := newMockHiveRepo()
	seedRunResult(h, "success", "trusted-pub", "trusted-pub")
	h.policy = &domain.HiveCIPipelinePolicy{ServiceID: uuid.New()}
	b := newMockBuildRepo()
	a := newMockArtifactRepo()
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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
	h.results["res-1"].ImageDigest = "sha256:def"
	inspector := &mockRegistryInspector{inspections: map[string]*registryadapter.ImageInspection{
		artifactKey("cascadia/ddgs", "sha256:def"): {
			Exists:    true,
			Digest:    "sha256:def",
			MediaType: "application/vnd.docker.distribution.manifest.v2+json",
			Size:      456,
		},
	}}
	bridge := NewBridge(h, b, a, newMockIntentRepo(), newMockEnvRepo(), nil, inspector, []string{"trusted-pub"}, nil)

	if err := bridge.ProcessResult(context.Background(), "res-1"); err != nil {
		t.Fatalf("ProcessResult error: %v", err)
	}
	if a.created != 1 {
		t.Fatalf("expected one artifact created via harbor inspector fallback, got %d", a.created)
	}
	if h.updated["res-1"] != domain.HiveCIProcessingStateProcessed {
		t.Fatalf("expected result state processed, got %q", h.updated["res-1"])
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
	a := newMockArtifactRepo()
	a.byDigest[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.Artifact{ID: uuid.New(), ImageRepo: "ghcr.io/acme/api", ImageDigest: "sha256:abc"}
	o := newMockOCIRepo()
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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

func TestBridge_StagingAutoDeployCreatesIntent(t *testing.T) {
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
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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
	if i.created != 1 {
		t.Fatalf("expected one intent created, got %d", i.created)
	}
	intent := i.byResultEventID["res-1"]
	if intent == nil {
		t.Fatalf("expected intent keyed by hive result event id")
	}
	if intent.ApprovalStatus != domain.ApprovalStatusNotRequired {
		t.Fatalf("expected not_required approval, got %q", intent.ApprovalStatus)
	}
	if intent.Status != domain.IntentStatusApproved {
		t.Fatalf("expected approved intent status, got %q", intent.Status)
	}
	if intent.SourceKind != domain.SourceKindEventTriggered {
		t.Fatalf("expected source kind event_triggered, got %q", intent.SourceKind)
	}
	if intent.RequestedBy != "hive-ci-bridge" {
		t.Fatalf("expected requested_by hive-ci-bridge, got %q", intent.RequestedBy)
	}
}

func TestBridge_ProtectedEnvCreatesPendingIntent(t *testing.T) {
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
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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
	intent := i.byResultEventID["res-1"]
	if intent == nil {
		t.Fatalf("expected intent")
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending {
		t.Fatalf("expected pending approval, got %q", intent.ApprovalStatus)
	}
	if intent.Status != domain.IntentStatusPending {
		t.Fatalf("expected pending intent status, got %q", intent.Status)
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
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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
	o.manifests[artifactKey("ghcr.io/acme/api", "sha256:abc")] = &domain.OCIManifest{
		Digest:    "sha256:abc",
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
