package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLLMCompatibilityRoundTripsRouteAndReleaseConfig(t *testing.T) {
	route := &domain.LLMRoute{ID: uuid.New(), Name: "chat", Description: "desc", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "chat-public", Path: "/v1/chat", TimeoutSeconds: 30, Headers: map[string]string{"x": "y"}}, DefaultPlacementPolicy: &domain.LLMPlacementPolicy{PreferredKinds: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}, MinGPUMemoryGB: 24}, DefaultPromotionGate: &domain.LLMPromotionGateConfig{IntervalSeconds: 1, TimeoutSeconds: 5, SuccessThreshold: 2, FailureThreshold: 1}, Metadata: map[string]any{"owner": "ops"}}
	convertedRoute := MLModelToLLMRoute(LLMRouteToMLModel(route))
	require.Equal(t, route.GatewayConfig, convertedRoute.GatewayConfig)
	require.Equal(t, route.DefaultPlacementPolicy, convertedRoute.DefaultPlacementPolicy)
	require.Equal(t, route.DefaultPromotionGate, convertedRoute.DefaultPromotionGate)
	require.Equal(t, route.Metadata, convertedRoute.Metadata)

	release := &domain.LLMRelease{ID: uuid.New(), RouteID: route.ID, Version: "v1", ModelRef: "hf://example/model", ModelSource: domain.ModelSourceHuggingFace, ModelRevision: "abc1234", EstimatedVRAMGB: 24, BackendPreferences: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "vllm", ContainerPort: 8000, HostPort: 18000, HealthPath: "/health"}, PlacementPolicy: &domain.LLMPlacementPolicy{MinGPUMemoryGB: 24}, PromotionGate: &domain.LLMPromotionGateConfig{IntervalSeconds: 1, TimeoutSeconds: 5, SuccessThreshold: 1, FailureThreshold: 1}, Metadata: map[string]any{"tier": "prod"}}
	convertedRelease := MLModelVersionToLLMRelease(LLMReleaseToMLModelVersion(release))
	require.Equal(t, release.RuntimeBackend, convertedRelease.RuntimeBackend)
	require.Equal(t, release.PlacementPolicy, convertedRelease.PlacementPolicy)
	require.Equal(t, release.PromotionGate, convertedRelease.PromotionGate)
	require.Equal(t, release.Metadata, convertedRelease.Metadata)
}

func TestMLRegistryBackfillsLLMAndParityDetectsMismatch(t *testing.T) {
	ctx := context.Background()
	routeID, releaseID, envID, intentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	source := &fakeLLMBackfillSource{
		routes:   []domain.LLMRoute{{ID: routeID, Name: "chat", Description: "chat route"}},
		releases: []domain.LLMRelease{{ID: releaseID, RouteID: routeID, Version: "v1", ModelRef: "hf://example/model", ModelSource: domain.ModelSourceHuggingFace, BackendPreferences: []domain.LLMBackendKind{domain.LLMBackendKindVLLM}, RuntimeBackend: &domain.LLMRuntimeManagedBackendConfig{Image: "vllm", ContainerPort: 8000, HostPort: 18000, HealthPath: "/health"}}},
		intents:  []domain.LLMDeploymentIntent{{ID: intentID, RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID, RequestedBy: "alice", SourceKind: domain.SourceKindManual, ApprovalStatus: domain.ApprovalStatusNotRequired, Status: domain.IntentStatusApproved}},
		states:   []domain.LLMRouteState{{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &releaseID, DesiredIntentID: &intentID, DriftStatus: domain.DriftStatusDeploying, GatewayStatus: domain.GatewayRouteStatusPending, BackendHealth: domain.HealthStatusUnknown}},
	}
	repo := newFakeMLRegistryRepo()
	svc := NewMLRegistryService(repo, nil, nil)

	report, err := svc.BackfillLLMCompatibility(ctx, source)
	require.NoError(t, err)
	require.Equal(t, 1, report.RoutesBackfilled)
	require.Equal(t, 1, report.ReleasesBackfilled)
	require.Equal(t, 1, report.IntentsBackfilled)
	require.Equal(t, 1, report.StatesBackfilled)

	parity, err := svc.CheckLLMCompatibilityParity(ctx, source)
	require.NoError(t, err)
	require.True(t, parity.OK(), parity.Mismatches)

	repo.states[key(routeID, envID)].DriftStatus = domain.DriftStatusDrifted
	parity, err = svc.CheckLLMCompatibilityParity(ctx, source)
	require.NoError(t, err)
	require.False(t, parity.OK())
	require.Contains(t, parity.Mismatches[0], "drift mismatch")
}

