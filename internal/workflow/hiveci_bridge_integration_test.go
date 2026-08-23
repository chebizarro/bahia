package workflow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/pipeline"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type hiveCIBridgeRepo struct {
	run    *domain.HiveCIWorkflowRun
	result *domain.HiveCIWorkflowResult
	policy *domain.HiveCIPipelinePolicy
}

func (r *hiveCIBridgeRepo) UpsertWorkflowRun(context.Context, domain.HiveCIWorkflowRun) error {
	return nil
}
func (r *hiveCIBridgeRepo) UpsertWorkflowResult(context.Context, domain.HiveCIWorkflowResult) error {
	return nil
}
func (r *hiveCIBridgeRepo) GetRunByEventID(context.Context, string) (*domain.HiveCIWorkflowRun, error) {
	return r.run, nil
}
func (r *hiveCIBridgeRepo) GetResultByEventID(context.Context, string) (*domain.HiveCIWorkflowResult, error) {
	return r.result, nil
}
func (r *hiveCIBridgeRepo) GetLatestResultByRunEventID(context.Context, string) (*domain.HiveCIWorkflowResult, error) {
	return r.result, nil
}
func (r *hiveCIBridgeRepo) ListPendingResults(context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (r *hiveCIBridgeRepo) ListOrphanedResultsByRun(context.Context, string) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (r *hiveCIBridgeRepo) UpdateResultState(context.Context, string, domain.HiveCIProcessingState) error {
	return nil
}
func (r *hiveCIBridgeRepo) IncrementResultRetry(context.Context, string, time.Time) (int, error) {
	return 0, nil
}
func (r *hiveCIBridgeRepo) MarkResultFailed(context.Context, string, string) error { return nil }
func (r *hiveCIBridgeRepo) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return nil, nil
}
func (r *hiveCIBridgeRepo) GetPolicyByRepoAndWorkflow(context.Context, string, string) (*domain.HiveCIPipelinePolicy, error) {
	return r.policy, nil
}
func (r *hiveCIBridgeRepo) EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error {
	return nil
}
func (r *hiveCIBridgeRepo) LookupRepositoryCI(context.Context, []string, bool) ([]domain.RepositoryCILookup, error) {
	return nil, nil
}

type hiveCIBridgeOCIRepo struct {
	manifest *domain.OCIManifest
}

func (r *hiveCIBridgeOCIRepo) EnsureRepository(context.Context, string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) GetRepository(context.Context, string) (*domain.OCIRepository, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) GetManifestByDigest(context.Context, string, string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) GetManifestByTag(context.Context, string, string) (*domain.OCIManifest, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) PutManifest(context.Context, domain.OCIManifest, string) error {
	return nil
}
func (r *hiveCIBridgeOCIRepo) GetBlob(context.Context, string) (*domain.OCIBlob, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) BlobExistsInRepo(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *hiveCIBridgeOCIRepo) FinalizeBlob(context.Context, domain.OCIBlobUpload) error {
	return nil
}
func (r *hiveCIBridgeOCIRepo) LinkBlobToRepo(context.Context, string, string) error { return nil }
func (r *hiveCIBridgeOCIRepo) UpsertBlob(context.Context, string, string, string, string, int64) error {
	return nil
}
func (r *hiveCIBridgeOCIRepo) ListTags(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) ListReferrers(context.Context, string, string, string) ([]domain.OCIReferrerDescriptor, error) {
	return nil, nil
}
func (r *hiveCIBridgeOCIRepo) GetManifest(context.Context, string, string) (*domain.OCIManifest, error) {
	return r.manifest, nil
}

type hiveCIBridgeFixture struct {
	bridge      *pipeline.Bridge
	registry    *service.RegistryService
	intents     *stubIntentRepo
	runs        *stubRunRepo
	state       *stubStateRepo
	service     *domain.Service
	environment *domain.Environment
}

