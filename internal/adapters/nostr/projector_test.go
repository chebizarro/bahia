package nostr

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

const projectorTestPrivateKey = "1111111111111111111111111111111111111111111111111111111111111111"

type captureProjectionPublisher struct {
	mu     sync.Mutex
	events []gonostr.Event
}

func (p *captureProjectionPublisher) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, ev)
	return 1, nil
}

func (p *captureProjectionPublisher) byKind(kind int) []gonostr.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []gonostr.Event
	for _, ev := range p.events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

type fakeProjectionSource struct {
	services    map[uuid.UUID]domain.Service
	envs        map[uuid.UUID]domain.Environment
	states      map[string]domain.EnvironmentServiceState
	builds      map[uuid.UUID]domain.Build
	artifacts   map[uuid.UUID]domain.Artifact
	intents     map[uuid.UUID]domain.DeploymentIntent
	runs        map[uuid.UUID]domain.DeploymentRun
	policies    map[uuid.UUID]domain.DeploymentPolicy
	llmRoutes   map[uuid.UUID]domain.LLMRoute
	llmStates   map[string]domain.LLMRouteState
	llmIntents  map[uuid.UUID]domain.LLMDeploymentIntent
	llmRuns     map[uuid.UUID]domain.LLMDeploymentRun
	mlModels    map[uuid.UUID]domain.MLModel
	mlVersions  map[uuid.UUID]domain.MLModelVersion
	mlEndpoints map[uuid.UUID]domain.MLInferenceEndpoint
	mlStates    map[string]domain.MLInferenceState
	mlArtifacts map[uuid.UUID]domain.MLArtifactRef
	mlEdges     map[uuid.UUID]domain.MLProvenanceEdge
	workers     map[string]domain.Worker
}

func newFakeProjectionSource() *fakeProjectionSource {
	return &fakeProjectionSource{
		services:    map[uuid.UUID]domain.Service{},
		envs:        map[uuid.UUID]domain.Environment{},
		states:      map[string]domain.EnvironmentServiceState{},
		builds:      map[uuid.UUID]domain.Build{},
		artifacts:   map[uuid.UUID]domain.Artifact{},
		intents:     map[uuid.UUID]domain.DeploymentIntent{},
		runs:        map[uuid.UUID]domain.DeploymentRun{},
		policies:    map[uuid.UUID]domain.DeploymentPolicy{},
		llmRoutes:   map[uuid.UUID]domain.LLMRoute{},
		llmStates:   map[string]domain.LLMRouteState{},
		llmIntents:  map[uuid.UUID]domain.LLMDeploymentIntent{},
		llmRuns:     map[uuid.UUID]domain.LLMDeploymentRun{},
		mlModels:    map[uuid.UUID]domain.MLModel{},
		mlVersions:  map[uuid.UUID]domain.MLModelVersion{},
		mlEndpoints: map[uuid.UUID]domain.MLInferenceEndpoint{},
		mlStates:    map[string]domain.MLInferenceState{},
		mlArtifacts: map[uuid.UUID]domain.MLArtifactRef{},
		mlEdges:     map[uuid.UUID]domain.MLProvenanceEdge{},
		workers:     map[string]domain.Worker{},
	}
}

func (s *fakeProjectionSource) ListServices(context.Context) ([]domain.Service, error) {
	out := make([]domain.Service, 0, len(s.services))
	for _, svc := range s.services {
		out = append(out, svc)
	}
	return out, nil
}

func (s *fakeProjectionSource) GetService(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	svc, ok := s.services[id]
	if !ok {
		return nil, nil
	}
	return &svc, nil
}

func (s *fakeProjectionSource) ListEnvironments(context.Context) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		out = append(out, env)
	}
	return out, nil
}

func (s *fakeProjectionSource) GetEnvironment(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	env, ok := s.envs[id]
	if !ok {
		return nil, nil
	}
	return &env, nil
}

