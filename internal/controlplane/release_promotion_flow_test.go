package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/hiveci"
	"github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
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
	mu                    sync.Mutex
	decisions             []string
	registrationDecisions []string
}

func (a *promotionFlowAudit) AuditPromotionDecision(_ context.Context, _ ReleasePromotionDecision, decision string, _ error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.decisions = append(a.decisions, decision)
	return nil
}

func (a *promotionFlowAudit) PreparePromotionDecisionAudit(_ context.Context, _ ReleasePromotionDecision, decision string, _ error) (*repository.NostrEventRecord, error) {
	content, _ := json.Marshal(map[string]string{"decision": decision})
	return &repository.NostrEventRecord{ID: uuid.NewString(), Content: string(content), EntityType: "release_promotion"}, nil
}

func (a *promotionFlowAudit) AuditReleaseRegistration(_ context.Context, _ domain.HiveCIAcceptedRelease, _ *domain.Artifact, decision string, _ error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registrationDecisions = append(a.registrationDecisions, decision)
	return nil
}

func (a *promotionFlowAudit) PrepareReleaseRegistrationAudit(_ context.Context, _ domain.HiveCIAcceptedRelease, _ *domain.Artifact, decision string, _ error) (*repository.NostrEventRecord, error) {
	content, _ := json.Marshal(map[string]string{"decision": decision})
	return &repository.NostrEventRecord{ID: uuid.NewString(), Content: string(content), EntityType: "artifact_registration"}, nil
}

type promotionFlowAuditEvents struct {
	audit *promotionFlowAudit
}

func (e *promotionFlowAuditEvents) Record(_ context.Context, record *repository.NostrEventRecord) (bool, error) {
	var body map[string]string
	if err := json.Unmarshal([]byte(record.Content), &body); err != nil {
		return false, err
	}
	e.audit.mu.Lock()
	defer e.audit.mu.Unlock()
	switch record.EntityType {
	case "release_promotion":
		e.audit.decisions = append(e.audit.decisions, body["decision"])
	case "artifact_registration":
		e.audit.registrationDecisions = append(e.audit.registrationDecisions, body["decision"])
	}
	return true, nil
}
func (e *promotionFlowAuditEvents) GetByID(context.Context, string) (*repository.NostrEventRecord, error) {
	return nil, repository.ErrNotFound
}
func (e *promotionFlowAuditEvents) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (e *promotionFlowAuditEvents) ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (e *promotionFlowAuditEvents) FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (e *promotionFlowAuditEvents) ListByEntity(context.Context, string, uuid.UUID, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}
func (e *promotionFlowAuditEvents) LatestCreatedAtForKinds(context.Context, []int) (*time.Time, error) {
	return nil, nil
}
func (e *promotionFlowAuditEvents) LatestCreatedAtForKindsAndAuthors(context.Context, []int, []string) (*time.Time, error) {
	return nil, nil
}

type promotionFlowTxExecutor struct {
	services     repository.ServiceRepository
	environments repository.EnvironmentRepository
	builds       repository.BuildRepository
	artifacts    repository.ArtifactRepository
	intents      repository.DeploymentIntentRepository
	state        repository.EnvironmentServiceStateRepository
	events       repository.NostrEventRepository
}

func (e *promotionFlowTxExecutor) WithinTx(ctx context.Context, fn func(repository.TxRepos) error) error {
	return fn(repository.TxRepos{
		Services: e.services, Environments: e.environments,
		Builds: e.builds, Artifacts: e.artifacts, Intents: e.intents,
		State: e.state, NostrEvents: e.events,
	})
}

type promotionFlowReleaseEvidence struct {
	run      *nostr.Event
	policies []domain.HiveCIPipelinePolicy
	objects  map[string]hiveci.ResolvedReleaseArtifact
}

