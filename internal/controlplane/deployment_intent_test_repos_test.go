package controlplane

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

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

type testBuildRepo struct {
	build *domain.Build
}

func (r *testBuildRepo) Create(context.Context, *domain.Build) error { return nil }
func (r *testBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	if r.build != nil && r.build.ID == id {
		cp := *r.build
		return &cp, nil
	}
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
	artifact  *domain.Artifact
	artifacts map[uuid.UUID]*domain.Artifact
	created   chan struct{}
}

func (r *testArtifactRepo) Create(_ context.Context, artifact *domain.Artifact) error {
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	if r.artifacts == nil {
		r.artifacts = map[uuid.UUID]*domain.Artifact{}
	}
	cp := *artifact
	r.artifacts[artifact.ID] = &cp
	if r.created != nil {
		select {
		case r.created <- struct{}{}:
		default:
		}
	}
	return nil
}
func (r *testArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if r.artifacts != nil {
		if artifact, ok := r.artifacts[id]; ok {
			cp := *artifact
			return &cp, nil
		}
	}
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
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
func (r *testDeploymentIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	if intent, ok := r.intents[id]; ok {
		intent.DesiredState = desiredState
		intent.DesiredHash = desiredHash
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
	states   map[string]*domain.EnvironmentServiceState
	upserts  int
	upserted chan struct{}
}

func (r *testEnvironmentServiceStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	r.upserts++
	cp := *state
	r.states[state.ServiceID.String()+":"+state.EnvironmentID.String()] = &cp
	if r.upserted != nil {
		select {
		case r.upserted <- struct{}{}:
		default:
		}
	}
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
