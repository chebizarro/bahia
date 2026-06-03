package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:abc"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}

	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		runRepo,
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	policyService := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	desired := &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example.com/api@sha256:abc"}
	desired.ComputeDesiredHash()
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		desiredState: desired,
		deployResp:   &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"},
	}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{"operator"}}, capture, registry, policyService, runtimeStub)

	request := &nostr.Event{
		ID:      "deploy-request",
		PubKey:  "operator",
		Kind:    KindDeployRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, environmentID, artifactID),
	}

	reactor.handleDeployRequest(ctx, request)

	if !runtimeStub.deployCalled {
		t.Fatal("5961 deploy request did not invoke RuntimeLifecycleService.DeployWithStatus")
	}
	if runtimeStub.deployServiceID != serviceID || runtimeStub.deployEnvID != environmentID || runtimeStub.deployArtifact == nil || *runtimeStub.deployArtifact != artifactID {
		t.Fatalf("runtime deploy call mismatch: %#v", runtimeStub)
	}
	if got := len(intentRepo.intents); got != 1 {
		t.Fatalf("deployment intents created = %d, want 1", got)
	}
	var persisted *domain.DeploymentIntent
	for _, intent := range intentRepo.intents {
		persisted = intent
	}
	if persisted.DesiredState == nil || persisted.DesiredHash != desired.DesiredHash {
		t.Fatalf("persisted desired state/hash mismatch: state=%#v hash=%q want %q", persisted.DesiredState, persisted.DesiredHash, desired.DesiredHash)
	}
	if got := len(runRepo.runs); got != 1 {
		t.Fatalf("deployment runs created = %d, want 1", got)
	}
	for _, deploymentRun := range runRepo.runs {
		if deploymentRun.ApplyMetadata["desired_hash"] != desired.DesiredHash {
			t.Fatalf("run apply desired_hash = %#v, want %q", deploymentRun.ApplyMetadata["desired_hash"], desired.DesiredHash)
		}
		if deploymentRun.Status != domain.RunStatusSucceeded {
			t.Fatalf("run status = %q, want %q", deploymentRun.Status, domain.RunStatusSucceeded)
		}
	}
	if len(capture.events) == 0 || capture.events[len(capture.events)-1].Kind != KindContextVMMessage {
		t.Fatalf("expected final ContextVM deployment result, got %#v", capture.events)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, capture.events[len(capture.events)-1].Tags, "desired_hash", desired.DesiredHash)
}

func TestHandleDeployRequestRejectsPolicyBlockedRequest(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: true}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:blocked"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}

	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		&testDeploymentRunRepo{},
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)

	policyRepo := &testPolicyRepo{
		globalPolicies: []domain.DeploymentPolicy{{
			ID:          uuid.New(),
			Name:        "require-sig",
			Enforcement: domain.PolicyEnforcementBlock,
			Enabled:     true,
			Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
		}},
	}
	policyService := service.NewPolicyService(policyRepo, &testSignatureRepo{hasVerifiedSignature: false}, &testSBOMRepo{}, zap.NewNop())
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{"operator"}}, capture, registry, policyService)

	request := &nostr.Event{
		ID:      "deploy-request",
		PubKey:  "operator",
		Kind:    KindDeployRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, environmentID, artifactID),
	}

	reactor.handleDeployRequest(ctx, request)

	if got := len(intentRepo.intents); got != 0 {
		t.Fatalf("deployment intents created = %d, want 0", got)
	}
	if stateRepo.upserts != 0 {
		t.Fatalf("state upserts = %d, want 0", stateRepo.upserts)
	}
	if got := len(capture.events); got != 1 {
		t.Fatalf("published events = %d, want 1", got)
	}

	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID)
	assertReactorTag(t, result.Tags, "p", request.PubKey)
	assertReactorTag(t, result.Tags, "status", "error")
	assertReactorTag(t, result.Tags, "step", "policy_blocked")
	assertReactorTag(t, result.Tags, "service", serviceID.String())
	assertReactorTag(t, result.Tags, "environment", environmentID.String())
	assertReactorTag(t, result.Tags, "artifact", artifactID.String())
	assertSignedEvent(t, result)

	var response ContextVMJSONRPCResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("decode policy-blocked ContextVM response: %v", err)
	}
	if response.Error == nil || response.Error.Message == "" {
		t.Fatalf("expected policy-blocked ContextVM error, got %#v", response)
	}
}

func newDeployRequestTestReactor(t *testing.T, cfg Config, capture *captureNostrPublisher, registry *service.RegistryService, policyService *service.PolicyService, runtimeLifecycle ...RuntimeLifecycleOperatorService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	opts := []ReactorOption{WithControlPlanePublisher(capture), WithPolicyService(policyService)}
	if len(runtimeLifecycle) > 0 {
		opts = append(opts, WithRuntimeLifecycleService(runtimeLifecycle[0]))
	}
	return NewReactor(cfg, registry, nil, signer, zap.NewNop(), opts...)
}

type testServiceRepo struct {
	service *domain.Service
}