type fakeLLMBackfillSource struct {
	routes   []domain.LLMRoute
	releases []domain.LLMRelease
	intents  []domain.LLMDeploymentIntent
	runs     []domain.LLMDeploymentRun
	obs      *domain.LLMRouteObservation
	states   []domain.LLMRouteState
}

func (s *fakeLLMBackfillSource) ListRoutes(context.Context, int, int) ([]domain.LLMRoute, error) {
	return s.routes, nil
}
func (s *fakeLLMBackfillSource) ListReleases(context.Context, uuid.UUID, int, int) ([]domain.LLMRelease, error) {
	return s.releases, nil
}
func (s *fakeLLMBackfillSource) ListDeploymentIntents(context.Context, uuid.UUID, uuid.UUID, int, int) ([]domain.LLMDeploymentIntent, error) {
	return s.intents, nil
}
func (s *fakeLLMBackfillSource) ListDeploymentRuns(context.Context, uuid.UUID) ([]domain.LLMDeploymentRun, error) {
	return s.runs, nil
}
func (s *fakeLLMBackfillSource) GetLatestObservation(context.Context, uuid.UUID, uuid.UUID) (*domain.LLMRouteObservation, error) {
	return s.obs, nil
}
func (s *fakeLLMBackfillSource) ListAllRouteStates(context.Context) ([]domain.LLMRouteState, error) {
	return s.states, nil
}

type fakeMLRegistryRepo struct {
	models       map[uuid.UUID]*domain.MLModel
	modelBySlug  map[string]*domain.MLModel
	versions     map[uuid.UUID]*domain.MLModelVersion
	endpoints    map[uuid.UUID]*domain.MLInferenceEndpoint
	intents      map[uuid.UUID]*domain.MLDeploymentIntent
	runs         map[uuid.UUID]*domain.MLDeploymentRun
	observations map[uuid.UUID]*domain.MLInferenceObservation
	states       map[string]*domain.MLInferenceState
	artifacts    []domain.MLArtifactRef
	edges        []domain.MLProvenanceEdge
}

func newFakeMLRegistryRepo() *fakeMLRegistryRepo {
	return &fakeMLRegistryRepo{models: map[uuid.UUID]*domain.MLModel{}, modelBySlug: map[string]*domain.MLModel{}, versions: map[uuid.UUID]*domain.MLModelVersion{}, endpoints: map[uuid.UUID]*domain.MLInferenceEndpoint{}, intents: map[uuid.UUID]*domain.MLDeploymentIntent{}, runs: map[uuid.UUID]*domain.MLDeploymentRun{}, observations: map[uuid.UUID]*domain.MLInferenceObservation{}, states: map[string]*domain.MLInferenceState{}, artifacts: []domain.MLArtifactRef{}, edges: []domain.MLProvenanceEdge{}}
}