func (s *fakeProjectionSource) ListAllStates(context.Context) ([]domain.EnvironmentServiceState, error) {
	out := make([]domain.EnvironmentServiceState, 0, len(s.states))
	for _, state := range s.states {
		out = append(out, state)
	}
	return out, nil
}

func (s *fakeProjectionSource) GetEnvironmentServiceState(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, ok := s.states[stateKeyForTest(serviceID, envID)]
	if !ok {
		return nil, nil
	}
	return &state, nil
}

func (s *fakeProjectionSource) GetBuild(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	build, ok := s.builds[id]
	if !ok {
		return nil, nil
	}
	return &build, nil
}

func (s *fakeProjectionSource) ListBuilds(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	out := []domain.Build{}
	for _, build := range s.builds {
		if build.ServiceID == serviceID {
			out = append(out, build)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetArtifact(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	artifact, ok := s.artifacts[id]
	if !ok {
		return nil, nil
	}
	return &artifact, nil
}

func (s *fakeProjectionSource) ListArtifacts(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	out := []domain.Artifact{}
	for _, artifact := range s.artifacts {
		if artifact.ServiceID == serviceID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := s.intents[id]
	if !ok {
		return nil, nil
	}
	return &intent, nil
}

func (s *fakeProjectionSource) ListDeploymentIntents(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	out := []domain.DeploymentIntent{}
	for _, intent := range s.intents {
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			out = append(out, intent)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetDeploymentRun(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	run, ok := s.runs[id]
	if !ok {
		return nil, nil
	}
	return &run, nil
}

func (s *fakeProjectionSource) ListDeploymentRuns(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	out := []domain.DeploymentRun{}
	for _, run := range s.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) ListPolicies(_ context.Context, enabledOnly bool) ([]domain.DeploymentPolicy, error) {
	out := []domain.DeploymentPolicy{}
	for _, policy := range s.policies {
		if !enabledOnly || policy.Enabled {
			out = append(out, policy)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetPolicy(_ context.Context, id uuid.UUID) (*domain.DeploymentPolicy, error) {
	policy, ok := s.policies[id]
	if !ok {
		return nil, nil
	}
	return &policy, nil
}

func (s *fakeProjectionSource) ListLLMRoutes(_ context.Context, limit, offset int) ([]domain.LLMRoute, error) {
	out := make([]domain.LLMRoute, 0, len(s.llmRoutes))
	for _, route := range s.llmRoutes {
		out = append(out, route)
	}
	if limit <= 0 {
		return out, nil
	}
	if offset >= len(out) {
		return nil, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (s *fakeProjectionSource) GetLLMRoute(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	route, ok := s.llmRoutes[id]
	if !ok {
		return nil, nil
	}
	return &route, nil
}

func (s *fakeProjectionSource) ListAllLLMRouteStates(context.Context) ([]domain.LLMRouteState, error) {
	out := make([]domain.LLMRouteState, 0, len(s.llmStates))
	for _, state := range s.llmStates {
		out = append(out, state)
	}
	return out, nil
}

func (s *fakeProjectionSource) GetLLMRouteState(_ context.Context, routeID, envID uuid.UUID) (*domain.LLMRouteState, error) {
	state, ok := s.llmStates[stateKeyForTest(routeID, envID)]
	if !ok {
		return nil, nil
	}
	return &state, nil
}

func (s *fakeProjectionSource) GetLLMDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	intent, ok := s.llmIntents[id]
	if !ok {
		return nil, nil
	}
	return &intent, nil
}

func (s *fakeProjectionSource) GetLLMDeploymentRun(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentRun, error) {
	run, ok := s.llmRuns[id]
	if !ok {
		return nil, nil
	}
	return &run, nil
}

func (s *fakeProjectionSource) ListModels(_ context.Context, _ domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error) {
	out := make([]domain.MLModel, 0, len(s.mlModels))
	for _, model := range s.mlModels {
		out = append(out, model)
	}
	if limit <= 0 || offset >= len(out) {
		if offset >= len(out) {
			return nil, nil
		}
		return out, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], nil
}

func (s *fakeProjectionSource) GetModel(_ context.Context, id uuid.UUID) (*domain.MLModel, error) {
	model, ok := s.mlModels[id]
	if !ok {
		return nil, nil
	}
	return &model, nil
}

func (s *fakeProjectionSource) GetModelBySlug(_ context.Context, slug string) (*domain.MLModel, error) {
	for _, model := range s.mlModels {
		if model.Slug == slug {
			return &model, nil
		}
	}
	return nil, nil
}

func (s *fakeProjectionSource) ListModelVersions(_ context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error) {
	out := []domain.MLModelVersion{}
	for _, version := range s.mlVersions {
		if version.ModelID == modelID {
			out = append(out, version)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	version, ok := s.mlVersions[id]
	if !ok {
		return nil, nil
	}
	return &version, nil
}

func (s *fakeProjectionSource) GetArtifactRef(_ context.Context, id uuid.UUID) (*domain.MLArtifactRef, error) {
	artifact, ok := s.mlArtifacts[id]
	if !ok {
		return nil, nil
	}
	return &artifact, nil
}

func (s *fakeProjectionSource) ListArtifactRefsByModelVersion(_ context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error) {
	out := []domain.MLArtifactRef{}
	for _, artifact := range s.mlArtifacts {
		if artifact.ModelVersionID != nil && *artifact.ModelVersionID == modelVersionID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) ListProvenanceEdgesByArtifact(_ context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error) {
	out := []domain.MLProvenanceEdge{}
	for _, edge := range s.mlEdges {
		if (edge.FromArtifactID != nil && *edge.FromArtifactID == artifactID) || (edge.ToArtifactID != nil && *edge.ToArtifactID == artifactID) {
			out = append(out, edge)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	endpoint, ok := s.mlEndpoints[id]
	if !ok {
		return nil, nil
	}
	return &endpoint, nil
}

func (s *fakeProjectionSource) ListInferenceEndpoints(_ context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error) {
	out := []domain.MLInferenceEndpoint{}
	for _, endpoint := range s.mlEndpoints {
		if envID == uuid.Nil || endpoint.EnvironmentID == envID {
			out = append(out, endpoint)
		}
	}
	return out, nil
}

func (s *fakeProjectionSource) GetMLDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	return nil, nil
}

func (s *fakeProjectionSource) GetMLDeploymentRun(_ context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error) {
	return nil, nil
}

func (s *fakeProjectionSource) GetInferenceState(_ context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	state, ok := s.mlStates[stateKeyForTest(endpointID, envID)]
	if !ok {
		return nil, nil
	}
	return &state, nil
}

func (s *fakeProjectionSource) ListInferenceStates(context.Context) ([]domain.MLInferenceState, error) {
	out := make([]domain.MLInferenceState, 0, len(s.mlStates))
	for _, state := range s.mlStates {
		out = append(out, state)
	}
	return out, nil
}

func (s *fakeProjectionSource) List(_ context.Context, status string, limit int) ([]domain.Worker, error) {
	out := make([]domain.Worker, 0, len(s.workers))
	for _, worker := range s.workers {
		if status == "" || string(worker.Status) == status {
			out = append(out, worker)
		}
	}
	return out, nil
}

func TestProjectorPublishesSystemDiscoverySnapshot(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"ws://localhost:3000/relay"}
	cfg.Nostr.BrowserEncryptedRequestRelays = []string{"wss://requests.example"}
	cfg.Nostr.EncryptedRequestRelays = []string{"wss://requests-backend.example"}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	discovery := assertOneSignedKind(t, sink, KindSystemDiscovery)
	assertTag(t, discovery, "d", "bahia-system-v1")
	assertJSONField(t, discovery.Content, "schema", "bahia.system-discovery.v1")
	browserSet := assertOneRelaySet(t, sink, "bahia-browser-v1")
	assertTag(t, browserSet, "relay", "ws://localhost:3000/relay")
	requestSet := assertOneRelaySet(t, sink, "bahia-requests-v1")
	assertTag(t, requestSet, "relay", "wss://requests.example")
	serviceSet := assertOneRelaySet(t, sink, "bahia-service-v1")
	assertTag(t, serviceSet, "relay", "ws://localhost:3000/relay")
}

func TestProjectorRepublishesSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{
		ID:            serviceID,
		Name:          "api",
		ArtifactRepo:  "ghcr.io/openagents/api",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeDocker,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	source.envs[envID] = domain.Environment{
		ID:             envID,
		Name:           "prod",
		DeployStrategy: domain.DeployStrategyReplace,
		Protected:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		DesiredArtifactID:   &artifactID,
		DesiredIntentID:     &intentID,
		LastSuccessfulRunID: &runID,
		DriftStatus:         domain.DriftStatusInSync,
		UpdatedAt:           now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	assertOneSignedKind(t, sink, KindServiceRegistry)
	assertOneSignedKind(t, sink, KindEnvironmentRegistry)
	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	assertTag(t, stateEvent, "service", serviceID.String())
	assertTag(t, stateEvent, "environment", envID.String())
	assertTag(t, stateEvent, "artifact", artifactID.String())
	assertTag(t, stateEvent, "intent", intentID.String())
	assertTag(t, stateEvent, "run", runID.String())
	assertJSONField(t, stateEvent.Content, "deleted", false)
}

func TestProjectorPublishesMLReadModelSnapshot(t *testing.T) {
	ctx := context.Background()
	modelID := uuid.New()
	versionID := uuid.New()
	envID := uuid.New()
	endpointID := uuid.New()
	artifactID := uuid.New()
	source := newFakeProjectionSource()
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod"}
	source.mlModels[modelID] = domain.MLModel{ID: modelID, Slug: "qwen", Name: "Qwen", Modalities: []string{"text"}, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}}
	source.mlVersions[versionID] = domain.MLModelVersion{ID: versionID, ModelID: modelID, Version: "v1", RuntimeRequirements: domain.MLRuntimeRequirements{PreferredRuntimes: []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM}, RequiredFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors}}}
	source.mlEndpoints[endpointID] = domain.MLInferenceEndpoint{ID: endpointID, Name: "chat", EnvironmentID: envID, TaskKinds: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Protocol: "openai-compatible"}
	source.mlStates[stateKeyForTest(endpointID, envID)] = domain.MLInferenceState{EndpointID: endpointID, EnvironmentID: envID, DesiredModelVersionID: &versionID, DriftStatus: domain.DriftStatusInSync, GatewayStatus: domain.GatewayRouteStatusSynced, RuntimeKind: domain.MLRuntimeKindVLLM}
	source.mlArtifacts[artifactID] = domain.MLArtifactRef{ID: artifactID, ModelVersionID: &versionID, Kind: domain.MLArtifactKindModel, Format: domain.MLArtifactFormatSafeTensors, URI: "hf://qwen", SHA256: "abc123"}
	source.workers["worker-pk"] = domain.Worker{PubKey: "worker-pk", Status: domain.WorkerStatusOnline, MLCapabilities: domain.WorkerMLCapabilities{Runtimes: []domain.MLRuntimeKind{domain.MLRuntimeKindVLLM}, ArtifactFormats: []domain.MLArtifactFormat{domain.MLArtifactFormatSafeTensors}, Tasks: []domain.MLTaskKind{domain.MLTaskKindChatCompletions}, Accelerators: []string{"gpu_nvidia_cuda"}}}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop(), WithMLProjectionSource(source), WithWorkerProjectionSource(source))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	model := assertOneSignedKind(t, sink, KindMLModelRegistry)
	assertTag(t, model, "d", "model:qwen")
	assertTag(t, model, "task", "chat_completions")
	version := assertOneSignedKind(t, sink, KindMLModelVersionRegistry)
	assertTag(t, version, "d", "model-version:qwen:v1")
	assertTag(t, version, "runtime", "vllm")
	endpoint := assertOneSignedKind(t, sink, KindMLInferenceEndpointRegistry)
	assertTag(t, endpoint, "d", "endpoint:chat:prod")
	state := assertOneSignedKind(t, sink, KindMLInferenceEndpointState)
	assertTag(t, state, "d", "endpoint-state:chat:prod")
	provenance := assertOneSignedKind(t, sink, KindMLArtifactProvenanceGraph)
	assertTag(t, provenance, "d", "artifact:abc123")
	capability := assertOneSignedKind(t, sink, KindMLRuntimeCapabilityProfile)
	assertTag(t, capability, "d", "worker:worker-pk:ai-capability")
	assertTag(t, capability, "runtime", "vllm")
}

func TestProjectorPublishesAuditAndReadModelsForRepresentativeMutations(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{
		ID:            serviceID,
		Name:          "worker",
		ArtifactRepo:  "ghcr.io/openagents/worker",
		DefaultBranch: "main",
		RuntimeType:   domain.RuntimeTypeCompose,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	source.intents[intentID] = domain.DeploymentIntent{
		ID:            intentID,
		ServiceID:     serviceID,
		EnvironmentID: envID,
		ArtifactID:    artifactID,
		Status:        domain.IntentStatusDeploying,
	}
	source.runs[runID] = domain.DeploymentRun{ID: runID, DeploymentIntentID: intentID, Status: domain.RunStatusSucceeded}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		DesiredArtifactID:   &artifactID,
		DesiredIntentID:     &intentID,
		LastSuccessfulRunID: &runID,
		DriftStatus:         domain.DriftStatusInSync,
		UpdatedAt:           now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())

	projector.handleEvent(ctx, events.Event{
		Type:     events.EventServiceCreated,
		EntityID: serviceID.String(),
		Data:     events.ResourceData{ServiceID: serviceID.String()},
	})
	projector.handleEvent(ctx, events.Event{
		Type:     events.EventDeploymentRunStatusChanged,
		EntityID: runID.String(),
		Data:     events.ResourceData{RunID: runID.String(), IntentID: intentID.String()},
	})

	assertOneSignedKind(t, sink, KindServiceRegistryAudit)
	assertOneSignedKind(t, sink, KindServiceRegistry)
	assertOneSignedKind(t, sink, KindDeploymentRunAudit)
	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	assertTag(t, stateEvent, "service", serviceID.String())
	assertTag(t, stateEvent, "environment", envID.String())
	assertTag(t, stateEvent, "artifact", artifactID.String())
	assertTag(t, stateEvent, "intent", intentID.String())
	assertTag(t, stateEvent, "run", runID.String())
}

func TestProjectorRepublishesLLMRouteAndState(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()

	source := newFakeProjectionSource()
	source.llmRoutes[routeID] = domain.LLMRoute{ID: routeID, Name: "chat", GatewayConfig: &domain.LLMGatewayRouteConfig{PublicModel: "chat-public"}, CreatedAt: now, UpdatedAt: now}
	source.llmStates[stateKeyForTest(routeID, envID)] = domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &releaseID, DesiredIntentID: &intentID, ActiveRunID: &runID, DriftStatus: domain.DriftStatusInSync, GatewayStatus: domain.GatewayRouteStatusSynced, BackendKind: domain.LLMBackendKindVLLM, BackendHealth: domain.HealthStatusHealthy, UpdatedAt: now}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop(), WithLLMProjectionSource(source))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	routeEvent := assertOneSignedKind(t, sink, KindLLMRouteRegistry)
	assertTag(t, routeEvent, "route", routeID.String())
	assertTag(t, routeEvent, "model", "chat-public")
	stateEvent := assertOneSignedKind(t, sink, KindLLMRouteState)
	assertTag(t, stateEvent, "route", routeID.String())
	assertTag(t, stateEvent, "environment", envID.String())
	assertTag(t, stateEvent, "release", releaseID.String())
	assertTag(t, stateEvent, "intent", intentID.String())
	assertTag(t, stateEvent, "run", runID.String())
	assertTag(t, stateEvent, "gateway_status", string(domain.GatewayRouteStatusSynced))
}

func TestProjectorPublishesLLMAuditAndStateFromRunEvent(t *testing.T) {
	ctx := context.Background()
	routeID := uuid.New()
	envID := uuid.New()
	releaseID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()
	source := newFakeProjectionSource()
	source.llmIntents[intentID] = domain.LLMDeploymentIntent{ID: intentID, RouteID: routeID, EnvironmentID: envID, ReleaseID: releaseID}
	source.llmRuns[runID] = domain.LLMDeploymentRun{ID: runID, DeploymentIntentID: intentID, Status: domain.RunStatusRunning}
	source.llmStates[stateKeyForTest(routeID, envID)] = domain.LLMRouteState{RouteID: routeID, EnvironmentID: envID, DesiredReleaseID: &releaseID, DesiredIntentID: &intentID, ActiveRunID: &runID, DriftStatus: domain.DriftStatusDeploying, GatewayStatus: domain.GatewayRouteStatusPending, UpdatedAt: time.Now().UTC()}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop(), WithLLMProjectionSource(source))
	projector.handleEvent(ctx, events.Event{Type: events.EventLLMDeploymentRunStatusChanged, EntityID: runID.String(), Data: events.ResourceData{RunID: runID.String()}})

	audit := assertOneSignedKind(t, sink, KindLLMRunAudit)
	assertTag(t, audit, "run", runID.String())
	stateEvent := assertOneSignedKind(t, sink, KindLLMRouteState)
	assertTag(t, stateEvent, "route", routeID.String())
	assertTag(t, stateEvent, "environment", envID.String())
}

func TestProjectorPublishesStateTombstoneForDeletedState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	envID := uuid.New()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	projector.handleEvent(ctx, events.Event{
		Type:     events.EventEnvironmentServiceStateChanged,
		EntityID: stateKeyForTest(serviceID, envID),
		Data: events.ResourceData{
			ServiceID:     serviceID.String(),
			EnvironmentID: envID.String(),
			Deleted:       true,
		},
	})

	assertOneSignedKind(t, sink, KindStateChangedAudit)
	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	assertTag(t, stateEvent, "service", serviceID.String())
	assertTag(t, stateEvent, "environment", envID.String())
	assertTag(t, stateEvent, "deleted", "true")
	assertJSONField(t, stateEvent.Content, "deleted", true)
}

func projectorTestConfig() config.NostrConfig {
	return config.NostrConfig{
		PrivateKey:     projectorTestPrivateKey,
		PublishEnabled: true,
	}
}

func assertOneSignedKind(t *testing.T, sink *captureProjectionPublisher, kind int) gonostr.Event {
	t.Helper()
	events := sink.byKind(kind)
	if len(events) != 1 {
		t.Fatalf("expected one event of kind %d, got %d", kind, len(events))
	}
	ok, err := events[0].CheckSignature()
	if err != nil || !ok {
		t.Fatalf("event kind %d has invalid signature: ok=%v err=%v", kind, ok, err)
	}
	return events[0]
}

func assertOneRelaySet(t *testing.T, sink *captureProjectionPublisher, dTag string) gonostr.Event {
	t.Helper()
	var matched []gonostr.Event
	for _, ev := range sink.byKind(30002) {
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "d" && tag[1] == dTag {
				matched = append(matched, ev)
			}
		}
	}
	if len(matched) != 1 {
		t.Fatalf("expected one relay set %s, got %d", dTag, len(matched))
	}
	ok, err := matched[0].CheckSignature()
	if err != nil || !ok {
		t.Fatalf("relay set %s has invalid signature: ok=%v err=%v", dTag, ok, err)
	}
	return matched[0]
}

func assertTag(t *testing.T, ev gonostr.Event, key, value string) {
	t.Helper()
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return
		}
	}
	t.Fatalf("event kind %d missing tag %s=%s; tags=%v", ev.Kind, key, value, ev.Tags)
}

func assertJSONField(t *testing.T, content, key string, want any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if got[key] != want {
		t.Fatalf("content[%q] = %#v, want %#v", key, got[key], want)
	}
}

func stateKeyForTest(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}