func newHiveCIBridgeFixture(t *testing.T, publisher events.Publisher, protected bool) *hiveCIBridgeFixture {
	t.Helper()
	serviceRepo := &stubServiceRepo{}
	envRepo := &stubEnvRepo{}
	buildRepo := &stubBuildRepo{}
	artifactRepo := &stubArtifactRepo{}
	intentRepo := newStubIntentRepo()
	runRepo := newStubRunRepo()
	stateRepo := newStubStateRepo()
	svc := &domain.Service{Name: "api", ArtifactRepo: "ghcr.io/acme/api"}
	if err := serviceRepo.Create(t.Context(), svc); err != nil {
		t.Fatal(err)
	}
	env := &domain.Environment{Name: "staging", Protected: protected, DeployStrategy: domain.DeployStrategyReplace}
	if err := envRepo.Create(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	registry := service.NewRegistryService(
		serviceRepo, envRepo, buildRepo, artifactRepo, intentRepo, runRepo,
		&stubObsRepo{}, stateRepo, nil, publisher, zap.NewNop(),
	)
	hiveRepo := &hiveCIBridgeRepo{
		run: &domain.HiveCIWorkflowRun{
			RunEventID: "run-1", RepoCoordinate: "github.com/acme/api", CommitSHA: "abc123",
			Branch: "main", WorkflowPath: ".github/workflows/ci.yml", PublisherPubkey: "trusted-ci",
		},
		result: &domain.HiveCIWorkflowResult{
			ResultEventID: "result-1", RunEventID: "run-1", Status: "success", PublisherPubkey: "trusted-ci",
			ImageRepo: "ghcr.io/acme/api", ImageTag: "main",
			ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		policy: &domain.HiveCIPipelinePolicy{
			ID: uuid.New(), ServiceID: svc.ID, EnvironmentID: env.ID,
			Metadata: map[string]any{"auto_deploy_staging": true, "staging_environment": env.Name},
		},
	}
	ociRepo := &hiveCIBridgeOCIRepo{manifest: &domain.OCIManifest{
		Digest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MediaType: "application/vnd.oci.image.manifest.v1+json", SizeBytes: 123,
	}}
	return &hiveCIBridgeFixture{
		bridge: pipeline.NewBridge(
			hiveRepo, serviceRepo, buildRepo, artifactRepo, intentRepo, envRepo, ociRepo, nil,
			registry, []string{"trusted-ci"}, true, zap.NewNop(),
		),
		registry: registry, intents: intentRepo, runs: runRepo, state: stateRepo,
		service: svc, environment: env,
	}
}

func subscribeOnce(publisher events.Publisher, eventType events.EventType, count *atomic.Int32) <-chan struct{} {
	delivered := make(chan struct{})
	var once sync.Once
	publisher.Subscribe(eventType, func(context.Context, events.Event) {
		count.Add(1)
		once.Do(func() { close(delivered) })
	})
	return delivered
}

func TestHiveCIAutoDeployExecutesInProcessAndDeduplicates(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	fixture := newHiveCIBridgeFixture(t, publisher, false)
	coordinator := NewCoordinator(fixture.registry, nil, publisher, zap.NewNop())
	awaitStarted := make(chan struct{})
	coordinator.loom = &stubLoomClient{awaitFn: func(context.Context, string, string) (*loom.JobStatus, error) {
		close(awaitStarted)
		return &loom.JobStatus{Status: loom.StatusCompleted}, nil
	}}
	coordinator.SetupEventHandlers(publisher)
	defer coordinator.Shutdown(time.Second)

	var createdEvents, approvedEvents, runEvents atomic.Int32
	created := subscribeOnce(publisher, events.EventDeploymentIntentCreated, &createdEvents)
	approved := subscribeOnce(publisher, events.EventDeploymentIntentApproved, &approvedEvents)
	runCreated := subscribeOnce(publisher, events.EventDeploymentRunCreated, &runEvents)

	if err := fixture.bridge.ProcessResult(t.Context(), "result-1"); err != nil {
		t.Fatalf("ProcessResult() error = %v", err)
	}
	<-created
	<-approved
	<-runCreated
	<-awaitStarted
	coordinator.wg.Wait()

	intent, err := fixture.intents.GetByHiveResultEventID(t.Context(), "result-1")
	if err != nil || intent == nil {
		t.Fatalf("event-triggered intent = %v, error = %v", intent, err)
	}
	state, err := fixture.state.Get(t.Context(), fixture.service.ID, fixture.environment.ID)
	if err != nil || state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		t.Fatalf("desired state did not advance to intent %s: state=%+v error=%v", intent.ID, state, err)
	}
	runs, err := fixture.runs.ListByIntent(t.Context(), intent.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("deployment runs = %d, error = %v", len(runs), err)
	}

	if err := fixture.bridge.ProcessResult(t.Context(), "result-1"); err != nil {
		t.Fatalf("duplicate ProcessResult() error = %v", err)
	}
	runs, err = fixture.runs.ListByIntent(t.Context(), intent.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("duplicate processing produced runs = %d, error = %v", len(runs), err)
	}
	if len(fixture.intents.intents) != 1 || createdEvents.Load() != 1 || approvedEvents.Load() != 1 || runEvents.Load() != 1 {
		t.Fatalf("duplicate processing was not idempotent: intents=%d created=%d approved=%d runs=%d",
			len(fixture.intents.intents), createdEvents.Load(), approvedEvents.Load(), runEvents.Load())
	}
}

func TestHiveCIAutoDeployProtectedEnvironmentDoesNotExecute(t *testing.T) {
	publisher := events.NewInProcessPublisher(zap.NewNop())
	fixture := newHiveCIBridgeFixture(t, publisher, true)
	coordinator := NewCoordinator(fixture.registry, nil, publisher, zap.NewNop())
	coordinator.SetupEventHandlers(publisher)
	defer coordinator.Shutdown(time.Second)

	var createdEvents, approvedEvents, runEvents atomic.Int32
	created := subscribeOnce(publisher, events.EventDeploymentIntentCreated, &createdEvents)
	publisher.Subscribe(events.EventDeploymentIntentApproved, func(context.Context, events.Event) { approvedEvents.Add(1) })
	publisher.Subscribe(events.EventDeploymentRunCreated, func(context.Context, events.Event) { runEvents.Add(1) })

	if err := fixture.bridge.ProcessResult(t.Context(), "result-1"); err != nil {
		t.Fatalf("ProcessResult() error = %v", err)
	}
	<-created
	intent, err := fixture.intents.GetByHiveResultEventID(t.Context(), "result-1")
	if err != nil || intent == nil {
		t.Fatalf("protected intent = %v, error = %v", intent, err)
	}
	if intent.ApprovalStatus != domain.ApprovalStatusPending || intent.Status != domain.IntentStatusPending {
		t.Fatalf("protected intent approval=%q status=%q", intent.ApprovalStatus, intent.Status)
	}
	if len(fixture.runs.runs) != 0 || approvedEvents.Load() != 0 || runEvents.Load() != 0 {
		t.Fatalf("protected policy auto-executed: approved=%d run_events=%d runs=%d",
			approvedEvents.Load(), runEvents.Load(), len(fixture.runs.runs))
	}
}