func (r *fakeMLRegistryRepo) UpsertModel(_ context.Context, m *domain.MLModel) error {
	cp := *m
	r.models[m.ID] = &cp
	r.modelBySlug[m.Slug] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetModel(_ context.Context, id uuid.UUID) (*domain.MLModel, error) {
	return r.models[id], nil
}
func (r *fakeMLRegistryRepo) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	return r.modelBySlug[slug], nil
}
func (r *fakeMLRegistryRepo) ListModels(context.Context, domain.MLTaskKind, int, int) ([]domain.MLModel, error) {
	out := []domain.MLModel{}
	for _, m := range r.models {
		out = append(out, *m)
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertModelVersion(_ context.Context, v *domain.MLModelVersion) error {
	cp := *v
	r.versions[v.ID] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	return r.versions[id], nil
}
func (r *fakeMLRegistryRepo) GetModelVersionByModelVersion(_ context.Context, modelID uuid.UUID, version string) (*domain.MLModelVersion, error) {
	for _, v := range r.versions {
		if v.ModelID == modelID && v.Version == version {
			return v, nil
		}
	}
	return nil, nil
}
func (r *fakeMLRegistryRepo) ListModelVersions(_ context.Context, modelID uuid.UUID, _ int, _ int) ([]domain.MLModelVersion, error) {
	out := []domain.MLModelVersion{}
	for _, v := range r.versions {
		if v.ModelID == modelID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertArtifactRef(_ context.Context, artifact *domain.MLArtifactRef) error {
	cp := *artifact
	r.artifacts = append(r.artifacts, cp)
	return nil
}
func (r *fakeMLRegistryRepo) ListArtifactRefsByModelVersion(_ context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error) {
	out := []domain.MLArtifactRef{}
	for _, artifact := range r.artifacts {
		if artifact.ModelVersionID != nil && *artifact.ModelVersionID == modelVersionID {
			out = append(out, artifact)
		}
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertProvenanceEdge(_ context.Context, edge *domain.MLProvenanceEdge) error {
	cp := *edge
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	r.edges = append(r.edges, cp)
	return nil
}
func (r *fakeMLRegistryRepo) ListProvenanceEdgesByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error) {
	out := []domain.MLProvenanceEdge{}
	for _, edge := range r.edges {
		if (edge.FromArtifactID != nil && *edge.FromArtifactID == artifactID) || (edge.ToArtifactID != nil && *edge.ToArtifactID == artifactID) {
			out = append(out, edge)
		}
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertRecipe(context.Context, *domain.MLRecipe) error { return nil }
func (r *fakeMLRegistryRepo) GetRecipe(context.Context, uuid.UUID) (*domain.MLRecipe, error) {
	return nil, nil
}
func (r *fakeMLRegistryRepo) UpsertRecipeRun(context.Context, *domain.MLRecipeRun) error { return nil }
func (r *fakeMLRegistryRepo) GetRecipeRun(context.Context, uuid.UUID) (*domain.MLRecipeRun, error) {
	return nil, nil
}
func (r *fakeMLRegistryRepo) UpsertInferenceEndpoint(_ context.Context, e *domain.MLInferenceEndpoint) error {
	cp := *e
	r.endpoints[e.ID] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	return r.endpoints[id], nil
}
func (r *fakeMLRegistryRepo) GetInferenceEndpointByNameEnv(_ context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	for _, e := range r.endpoints {
		if e.Name == name && e.EnvironmentID == envID {
			return e, nil
		}
	}
	return nil, nil
}
func (r *fakeMLRegistryRepo) ListInferenceEndpoints(context.Context, uuid.UUID, int, int) ([]domain.MLInferenceEndpoint, error) {
	return nil, nil
}
func (r *fakeMLRegistryRepo) UpsertDeploymentIntent(_ context.Context, i *domain.MLDeploymentIntent) error {
	cp := *i
	r.intents[i.ID] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	return r.intents[id], nil
}
func (r *fakeMLRegistryRepo) ListDeploymentIntents(_ context.Context, endpointID, envID uuid.UUID, _ int, _ int) ([]domain.MLDeploymentIntent, error) {
	out := []domain.MLDeploymentIntent{}
	for _, i := range r.intents {
		if i.EndpointID == endpointID && i.EnvironmentID == envID {
			out = append(out, *i)
		}
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertDeploymentRun(_ context.Context, run *domain.MLDeploymentRun) error {
	cp := *run
	r.runs[run.ID] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetDeploymentRun(_ context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error) {
	return r.runs[id], nil
}
func (r *fakeMLRegistryRepo) ListDeploymentRuns(_ context.Context, intentID uuid.UUID) ([]domain.MLDeploymentRun, error) {
	out := []domain.MLDeploymentRun{}
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (r *fakeMLRegistryRepo) UpsertInferenceObservation(_ context.Context, obs *domain.MLInferenceObservation) error {
	cp := *obs
	r.observations[obs.ID] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetLatestInferenceObservation(context.Context, uuid.UUID, uuid.UUID) (*domain.MLInferenceObservation, error) {
	return nil, nil
}
func (r *fakeMLRegistryRepo) UpsertInferenceState(_ context.Context, s *domain.MLInferenceState) error {
	cp := *s
	r.states[key(s.EndpointID, s.EnvironmentID)] = &cp
	return nil
}
func (r *fakeMLRegistryRepo) GetInferenceState(_ context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	return r.states[key(endpointID, envID)], nil
}
func (r *fakeMLRegistryRepo) ListInferenceStates(context.Context) ([]domain.MLInferenceState, error) {
	out := []domain.MLInferenceState{}
	for _, s := range r.states {
		out = append(out, *s)
	}
	return out, nil
}
