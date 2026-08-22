package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/pipeline"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/workflow"
	"go.uber.org/zap"
)

type promotionFlowBuildRepo struct {
	builds map[uuid.UUID]*domain.Build
}

func (r *promotionFlowBuildRepo) Create(_ context.Context, build *domain.Build) error {
	if build.ID == uuid.Nil {
		build.ID = uuid.New()
	}
	if r.builds == nil {
		r.builds = map[uuid.UUID]*domain.Build{}
	}
	copy := *build
	r.builds[build.ID] = &copy
	return nil
}
func (r *promotionFlowBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	build := r.builds[id]
	if build == nil {
		return nil, nil
	}
	copy := *build
	return &copy, nil
}
func (r *promotionFlowBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, runID string) (*domain.Build, error) {
	for _, build := range r.builds {
		if build.CISystem == ciSystem && build.CIRunID == runID {
			copy := *build
			return &copy, nil
		}
	}
	return nil, nil
}
func (r *promotionFlowBuildRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Build, error) {
	return nil, nil
}
func (r *promotionFlowBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	if build := r.builds[id]; build != nil {
		build.Status = status
	}
	return nil
}

type promotionFlowUnitRepo struct {
	unit *domain.DeploymentUnit
}

func (r *promotionFlowUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	copy := *unit
	r.unit = &copy
	return nil
}
func (r *promotionFlowUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	if r.unit == nil || r.unit.ID != id {
		return nil, nil
	}
	copy := *r.unit
	return &copy, nil
}
func (r *promotionFlowUnitRepo) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	if r.unit == nil || r.unit.EnvironmentID != environmentID || r.unit.Key != key {
		return nil, nil
	}
	copy := *r.unit
	return &copy, nil
}
func (r *promotionFlowUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	if r.unit == nil || r.unit.EnvironmentID != environmentID {
		return nil, nil
	}
	return []domain.DeploymentUnit{*r.unit}, nil
}
func (r *promotionFlowUnitRepo) ResolveDefault(_ context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	if r.unit == nil || env == nil || r.unit.EnvironmentID != env.ID {
		return nil, nil
	}
	copy := *r.unit
	return &copy, nil
}

type promotionFlowMembers struct {
	member *domain.OrgMember
}

func (m *promotionFlowMembers) GetMember(_ context.Context, orgID uuid.UUID, pubkey string) (*domain.OrgMember, error) {
	if m.member == nil || m.member.OrgID != orgID || m.member.Pubkey != pubkey {
		return nil, repository.ErrNotFound
	}
	copy := *m.member
	return &copy, nil
}
func (m *promotionFlowMembers) ListByPubkey(_ context.Context, pubkey string) ([]domain.OrgMember, error) {
	if m.member == nil || m.member.Pubkey != pubkey {
		return nil, nil
	}
	return []domain.OrgMember{*m.member}, nil
}

type promotionFlowAudit struct {
	mu        sync.Mutex
	decisions []string
}

func (a *promotionFlowAudit) AuditPromotionDecision(_ context.Context, _ ReleasePromotionDecision, decision string, _ error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decisions = append(a.decisions, decision)
	return nil
}

type promotionFlowLoom struct {
	mu       sync.Mutex
	requests []loom.JobRequest
}

func (l *promotionFlowLoom) SubmitJob(_ context.Context, request loom.JobRequest) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, request)
	return "loom-canary-job", nil
}
func (l *promotionFlowLoom) AwaitJobStatusFromWorker(context.Context, string, string, ...loom.StatusCallback) (*loom.JobStatus, error) {
	success := true
	exitCode := 0
	return &loom.JobStatus{JobID: "loom-canary-job", Status: loom.StatusCompleted, Success: &success, ExitCode: &exitCode}, nil
}
func (l *promotionFlowLoom) JobTimeout() time.Duration { return time.Second }