func (r *testServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *testServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if r.service != nil && r.service.ID == id {
		cp := *r.service
		return &cp, nil
	}
	return nil, nil
}
func (r *testServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *testServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *testServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *testServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *testServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type testEnvironmentRepo struct {
	environment *domain.Environment
}

func (r *testEnvironmentRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *testEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if r.environment != nil && r.environment.ID == id {
		cp := *r.environment
		return &cp, nil
	}
	return nil, nil
}
func (r *testEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r *testEnvironmentRepo) List(context.Context) ([]domain.Environment, error) { return nil, nil }
func (r *testEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *testEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *testEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type testBuildRepo struct{}

func (r *testBuildRepo) Create(context.Context, *domain.Build) error { return nil }
func (r *testBuildRepo) GetByID(context.Context, uuid.UUID) (*domain.Build, error) {
	return nil, nil
}
func (r *testBuildRepo) GetByCISystemRunID(context.Context, string, string) (*domain.Build, error) {
	return nil, nil
}
func (r *testBuildRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Build, error) {
	return nil, nil
}
func (r *testBuildRepo) UpdateStatus(context.Context, uuid.UUID, domain.BuildStatus) error {
	return nil
}

type testArtifactRepo struct {
	artifact *domain.Artifact
}

func (r *testArtifactRepo) Create(context.Context, *domain.Artifact) error { return nil }
func (r *testArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if r.artifact != nil && r.artifact.ID == id {
		cp := *r.artifact
		return &cp, nil
	}
	return nil, nil
}
func (r *testArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type testDeploymentIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
}

func (r *testDeploymentIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	cp := *di
	r.intents[cp.ID] = &cp
	return nil
}
func (r *testDeploymentIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := r.intents[id]
	if !ok {
		return nil, nil
	}
	cp := *intent
	return &cp, nil
}
func (r *testDeploymentIntentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *testDeploymentIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, _, _ int) ([]domain.DeploymentIntent, error) {
	out := make([]domain.DeploymentIntent, 0, len(r.intents))
	for _, intent := range r.intents {
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			out = append(out, *intent)
		}
	}
	return out, nil
}
func (r *testDeploymentIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	if intent, ok := r.intents[id]; ok {
		intent.Status = status
	}
	return nil
}
func (r *testDeploymentIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	if intent, ok := r.intents[id]; ok {
		intent.ApprovalStatus = status
	}
	return nil
}

type testDeploymentRunRepo struct {
	runs map[uuid.UUID]*domain.DeploymentRun
}

func (r *testDeploymentRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	if r.runs == nil {
		r.runs = map[uuid.UUID]*domain.DeploymentRun{}
	}
	cp := *run
	r.runs[run.ID] = &cp
	return nil
}
func (r *testDeploymentRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	if run, ok := r.runs[id]; ok {
		cp := *run
		return &cp, nil
	}
	return nil, nil
}
func (r *testDeploymentRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	out := make([]domain.DeploymentRun, 0, len(r.runs))
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (r *testDeploymentRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if run, ok := r.runs[id]; ok {
		run.Status = status
		run.ExitCode = exitCode
	}
	return nil
}

type testObservationRepo struct{}

func (r *testObservationRepo) Create(context.Context, *domain.RuntimeObservation) error { return nil }
func (r *testObservationRepo) GetLatest(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return nil, nil
}
func (r *testObservationRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

type testEnvironmentServiceStateRepo struct {
	states  map[string]*domain.EnvironmentServiceState
	upserts int
}

func (r *testEnvironmentServiceStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	r.upserts++
	cp := *state
	r.states[state.ServiceID.String()+":"+state.EnvironmentID.String()] = &cp
	return nil
}
func (r *testEnvironmentServiceStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, ok := r.states[serviceID.String()+":"+envID.String()]
	if !ok {
		return nil, nil
	}
	cp := *state
	return &cp, nil
}
func (r *testEnvironmentServiceStateRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListByService(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListDrifted(context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListDueForObservation(context.Context, time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

type testPolicyRepo struct {
	globalPolicies []domain.DeploymentPolicy
	envPolicies    []domain.DeploymentPolicy
}

func (r *testPolicyRepo) Create(context.Context, *domain.DeploymentPolicy) error { return nil }
func (r *testPolicyRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) GetByName(context.Context, string) (*domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) List(context.Context, bool) ([]domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.DeploymentPolicy, error) {
	return append([]domain.DeploymentPolicy(nil), r.envPolicies...), nil
}
func (r *testPolicyRepo) ListGlobal(context.Context) ([]domain.DeploymentPolicy, error) {
	return append([]domain.DeploymentPolicy(nil), r.globalPolicies...), nil
}
func (r *testPolicyRepo) Update(context.Context, *domain.DeploymentPolicy) error { return nil }
func (r *testPolicyRepo) Delete(context.Context, uuid.UUID) error                { return nil }

type testSignatureRepo struct {
	hasVerifiedSignature bool
}

func (r *testSignatureRepo) Create(context.Context, *domain.ArtifactSignature) error { return nil }
func (r *testSignatureRepo) GetByID(context.Context, uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) ListByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) ListVerifiedByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) HasVerifiedSignature(context.Context, uuid.UUID) (bool, error) {
	return r.hasVerifiedSignature, nil
}

type testSBOMRepo struct{}

func (r *testSBOMRepo) CreateSBOM(context.Context, *domain.ArtifactSBOM) error { return nil }
func (r *testSBOMRepo) GetSBOMByID(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) GetSBOMByArtifact(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) GetSBOMByHash(context.Context, string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) CreatePackages(context.Context, []domain.SBOMPackage) error { return nil }
func (r *testSBOMRepo) ListPackagesBySBOM(context.Context, uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (r *testSBOMRepo) SearchPackagesByName(context.Context, string, int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

func decodeJSONMap(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}