func (e *promotionFlowReleaseEvidence) GetWorkflowRunEvent(context.Context, string) (*nostr.Event, error) {
	return e.run, nil
}
func (e *promotionFlowReleaseEvidence) ListPipelinePolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return e.policies, nil
}
func (e *promotionFlowReleaseEvidence) AdmitWorker(_ context.Context, pubkey, capability, adID string) (hiveci.WorkerAdmissionEvidence, bool, error) {
	return hiveci.WorkerAdmissionEvidence{
		WorkerIdentity: pubkey, WorkerCapability: capability, WorkerAdEventID: adID,
		WorkerAdvertisedAt: time.Now().UTC(), DecisionCode: "eligible",
		CapacityClass: string(domain.WorkerCapacityOpen), PressureLevel: string(domain.WorkerPressureNominal),
	}, true, nil
}
func (e *promotionFlowReleaseEvidence) ResolveArtifact(_ context.Context, artifact domain.HiveCIReleaseArtifact) (hiveci.ResolvedReleaseArtifact, error) {
	return e.objects[artifact.Digest], nil
}

type promotionFlowReleaseStore struct {
	accepted *domain.HiveCIAcceptedRelease
}

func (s *promotionFlowReleaseStore) CommitAcceptedRelease(_ context.Context, release domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error) {
	if s.accepted != nil {
		if s.accepted.ContentDigest != release.ContentDigest {
			return domain.HiveCIReleaseCommitResult{}, repository.ErrHiveCIReleaseReplayConflict
		}
		return domain.HiveCIReleaseCommitResult{Release: *s.accepted, Replay: true}, nil
	}
	copy := release
	s.accepted = &copy
	return domain.HiveCIReleaseCommitResult{Release: release}, nil
}

func promotionFlowDescriptor(repositoryName, mediaType string, content []byte) domain.HiveCIReleaseArtifact {
	sum := sha256.Sum256(content)
	return domain.HiveCIReleaseArtifact{
		Repository: repositoryName, Digest: "sha256:" + hex.EncodeToString(sum[:]),
		MediaType: mediaType, Size: int64(len(content)),
	}
}