func TestAcceptedReleaseContextVMPromotionCreatesDigestOnlyCanary(t *testing.T) {
	ctx := context.Background()
	orgID, serviceID, environmentID := uuid.New(), uuid.New(), uuid.New()
	previousDigest := "sha256:" + strings.Repeat("1", 64)
	releaseDigest := "sha256:" + strings.Repeat("2", 64)
	releaseIdentity := domain.HiveCIReleaseIdentityPrefix + strings.Repeat("3", 64)
	repositoryName := "harbor.example/team/bahia"
	workflowPath := ".gitea/workflows/release.yml"
	repoCoordinate := "30617:" + strings.Repeat("a", 64) + ":bahia"

	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "bahia", ArtifactRepo: repositoryName,
		RuntimeType: domain.RuntimeTypeCompose, RuntimeConfig: &domain.ServiceRuntimeConfig{},
	}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "staging", Protected: false,
		DeployStrategy: domain.DeployStrategyCanary,
	}}
	previous := &domain.Artifact{
		ID: uuid.New(), ServiceID: serviceID, ImageRepo: repositoryName, ImageDigest: previousDigest,
	}
	artifactRepo := &testArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{previous.ID: previous}}
	buildRepo := &promotionFlowBuildRepo{builds: map[uuid.UUID]*domain.Build{}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{
		serviceID.String() + ":" + environmentID.String(): {
			ServiceID: serviceID, EnvironmentID: environmentID, DesiredArtifactID: &previous.ID,
		},
	}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, buildRepo, artifactRepo, intentRepo, runRepo,
		&testObservationRepo{}, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
	)
	policyID := uuid.New()
	release := domain.HiveCIAcceptedRelease{
		ResultEventID: "release-event", Attestor: strings.Repeat("b", 64), Workflow: workflowPath,
		Branch: "refs/heads/master", PolicyID: policyID.String(), ContentDigest: "sha256:" + strings.Repeat("6", 64),
		SignedEvent: "signed-5402", WorkflowRunSignedEvent: "signed-5401", AcceptedAt: time.Now().UTC(),
		Policy: domain.HiveCIPipelinePolicy{
			ID: policyID, RepoCoordinate: repoCoordinate, WorkflowPath: workflowPath,
			ServiceID: serviceID, EnvironmentID: environmentID, Enabled: true,
		},
		WorkerAdmissionEvidence: map[string]any{"worker_identity": strings.Repeat("c", 64), "state": "admitted"},
		RollbackCompatibility:   map[string]any{"compatible_from_digests": []any{previousDigest}},
		HealthReadinessContracts: map[string]any{
			"health":    map[string]any{"type": "http", "path": "/health", "timeout_seconds": 10},
			"readiness": map[string]any{"type": "http", "path": "/ready", "timeout_seconds": 15},
		},
		Result: domain.HiveCIReleaseResult{
			SchemaVersion: domain.HiveCIReleaseSchemaV1, ResultType: domain.HiveCIReleaseResultType,
			Status: "success", ReleaseIdentity: releaseIdentity, ImageTag: "release-candidate",
			Lineage: domain.HiveCIReleaseLineage{
				WorkflowRunEventID: "workflow-run-event", RepoAddress: repoCoordinate,
				Commit: strings.Repeat("d", 40), WorkflowDigest: "sha256:" + strings.Repeat("7", 64),
			},
			Execution: domain.HiveCIReleaseExecution{Complete: true, Status: "success", WorkerIdentity: strings.Repeat("c", 64)},
			Manifest: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: releaseDigest,
				MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100,
			},
			SBOM: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: "sha256:" + strings.Repeat("4", 64),
				MediaType: "application/vnd.cyclonedx+json", Size: 200,
			},
			Provenance: domain.HiveCIReleaseArtifact{
				Repository: repositoryName, Digest: "sha256:" + strings.Repeat("5", 64),
				MediaType: "application/vnd.in-toto+json", Size: 300,
			},
		},
	}
	bridge := pipeline.NewBridge(
		nil, svcRepo, buildRepo, artifactRepo, nil, nil, nil, nil, registry,
		nil, false, zap.NewNop(),
	)
	artifact, err := bridge.RegisterAcceptedRelease(ctx, release)
	if err != nil {
		t.Fatalf("register accepted release: %v", err)
	}
	if artifact == nil || artifact.ImageDigest != releaseDigest || artifact.ImageTag != "" {
		t.Fatalf("registered artifact is not digest-only: %#v", artifact)
	}
	if len(intentRepo.intents) != 0 {
		t.Fatal("CI registration created a deployment intent")
	}
	beforePromotion, err := stateRepo.Get(ctx, serviceID, environmentID)
	if err != nil || beforePromotion == nil || beforePromotion.DesiredArtifactID == nil || *beforePromotion.DesiredArtifactID != previous.ID {
		t.Fatalf("CI registration changed desired state: %#v err=%v", beforePromotion, err)
	}

	unit := &domain.DeploymentUnit{
		ID: uuid.New(), EnvironmentID: environmentID, Key: "staging-canary",
		RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "staging",
		ComposeDir: "/srv/bahia/staging", ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
	}
	unitRepo := &promotionFlowUnitRepo{unit: unit}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil,
		&events.NoopPublisher{}, zap.NewNop(), service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	policyService := service.NewPolicyService(&testPolicyRepo{}, nil, nil, zap.NewNop())
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	audit := &promotionFlowAudit{}
	handler := &encryptedServiceHandlers{
		registry: registry, runtimeLifecycle: lifecycle, policy: policyService,
		authorizer: encryptedTenantAuthorizer{
			services: svcRepo, environments: registry,
			rbac: auth.NewRBAC(&promotionFlowMembers{member: &domain.OrgMember{
				OrgID: orgID, Pubkey: requesterPubkey, Role: domain.RoleDeployer,
			}}),
		},
		releasePromotions: NewReleasePromotionAuthorizer(registry, audit),
		logger:            zap.NewNop(),
	}

	rpcPayload := map[string]any{
		"jsonrpc": "2.0", "id": "promote-1", "method": ContextVMMethodServiceDeploy,
		"params": map[string]any{
			"service_id": serviceID, "environment_id": environmentID,
			"deployment_unit_id": unit.ID, "artifact_id": artifact.ID,
			"strategy": "canary", "idempotency_key": "promote-1",
			"parameters": map[string]any{
				"release_identity": releaseIdentity, "artifact_digest": releaseDigest,
				"previous_artifact_digest": previousDigest,
			},
		},
	}
	requestJSON, err := json.Marshal(rpcPayload)
	if err != nil {
		t.Fatal(err)
	}
	event := makeContextVMEvent(t, testRequesterKey, string(requestJSON))
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal(requestJSON, &rpc); err != nil {
		t.Fatal(err)
	}
	rpc.Params, err = json.Marshal(rpcPayload["params"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.deploy(ctx, ContextVMRequest{
		Event: makeContextVMEvent(t, testServiceKey, string(requestJSON)), RPC: rpc,
	}); err == nil {
		t.Fatal("unauthorized promotion was accepted")
	}
	if len(intentRepo.intents) != 0 {
		t.Fatal("unauthorized promotion created an intent")
	}
	afterUnauthorized, stateErr := stateRepo.Get(ctx, serviceID, environmentID)
	if stateErr != nil || afterUnauthorized == nil || afterUnauthorized.DesiredArtifactID == nil || *afterUnauthorized.DesiredArtifactID != previous.ID {
		t.Fatalf("unauthorized promotion changed desired state: %#v err=%v", afterUnauthorized, stateErr)
	}

	result, err := handler.deploy(ctx, ContextVMRequest{Event: event, RPC: rpc})
	if err != nil {
		t.Fatalf("ContextVM promotion: %v", err)
	}
	response := result.(map[string]any)
	if response["status"] != string(domain.IntentStatusApproved) || response["strategy"] != "canary" || response["replay"] != false {
		t.Fatalf("unexpected promotion response: %#v", response)
	}
	if len(intentRepo.intents) != 1 {
		t.Fatalf("promotion intents=%d, want 1", len(intentRepo.intents))
	}
	var intent *domain.DeploymentIntent
	for _, candidate := range intentRepo.intents {
		intent = candidate
	}
	if intent.Metadata["release_promotion"] != true || intent.Metadata["artifact_digest"] != releaseDigest {
		t.Fatalf("promotion intent lost release binding: %#v", intent.Metadata)
	}

	replayResult, err := handler.deploy(ctx, ContextVMRequest{Event: event, RPC: rpc})
	if err != nil {
		t.Fatalf("exact promotion replay: %v", err)
	}
	if replayResult.(map[string]any)["replay"] != true || len(intentRepo.intents) != 1 {
		t.Fatalf("exact replay was not idempotent: result=%#v intents=%d", replayResult, len(intentRepo.intents))
	}

	conflictPayload := map[string]any{}
	if err := json.Unmarshal(requestJSON, &conflictPayload); err != nil {
		t.Fatal(err)
	}
	conflictPayload["params"].(map[string]any)["parameters"].(map[string]any)["previous_artifact_digest"] = "sha256:" + strings.Repeat("9", 64)
	conflictJSON, _ := json.Marshal(conflictPayload)
	var conflictRPC ContextVMJSONRPCRequest
	if err := json.Unmarshal(conflictJSON, &conflictRPC); err != nil {
		t.Fatal(err)
	}
	conflictRPC.Params, err = json.Marshal(conflictPayload["params"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handler.deploy(ctx, ContextVMRequest{
		Event: makeContextVMEvent(t, testRequesterKey, string(conflictJSON)), RPC: conflictRPC,
	}); !errors.Is(err, ErrPromotionReplayConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	if len(intentRepo.intents) != 1 {
		t.Fatal("conflicting replay created another intent")
	}

	loomFake := &promotionFlowLoom{}
	coordinator := workflow.NewCoordinator(
		registry, nil, &events.NoopPublisher{}, zap.NewNop(),
		workflow.WithDeploymentLoomClient(loomFake),
		workflow.WithDeploymentUnitRouting(unitRepo, nil),
	)
	if err := coordinator.ExecuteDeployment(ctx, intent.ID); err != nil {
		t.Fatalf("execute authorized canary: %v", err)
	}
	coordinator.Shutdown(time.Second)
	if len(loomFake.requests) != 1 {
		t.Fatalf("canary jobs=%d, want 1", len(loomFake.requests))
	}
	job := loomFake.requests[0]
	if job.Image != repositoryName+"@"+releaseDigest || job.Digest != releaseDigest ||
		job.Params["rollout_strategy"] != "canary" || job.Params["canary_weight"] != "10" ||
		job.Params["deployment_unit_id"] != unit.ID.String() || job.Params["deployment_unit_key"] != unit.Key ||
		!strings.Contains(job.Params["health_contract"], "/health") ||
		!strings.Contains(job.Params["readiness_contract"], "/ready") {
		t.Fatalf("unexpected canary job: %#v", job)
	}
	if len(audit.decisions) != 4 || audit.decisions[0] != "rejected" ||
		audit.decisions[1] != "accepted" || audit.decisions[2] != "accepted" ||
		audit.decisions[3] != "rejected" {
		t.Fatalf("promotion audits=%v", audit.decisions)
	}
}