func ingestPromotionFlowRelease(
	t *testing.T,
	policyID, serviceID, environmentID uuid.UUID,
	repoCoordinate, repositoryName, workflowPath, previousDigest string,
) domain.HiveCIAcceptedRelease {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	issuer, attestor, worker := nostr.Generate(), nostr.Generate(), nostr.Generate()
	lineage := domain.HiveCIReleaseLineage{
		TriggerIdentity: strings.Repeat("1", 64), TriggerSource: "gitea", TriggerID: "delivery-e2e",
		PREventID: strings.Repeat("2", 64), ReviewEventID: strings.Repeat("3", 64),
		AuditEventID: strings.Repeat("4", 64), RepoAddress: repoCoordinate,
		SourceRepoIdentity:  "gitea.example/team/bahia",
		SourceProvenanceRef: "hiveci-source-provenance:v1:" + strings.Repeat("5", 64),
		Commit:              strings.Repeat("d", 40), Tree: strings.Repeat("7", 40),
		WorkflowDigest: strings.Repeat("8", 64),
	}
	workerCapabilityJSON, err := json.Marshal(nostr.Tags{
		{"S", "linux"}, {"A", "amd64"}, {"S", "docker-buildx"},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution := domain.HiveCIReleaseExecution{
		Complete: true, Status: "success", ExitCode: 0, DurationMS: 12000, BahiaDuration: "12",
		WorkerIdentity: worker.Public().Hex(), WorkerCapability: string(workerCapabilityJSON),
		BuildEnvironmentImageDigest: "sha256:" + strings.Repeat("9", 64),
		Tests:                       domain.HiveCIReleaseTestSummary{Status: "success", Total: 3, Passed: 3},
		DurableLogReference:         "cas://sha256/" + strings.Repeat("b", 64),
	}
	run := &nostr.Event{
		Kind: kinds.HiveCIWorkflowRun, CreatedAt: nostr.Timestamp(now.Add(-time.Minute).Unix()),
		Tags: nostr.Tags{
			{"a", repoCoordinate}, {"repo-address", repoCoordinate}, {"commit", lineage.Commit},
			{"branch", "refs/heads/master"}, {"workflow", workflowPath}, {"trigger", "push"},
			{"triggered-by", strings.Repeat("c", 64)}, {"publisher", nostr.Generate().Public().Hex()}, {"t", "hive-ci"},
			{"pr", lineage.PREventID}, {"pr-event", lineage.PREventID}, {"review", lineage.ReviewEventID},
			{"audit", lineage.AuditEventID}, {"tree", lineage.Tree}, {"workflow-digest", lineage.WorkflowDigest},
			{"source-provenance", lineage.SourceProvenanceRef}, {"source-repo", lineage.SourceRepoIdentity},
			{"idempotency", lineage.TriggerIdentity}, {"trigger-envelope", lineage.TriggerIdentity},
			{"trigger-source", lineage.TriggerSource}, {"trigger-id", lineage.TriggerID},
			{"worker", execution.WorkerIdentity}, {"worker-ad", strings.Repeat("e", 64)},
			{"worker-capability", execution.WorkerCapability}, {"review-policy", "review-policy-v1"},
			{"policy-digest", strings.Repeat("f", 64)},
		},
	}
	if err := run.Sign(issuer); err != nil {
		t.Fatal(err)
	}
	lineage.WorkflowRunEventID = run.ID.Hex()

	manifestBytes := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`)
	sbomBytes := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5"}`)
	manifest := promotionFlowDescriptor(repositoryName, "application/vnd.oci.image.manifest.v1+json", manifestBytes)
	sbom := promotionFlowDescriptor(repositoryName, "application/vnd.cyclonedx+json", sbomBytes)
	identityInput, err := json.Marshal(struct {
		Schema  string                      `json:"schema"`
		Lineage domain.HiveCIReleaseLineage `json:"lineage"`
	}{Schema: domain.HiveCIReleaseSchemaV1, Lineage: lineage})
	if err != nil {
		t.Fatal(err)
	}
	identityHash := sha256.Sum256(identityInput)
	releaseIdentity := domain.HiveCIReleaseIdentityPrefix + hex.EncodeToString(identityHash[:])
	provenanceBytes, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://sharegap.net/hiveci/release-provenance/v1",
		"subject":       []any{map[string]any{"name": repositoryName, "digest": map[string]string{"sha256": strings.TrimPrefix(manifest.Digest, "sha256:")}}},
		"predicate":     map[string]any{"release_identity": releaseIdentity, "lineage": lineage, "execution": execution, "sbom_digest": sbom.Digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	provenance := promotionFlowDescriptor(repositoryName, "application/vnd.in-toto+json", provenanceBytes)
	result := domain.HiveCIReleaseResult{
		SchemaVersion: domain.HiveCIReleaseSchemaV1, ResultType: domain.HiveCIReleaseResultType,
		Status: "success", ReleaseIdentity: releaseIdentity, ImageTag: "signed-metadata-only",
		Lineage: lineage, Execution: execution, Manifest: manifest, SBOM: sbom, Provenance: provenance,
		ArtifactAttestation: domain.HiveCISignetArtifactAttestation{
			Type: "https://sharegap.net/hiveci/signet-artifact-attestation/v1", SignerPubkey: attestor.Public().Hex(),
			Subjects: []domain.HiveCIReleaseArtifact{manifest, sbom, provenance},
		},
	}
	content, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	releaseEvent := &nostr.Event{
		Kind: kinds.HiveCIWorkflowResult, CreatedAt: nostr.Timestamp(now.Unix()), Content: string(content),
		Tags: nostr.Tags{
			{"e", lineage.WorkflowRunEventID}, {"status", "success"}, {"result", domain.HiveCIReleaseResultType},
			{"release", releaseIdentity}, {"trigger-envelope", lineage.TriggerIdentity},
			{"trigger-source", lineage.TriggerSource}, {"trigger-id", lineage.TriggerID},
			{"pr", lineage.PREventID}, {"review", lineage.ReviewEventID}, {"audit", lineage.AuditEventID},
			{"a", lineage.RepoAddress}, {"source-repo", lineage.SourceRepoIdentity},
			{"source-provenance", lineage.SourceProvenanceRef}, {"commit", lineage.Commit}, {"tree", lineage.Tree},
			{"workflow-digest", lineage.WorkflowDigest}, {"worker", execution.WorkerIdentity},
			{"worker-capability", execution.WorkerCapability}, {"build-image", execution.BuildEnvironmentImageDigest},
			{"exit_code", "0"}, {"duration", execution.BahiaDuration}, {"log_url", execution.DurableLogReference},
			{"image_repo", manifest.Repository}, {"image_digest", manifest.Digest}, {"sbom_digest", sbom.Digest},
			{"provenance_digest", provenance.Digest}, {"image_tag", result.ImageTag},
		},
	}
	if err := releaseEvent.Sign(attestor); err != nil {
		t.Fatal(err)
	}
	policy := domain.HiveCIPipelinePolicy{
		ID: policyID, RepoCoordinate: repoCoordinate, WorkflowPath: workflowPath,
		BranchPattern: "refs/heads/master", ServiceID: serviceID, EnvironmentID: environmentID, Enabled: true,
		Metadata: map[string]any{
			"workflow_digest": lineage.WorkflowDigest, "policy_digest": strings.Repeat("f", 64),
			"review_policy": "review-policy-v1", "source_repo_identity": lineage.SourceRepoIdentity,
			"release_image_repository": repositoryName, "release_attestors": []any{attestor.Public().Hex()},
			"rollback_compatibility": map[string]any{"compatible_from_digests": []any{previousDigest}},
			"health_contract":        map[string]any{"type": "http", "path": "/health", "timeout_seconds": 10},
			"readiness_contract":     map[string]any{"type": "http", "path": "/ready", "timeout_seconds": 15},
		},
	}
	evidence := &promotionFlowReleaseEvidence{
		run: run, policies: []domain.HiveCIPipelinePolicy{policy},
		objects: map[string]hiveci.ResolvedReleaseArtifact{
			manifest.Digest:   {Content: manifestBytes, MediaType: manifest.MediaType, Size: manifest.Size},
			sbom.Digest:       {Content: sbomBytes, MediaType: sbom.MediaType, Size: sbom.Size},
			provenance.Digest: {Content: provenanceBytes, MediaType: provenance.MediaType, Size: provenance.Size},
		},
	}
	store := &promotionFlowReleaseStore{}
	commit, err := hiveci.NewReleaseIngestor(
		evidence, store, []string{attestor.Public().Hex()}, []string{issuer.Public().Hex()},
	).Ingest(context.Background(), releaseEvent)
	if err != nil {
		t.Fatalf("ingest signed Hive-CI RELEASE: %v", err)
	}
	return commit.Release
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
	audit := &promotionFlowAudit{}
	auditEvents := &promotionFlowAuditEvents{audit: audit}
	txExecutor := &promotionFlowTxExecutor{
		services: svcRepo, environments: envRepo,
		builds: buildRepo, artifacts: artifactRepo, intents: intentRepo,
		state: stateRepo, events: auditEvents,
	}
	registry := service.NewRegistryService(
		svcRepo, envRepo, buildRepo, artifactRepo, intentRepo, runRepo,
		&testObservationRepo{}, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRegistryTxExecutor(txExecutor),
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
	release = ingestPromotionFlowRelease(
		t, policyID, serviceID, environmentID,
		repoCoordinate, repositoryName, workflowPath, previousDigest,
	)
	releaseDigest = release.Result.Manifest.Digest
	releaseIdentity = release.Result.ReleaseIdentity
	bridge := pipeline.NewBridge(
		nil, svcRepo, buildRepo, artifactRepo, nil, nil, nil, nil, registry,
		nil, false, zap.NewNop(),
	)
	bridge.SetReleaseRegistrationAuditor(audit)
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
	if len(audit.registrationDecisions) != 1 || audit.registrationDecisions[0] != "accepted" {
		t.Fatalf("registration audits=%v", audit.registrationDecisions)
	}
	if len(audit.decisions) != 4 || audit.decisions[0] != "rejected" ||
		audit.decisions[1] != "accepted" || audit.decisions[2] != "accepted" ||
		audit.decisions[3] != "rejected" {
		t.Fatalf("promotion audits=%v", audit.decisions)
	}
}
