package nostr

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
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
		if ev.Kind == kind || (isCanonicalRuntimeKind(ev.Kind) && hasTag(ev.Tags, "legacy_kind", strconv.Itoa(kind))) {
			out = append(out, ev)
		}
	}
	return out
}

func isCanonicalRuntimeKind(kind int) bool {
	return kind == KindCASControlState || kind == KindCASAudit || kind == KindNIP38Status
}

func hasTag(tags gonostr.Tags, key, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}

type fakeProjectionSource struct {
	services     map[uuid.UUID]domain.Service
	envs         map[uuid.UUID]domain.Environment
	states       map[string]domain.EnvironmentServiceState
	observations map[uuid.UUID]domain.RuntimeObservation
	builds       map[uuid.UUID]domain.Build
	artifacts    map[uuid.UUID]domain.Artifact
	intents      map[uuid.UUID]domain.DeploymentIntent
	runs         map[uuid.UUID]domain.DeploymentRun
	policies     map[uuid.UUID]domain.DeploymentPolicy
	llmRoutes    map[uuid.UUID]domain.LLMRoute
	llmStates    map[string]domain.LLMRouteState
	llmIntents   map[uuid.UUID]domain.LLMDeploymentIntent
	llmRuns      map[uuid.UUID]domain.LLMDeploymentRun
	mlModels     map[uuid.UUID]domain.MLModel
	mlVersions   map[uuid.UUID]domain.MLModelVersion
	mlEndpoints  map[uuid.UUID]domain.MLInferenceEndpoint
	mlStates     map[string]domain.MLInferenceState
	mlArtifacts  map[uuid.UUID]domain.MLArtifactRef
	mlEdges      map[uuid.UUID]domain.MLProvenanceEdge
	workers      map[string]domain.Worker
}

func newFakeProjectionSource() *fakeProjectionSource {
	return &fakeProjectionSource{
		services:     map[uuid.UUID]domain.Service{},
		envs:         map[uuid.UUID]domain.Environment{},
		states:       map[string]domain.EnvironmentServiceState{},
		observations: map[uuid.UUID]domain.RuntimeObservation{},
		builds:       map[uuid.UUID]domain.Build{},
		artifacts:    map[uuid.UUID]domain.Artifact{},
		intents:      map[uuid.UUID]domain.DeploymentIntent{},
		runs:         map[uuid.UUID]domain.DeploymentRun{},
		policies:     map[uuid.UUID]domain.DeploymentPolicy{},
		llmRoutes:    map[uuid.UUID]domain.LLMRoute{},
		llmStates:    map[string]domain.LLMRouteState{},
		llmIntents:   map[uuid.UUID]domain.LLMDeploymentIntent{},
		llmRuns:      map[uuid.UUID]domain.LLMDeploymentRun{},
		mlModels:     map[uuid.UUID]domain.MLModel{},
		mlVersions:   map[uuid.UUID]domain.MLModelVersion{},
		mlEndpoints:  map[uuid.UUID]domain.MLInferenceEndpoint{},
		mlStates:     map[string]domain.MLInferenceState{},
		mlArtifacts:  map[uuid.UUID]domain.MLArtifactRef{},
		mlEdges:      map[uuid.UUID]domain.MLProvenanceEdge{},
		workers:      map[string]domain.Worker{},
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

func (s *fakeProjectionSource) GetLatestObservation(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	var latest *domain.RuntimeObservation
	for _, obs := range s.observations {
		if obs.ServiceID != serviceID || obs.EnvironmentID != envID {
			continue
		}
		candidate := obs
		if latest == nil || candidate.ObservedAt.After(latest.ObservedAt) {
			latest = &candidate
		}
	}
	return latest, nil
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

type fakeDNSProjectionSource struct {
	endpoints []domain.DNSEndpoint
}

func (s *fakeDNSProjectionSource) ListDNSEndpoints(context.Context) ([]domain.DNSEndpoint, error) {
	return append([]domain.DNSEndpoint(nil), s.endpoints...), nil
}

type fakeDNSZoneProjectionSource struct {
	zones []domain.DNSZone
}

func (s *fakeDNSZoneProjectionSource) ListDNSZones() []domain.DNSZone {
	return append([]domain.DNSZone(nil), s.zones...)
}

type fakeDNSBackendProjectionSource struct {
	backends []domain.DNSBackendState
}

func (s *fakeDNSBackendProjectionSource) ListDNSBackendStates(context.Context) []domain.DNSBackendState {
	return append([]domain.DNSBackendState(nil), s.backends...)
}

type fakeDNSPolicyProjectionSource struct {
	policies []domain.DNSPolicy
}

func (s *fakeDNSPolicyProjectionSource) ListEnabledDNSPolicies(context.Context) ([]domain.DNSPolicy, error) {
	out := make([]domain.DNSPolicy, 0, len(s.policies))
	for _, policy := range s.policies {
		if policy.Enabled {
			out = append(out, policy)
		}
	}
	return out, nil
}

func TestProjectorPublishesDNSZoneStateSnapshot(t *testing.T) {
	ctx := context.Background()
	zoneSource := &fakeDNSZoneProjectionSource{zones: []domain.DNSZone{{
		Name:       "prod.cascadia",
		Visibility: domain.ZoneVisibilityInternal,
		BackendRef: "fs-primary",
		TTL:        60,
	}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSZoneProjectionSource(zoneSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	zoneEvent := assertOneSignedKind(t, sink, KindDNSZoneState)
	assertTag(t, zoneEvent, "d", "zone:prod.cascadia")
	assertTag(t, zoneEvent, "zone", "prod.cascadia")
	assertTag(t, zoneEvent, "backend", "fs-primary")
	assertTag(t, zoneEvent, "visibility", "internal")
	assertTag(t, zoneEvent, "t", "dns-zone")
	assertTag(t, zoneEvent, "t", "bahia")
	assertJSONField(t, zoneEvent.Content, "name", "prod.cascadia")
	assertJSONField(t, zoneEvent.Content, "visibility", "internal")
	assertJSONField(t, zoneEvent.Content, "backend_ref", "fs-primary")
	assertJSONField(t, zoneEvent.Content, "ttl", float64(60))
	assertJSONField(t, zoneEvent.Content, "deleted", false)
}

func TestProjectorPublishesDNSBackendStateSnapshot(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	lastSync := now.Add(-time.Minute)
	backendSource := &fakeDNSBackendProjectionSource{backends: []domain.DNSBackendState{{
		Ref:        "fs-primary",
		Type:       domain.DNSBackendTypeFilesystem,
		Health:     domain.HealthStatusHealthy,
		ZoneRefs:   []string{"prod.cascadia"},
		LastSyncAt: &lastSync,
		UpdatedAt:  now,
	}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSBackendProjectionSource(backendSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	backendEvent := assertOneSignedKind(t, sink, KindDNSBackendState)
	assertTag(t, backendEvent, "d", "dnsbackend:fs-primary")
	assertTag(t, backendEvent, "backend", "fs-primary")
	assertTag(t, backendEvent, "type", "filesystem")
	assertTag(t, backendEvent, "health", "healthy")
	assertTag(t, backendEvent, "zone", "prod.cascadia")
	assertTag(t, backendEvent, "t", "dns-backend")
	assertTag(t, backendEvent, "t", "bahia")
	assertJSONField(t, backendEvent.Content, "ref", "fs-primary")
	assertJSONField(t, backendEvent.Content, "type", "filesystem")
	assertJSONField(t, backendEvent.Content, "health", "healthy")
	assertJSONField(t, backendEvent.Content, "deleted", false)
}

func TestProjectorPublishesDNSPolicyStateSnapshotAndTombstone(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	policyID := uuid.New()
	zoneID := uuid.New()
	ttl := 120
	policySource := &fakeDNSPolicyProjectionSource{policies: []domain.DNSPolicy{{
		ID:        policyID,
		Name:      "latency-aware",
		ZoneID:    &zoneID,
		Rules:     []domain.DNSPolicyRule{{Match: domain.DNSPolicyMatch{Environment: "prod"}, Action: domain.DNSPolicyAction{Visibility: domain.ZoneVisibilityInternal, TTLOverride: &ttl}}},
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSPolicyProjectionSource(policySource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	policyEvent := assertOneSignedKind(t, sink, KindDNSPolicyState)
	assertTag(t, policyEvent, "d", "dnspolicy:"+policyID.String())
	assertTag(t, policyEvent, "policy", policyID.String())
	assertTag(t, policyEvent, "zone", zoneID.String())
	assertTag(t, policyEvent, "enabled", "true")
	assertTag(t, policyEvent, "t", "dns-policy")
	assertTag(t, policyEvent, "t", "bahia")
	assertJSONField(t, policyEvent.Content, "id", policyID.String())
	assertJSONField(t, policyEvent.Content, "name", "latency-aware")
	assertJSONField(t, policyEvent.Content, "zone_id", zoneID.String())
	assertJSONField(t, policyEvent.Content, "enabled", true)
	assertJSONField(t, policyEvent.Content, "deleted", false)

	policySource.policies = nil
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after removal: %v", err)
	}
	policyEvents := sink.byKind(KindDNSPolicyState)
	if len(policyEvents) != 2 {
		t.Fatalf("expected policy event and tombstone, got %d", len(policyEvents))
	}
	tombstone := policyEvents[1]
	assertTag(t, tombstone, "d", "dnspolicy:"+policyID.String())
	assertTag(t, tombstone, "deleted", "true")
	assertTag(t, tombstone, "policy", policyID.String())
	assertJSONField(t, tombstone.Content, "deleted", true)
	assertJSONField(t, tombstone.Content, "id", policyID.String())
}

func TestProjectorPublishesDNSZoneAndBackendTombstones(t *testing.T) {
	ctx := context.Background()
	zoneSource := &fakeDNSZoneProjectionSource{zones: []domain.DNSZone{{Name: "prod.cascadia", Visibility: domain.ZoneVisibilityInternal, BackendRef: "fs-primary", TTL: 60}}}
	backendSource := &fakeDNSBackendProjectionSource{backends: []domain.DNSBackendState{{Ref: "fs-primary", Type: domain.DNSBackendTypeFilesystem, Health: domain.HealthStatusHealthy, ZoneRefs: []string{"prod.cascadia"}, UpdatedAt: time.Now().UTC()}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSZoneProjectionSource(zoneSource), WithDNSBackendProjectionSource(backendSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	zoneSource.zones = nil
	backendSource.backends = nil
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after removal: %v", err)
	}

	zoneEvents := sink.byKind(KindDNSZoneState)
	if len(zoneEvents) != 2 {
		t.Fatalf("expected zone event and tombstone, got %d", len(zoneEvents))
	}
	assertTag(t, zoneEvents[1], "d", "zone:prod.cascadia")
	assertTag(t, zoneEvents[1], "deleted", "true")
	assertJSONField(t, zoneEvents[1].Content, "deleted", true)

	backendEvents := sink.byKind(KindDNSBackendState)
	if len(backendEvents) != 2 {
		t.Fatalf("expected backend event and tombstone, got %d", len(backendEvents))
	}
	assertTag(t, backendEvents[1], "d", "dnsbackend:fs-primary")
	assertTag(t, backendEvents[1], "deleted", "true")
	assertJSONField(t, backendEvents[1].Content, "deleted", true)
}

func TestProjectorPublishesDNSAuditEvents(t *testing.T) {
	ctx := context.Background()
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())

	projector.handleEvent(ctx, events.Event{Type: eventDNSZoneSynced, EntityID: "prod.cascadia", Data: map[string]any{"zone": "prod.cascadia", "backend_ref": "fs-primary"}})
	projector.handleEvent(ctx, events.Event{Type: eventDNSRecordChanged, EntityID: "api.prod.cascadia", Data: map[string]any{"zone": "prod.cascadia", "fqdn": "api.prod.cascadia", "record_type": "A", "operation": "add"}})
	projector.handleEvent(ctx, events.Event{Type: eventDNSDriftDetected, EntityID: "prod.cascadia", Data: map[string]any{"zone": "prod.cascadia", "backend_ref": "fs-primary"}})
	projector.handleEvent(ctx, events.Event{Type: eventDNSEndpointRegistered, EntityID: "endpoint:service:api:prod", Data: map[string]any{"source_coordinate": "endpoint:service:api:prod", "fqdn": "api.prod.cascadia"}})
	projector.handleEvent(ctx, events.Event{Type: eventDNSEndpointDeregistered, EntityID: "endpoint:service:api:prod", Data: map[string]any{"source_coordinate": "endpoint:service:api:prod", "fqdn": "api.prod.cascadia"}})

	zoneSynced := assertOneSignedKind(t, sink, KindDNSZoneSyncedAudit)
	assertTag(t, zoneSynced, "event_type", "dns.zone_synced")
	assertTag(t, zoneSynced, "zone", "prod.cascadia")
	assertTag(t, zoneSynced, "backend", "fs-primary")
	recordChanged := assertOneSignedKind(t, sink, KindDNSRecordChangedAudit)
	assertTag(t, recordChanged, "fqdn", "api.prod.cascadia")
	assertTag(t, recordChanged, "record_type", "A")
	assertTag(t, recordChanged, "operation", "add")
	assertOneSignedKind(t, sink, KindDNSDriftDetectedAudit)
	assertOneSignedKind(t, sink, KindDNSEndpointRegisteredAudit)
	assertOneSignedKind(t, sink, KindDNSEndpointDeregisteredAudit)
}

func TestProjectorPublishesDNSEndpointSnapshotAndTombstone(t *testing.T) {
	ctx := context.Background()
	port := 8443
	dnsSource := &fakeDNSProjectionSource{endpoints: []domain.DNSEndpoint{{
		Family:      domain.DNSEndpointFamilyService,
		Name:        "api",
		Environment: "prod",
		Zone:        "prod.cascadia",
		FQDN:        "api.prod.cascadia",
		Protocol:    "https",
		Address:     "10.0.1.44",
		Port:        &port,
		Runtime:     string(domain.RuntimeTypeDocker),
		Health:      domain.HealthStatusHealthy,
		DriftStatus: domain.DriftStatusInSync,
		Source:      "test",
	}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSProjectionSource(dnsSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}
	endpointEvent := assertOneSignedKind(t, sink, KindDNSEndpointState)
	assertTag(t, endpointEvent, "d", "endpoint:service:api:prod")
	assertTag(t, endpointEvent, "family", "service")
	assertTag(t, endpointEvent, "environment", "prod")
	assertTag(t, endpointEvent, "health", "healthy")
	assertTag(t, endpointEvent, "runtime", "docker")
	assertTag(t, endpointEvent, "dns", "api.prod.cascadia")
	assertTag(t, endpointEvent, "addr", "10.0.1.44")
	assertTag(t, endpointEvent, "proto", "https")
	assertTag(t, endpointEvent, "port", "8443")
	assertTag(t, endpointEvent, "t", "dns-endpoint")
	assertTag(t, endpointEvent, "t", "bahia")
	assertNoTag(t, endpointEvent, "npub", "")
	assertNoTag(t, endpointEvent, "mesh", "")
	assertJSONField(t, endpointEvent.Content, "coordinate", "endpoint:service:api:prod")

	dnsSource.endpoints = nil
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot after removal: %v", err)
	}
	dnsEvents := sink.byKind(KindDNSEndpointState)
	if len(dnsEvents) != 2 {
		t.Fatalf("expected endpoint event and tombstone, got %d", len(dnsEvents))
	}
	tombstone := dnsEvents[1]
	assertTag(t, tombstone, "d", "endpoint:service:api:prod")
	assertTag(t, tombstone, "deleted", "true")
	assertTag(t, tombstone, "dns", "api.prod.cascadia")
	assertJSONField(t, tombstone.Content, "deleted", true)
	assertJSONField(t, tombstone.Content, "coordinate", "endpoint:service:api:prod")
}

func TestProjectorPublishesDNSEndpointFIPSTagsWhenWorkerPubkeyPresent(t *testing.T) {
	ctx := context.Background()
	port := 8000
	workerPubkey := "npub1workerpubkey"
	dnsSource := &fakeDNSProjectionSource{endpoints: []domain.DNSEndpoint{{
		Family:       domain.DNSEndpointFamilyWorker,
		Name:         "t7920-l40s",
		Zone:         "edge.cascadia",
		FQDN:         "t7920-l40s.edge.cascadia",
		Protocol:     "http",
		Address:      "10.0.1.45",
		Port:         &port,
		WorkerPubkey: workerPubkey,
		Health:       domain.HealthStatusHealthy,
		DriftStatus:  domain.DriftStatusInSync,
		Source:       "test",
	}}}
	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop(), WithDNSProjectionSource(dnsSource))

	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	endpointEvent := assertOneSignedKind(t, sink, KindDNSEndpointState)
	assertTag(t, endpointEvent, "d", "endpoint:worker:t7920-l40s")
	assertTag(t, endpointEvent, "worker", workerPubkey)
	assertTag(t, endpointEvent, "npub", workerPubkey)
	assertTag(t, endpointEvent, "mesh", "fips")
}

func TestProjectorSystemDiscoveryAdvertisesDNSOnlyWhenSourceConfigured(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = projectorTestPrivateKey
	cfg.Nostr.PublishEnabled = true
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"ws://localhost:3000/relay"}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(cfg.Nostr, newFakeProjectionSource(), sink, nil, zap.NewNop(), WithSystemDiscoveryConfig(cfg, true), WithDNSProjectionSource(&fakeDNSProjectionSource{}), WithDNSPolicyProjectionSource(&fakeDNSPolicyProjectionSource{}))
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	discovery := assertOneSignedKind(t, sink, KindSystemDiscovery)
	var payload map[string]any
	if err := json.Unmarshal([]byte(discovery.Content), &payload); err != nil {
		t.Fatalf("unmarshal discovery: %v", err)
	}
	controlPlane, ok := payload["control_plane"].(map[string]any)
	if !ok {
		t.Fatalf("control_plane missing from discovery: %#v", payload["control_plane"])
	}
	capabilities, ok := controlPlane["capabilities"].([]any)
	if !ok {
		t.Fatalf("control_plane.capabilities missing: %#v", controlPlane["capabilities"])
	}
	foundCapability := false
	for _, capability := range capabilities {
		if capability == "dns_endpoint_catalog" {
			foundCapability = true
		}
	}
	if !foundCapability {
		t.Fatalf("dns_endpoint_catalog capability missing: %#v", capabilities)
	}
	assertNoDiscoveryKeys(t, controlPlane, "request_kinds", "status_kinds", "result_kinds", "read_model_kinds", "legacy_read_model_kinds")
	assertDiscoveryKindMap(t, controlPlane, "transport_kinds", map[string]int{
		"contextvm_message":        kinds.ContextVMMessage,
		"contextvm_gift_wrap":      kinds.ContextVMGiftWrap,
		"contextvm_ephemeral_wrap": kinds.ContextVMEphemeralGiftWrap,
	})
	assertDiscoveryKindMap(t, controlPlane, "observable_kinds", map[string]int{
		"control_state": KindCASControlState,
		"status":        KindNIP38Status,
		"audit":         KindCASAudit,
	})
	assertDiscoveryKindMap(t, controlPlane, "announcement_kinds", map[string]int{
		"server":             kinds.ContextVMServerAnnouncement,
		"tools":              kinds.ContextVMToolsList,
		"resources":          kinds.ContextVMResourcesList,
		"resource_templates": kinds.ContextVMResourceTemplatesList,
		"prompts":            kinds.ContextVMPromptsList,
	})
	assertDiscoveryKindMap(t, controlPlane, "relay_kinds", map[string]int{
		"relay_set": kinds.RelaySetDiscovery,
		"nip65":     kinds.NIP65RelayList,
	})
	methods, ok := controlPlane["methods"].([]any)
	if !ok {
		t.Fatalf("control_plane.methods missing: %#v", controlPlane["methods"])
	}
	for _, method := range []string{"service/deploy", "service/rollback", "worker/cordon", "dns/zone-create", "ml/recipe-run", "ml/inference-deploy"} {
		assertDiscoveryStringContains(t, methods, method)
	}
	assertDiscoveryContainsNoLegacyKinds(t, payload)
}

func assertNoDiscoveryKeys(t *testing.T, value map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if got, ok := value[key]; ok {
			t.Fatalf("discovery should not advertise %s, got %#v", key, got)
		}
	}
}

func assertDiscoveryKindMap(t *testing.T, parent map[string]any, key string, want map[string]int) {
	t.Helper()
	got, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("control_plane.%s missing: %#v", key, parent[key])
	}
	for name, wantKind := range want {
		if got[name] != float64(wantKind) {
			t.Fatalf("control_plane.%s.%s = %#v, want %d", key, name, got[name], wantKind)
		}
	}
}

func assertDiscoveryStringContains(t *testing.T, values []any, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("discovery string list missing %q: %#v", want, values)
}

func assertDiscoveryContainsNoLegacyKinds(t *testing.T, value any) {
	t.Helper()
	assertDiscoveryContainsNoLegacyKindsAt(t, "payload", value)
}

func assertDiscoveryContainsNoLegacyKindsAt(t *testing.T, path string, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			assertDiscoveryContainsNoLegacyKindsAt(t, path+"."+key, child)
		}
	case []any:
		for i, child := range v {
			assertDiscoveryContainsNoLegacyKindsAt(t, path+"["+strconv.Itoa(i)+"]", child)
		}
	case float64:
		kind := int(v)
		if float64(kind) == v && isLegacyDiscoveryKind(kind) {
			t.Fatalf("discovery payload advertises legacy kind at %s: %d", path, kind)
		}
	}
}

func isLegacyDiscoveryKind(kind int) bool {
	return (kind >= 5960 && kind < 7000) || (kind >= 7960 && kind < 8000) || (kind >= 31900 && kind < 32000)
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

func assertNoTag(t *testing.T, ev gonostr.Event, key, value string) {
	t.Helper()
	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != key {
			continue
		}
		if value == "" || tag[1] == value {
			t.Fatalf("event kind %d unexpectedly had tag %s=%s; tags=%v", ev.Kind, key, tag[1], ev.Tags)
		}
	}
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

// ---------------------------------------------------------------------------
// Desired-state metadata enrichment tests (Item 8 — bahia-zu2p.7.2)
// ---------------------------------------------------------------------------

func TestProjectorStateCarriesDesiredStateMetadata(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	observationID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeCompose, CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID:            serviceID,
		EnvironmentID:        envID,
		DesiredArtifactID:    &artifactID,
		DesiredIntentID:      &intentID,
		CurrentObservationID: &observationID,
		DriftStatus:          domain.DriftStatusInSync,
		DesiredHash:          "sha256:abc123",
		DesiredRuntimeState: &domain.DesiredServiceSpec{
			SchemaVersion:    domain.DesiredStateSchemaVersion,
			ServiceID:        serviceID,
			EnvironmentID:    envID,
			ArtifactID:       artifactID,
			StableServiceKey: "api-prod",
			ImageRef:         "ghcr.io/org/api:v1",
			ComposeExtension: &domain.ComposeExtension{ProjectName: "bahia-prod"},
		},
		UpdatedAt: now,
	}
	source.observations[observationID] = domain.RuntimeObservation{
		ID:             observationID,
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		NormalizedHash: "sha256:observed123",
		ObservedAt:     now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	// Existing tags preserved
	assertTag(t, stateEvent, "service", serviceID.String())
	assertTag(t, stateEvent, "environment", envID.String())
	assertTag(t, stateEvent, "drift_status", "in_sync")
	// New desired-state tags
	assertTag(t, stateEvent, "desired_hash", "sha256:abc123")
	assertTag(t, stateEvent, "observed_hash", "sha256:observed123")
	// New desired-state content fields
	assertJSONField(t, stateEvent.Content, "desired_hash", "sha256:abc123")
	assertJSONField(t, stateEvent.Content, "observed_hash", "sha256:observed123")
	assertJSONField(t, stateEvent.Content, "renderer", "compose")
	assertJSONField(t, stateEvent.Content, "target", "api-prod")
}

func TestProjectorStateOmitsDesiredMetadataWhenAbsent(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "staging", CreatedAt: now, UpdatedAt: now}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID:     serviceID,
		EnvironmentID: envID,
		DriftStatus:   domain.DriftStatusUnknown,
		UpdatedAt:     now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	// No desired_hash tag when empty
	assertNoTag(t, stateEvent, "desired_hash", "")
	// Content should not carry these fields
	var content map[string]any
	if err := json.Unmarshal([]byte(stateEvent.Content), &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if _, ok := content["desired_hash"]; ok {
		t.Fatal("content should not have desired_hash when empty")
	}
	if _, ok := content["renderer"]; ok {
		t.Fatal("content should not have renderer when no desired state")
	}
}

func TestProjectorIntentRegistryCarriesDesiredHash(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}
	source.intents[intentID] = domain.DeploymentIntent{
		ID:            intentID,
		ServiceID:     serviceID,
		EnvironmentID: envID,
		ArtifactID:    artifactID,
		Status:        domain.IntentStatusDeploying,
		DesiredHash:   "sha256:intent-hash",
		DesiredState: &domain.DesiredServiceSpec{
			StableServiceKey: "api-prod",
			DockerExtension:  &domain.DockerExtension{},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID: serviceID, EnvironmentID: envID, DriftStatus: domain.DriftStatusUnknown, UpdatedAt: now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	intentEvent := assertOneSignedKind(t, sink, KindDeploymentIntentRegistry)
	assertTag(t, intentEvent, "desired_hash", "sha256:intent-hash")
	assertJSONField(t, intentEvent.Content, "desired_hash", "sha256:intent-hash")
	assertJSONField(t, intentEvent.Content, "renderer", "docker")
	assertJSONField(t, intentEvent.Content, "target", "api-prod")
}

func TestProjectorRunRegistryCarriesApplyMetadata(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()
	obsID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}
	source.intents[intentID] = domain.DeploymentIntent{ID: intentID, ServiceID: serviceID, EnvironmentID: envID, ArtifactID: artifactID, Status: domain.IntentStatusDeploying, CreatedAt: now, UpdatedAt: now}
	source.runs[runID] = domain.DeploymentRun{
		ID:                 runID,
		DeploymentIntentID: intentID,
		Status:             domain.RunStatusSucceeded,
		ApplyMetadata: map[string]any{
			"renderer":       "compose",
			"desired_hash":   "sha256:run-hash",
			"revision_hash":  "sha256:rev-hash",
			"target":         "api-prod",
			"apply_summary":  "recreated 1 service",
			"observation_id": obsID.String(),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	runEvent := assertOneSignedKind(t, sink, KindDeploymentRunRegistry)
	assertTag(t, runEvent, "renderer", "compose")
	assertJSONField(t, runEvent.Content, "renderer", "compose")
	assertJSONField(t, runEvent.Content, "desired_hash", "sha256:run-hash")
	assertJSONField(t, runEvent.Content, "revision_hash", "sha256:rev-hash")
	assertJSONField(t, runEvent.Content, "target", "api-prod")
	assertJSONField(t, runEvent.Content, "apply_summary", "recreated 1 service")
	assertJSONField(t, runEvent.Content, "observation_id", obsID.String())
}

func TestProjectorRunRegistryOmitsApplyMetadataWhenNil(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	intentID := uuid.New()
	runID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}
	source.intents[intentID] = domain.DeploymentIntent{ID: intentID, ServiceID: serviceID, EnvironmentID: envID, ArtifactID: artifactID, Status: domain.IntentStatusDeploying, CreatedAt: now, UpdatedAt: now}
	source.runs[runID] = domain.DeploymentRun{
		ID:                 runID,
		DeploymentIntentID: intentID,
		Status:             domain.RunStatusSucceeded,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	runEvent := assertOneSignedKind(t, sink, KindDeploymentRunRegistry)
	// No renderer tag when no apply_metadata
	assertNoTag(t, runEvent, "renderer", "")
	var content map[string]any
	if err := json.Unmarshal([]byte(runEvent.Content), &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if _, ok := content["renderer"]; ok {
		t.Fatal("content should not have renderer when apply_metadata is nil")
	}
}

func TestProjectorStateSecretPlaintextNeverProjected(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	secretID := uuid.New()

	source := newFakeProjectionSource()
	source.services[serviceID] = domain.Service{ID: serviceID, Name: "api", CreatedAt: now, UpdatedAt: now}
	source.envs[envID] = domain.Environment{ID: envID, Name: "prod", CreatedAt: now, UpdatedAt: now}
	source.states[stateKeyForTest(serviceID, envID)] = domain.EnvironmentServiceState{
		ServiceID:     serviceID,
		EnvironmentID: envID,
		DriftStatus:   domain.DriftStatusInSync,
		DesiredHash:   "sha256:abc",
		DesiredRuntimeState: &domain.DesiredServiceSpec{
			SchemaVersion:    domain.DesiredStateSchemaVersion,
			ServiceID:        serviceID,
			EnvironmentID:    envID,
			ArtifactID:       artifactID,
			StableServiceKey: "api-prod",
			ImageRef:         "ghcr.io/org/api:v1",
			Env:              map[string]string{"APP_ENV": "production"},
			SecretRefs: []domain.DesiredSecretRef{
				{EnvVar: "DB_PASSWORD", Name: "DB_PASSWORD", SecretID: secretID, RedactedValue: "REDACTED(DB_PASSWORD)"},
			},
			ComposeExtension: &domain.ComposeExtension{ProjectName: "bahia-prod"},
		},
		UpdatedAt: now,
	}

	sink := &captureProjectionPublisher{}
	projector := NewProjector(projectorTestConfig(), source, sink, nil, zap.NewNop())
	if err := projector.RepublishSnapshot(ctx); err != nil {
		t.Fatalf("republish snapshot: %v", err)
	}

	stateEvent := assertOneSignedKind(t, sink, KindServiceState)
	// The content should carry sanitized metadata (renderer/target) but never
	// the DesiredRuntimeState with potential env values. publishState only
	// projects scalar metadata, not the full spec.
	var content map[string]any
	if err := json.Unmarshal([]byte(stateEvent.Content), &content); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	// Must NOT contain env or secret_refs in the projected event
	if _, ok := content["env"]; ok {
		t.Fatal("projected state must not contain env map")
	}
	if _, ok := content["secret_refs"]; ok {
		t.Fatal("projected state must not contain secret_refs")
	}
	if _, ok := content["desired_runtime_state"]; ok {
		t.Fatal("projected state must not contain full desired_runtime_state")
	}
	// Sanitized metadata should be present
	assertJSONField(t, stateEvent.Content, "renderer", "compose")
	assertJSONField(t, stateEvent.Content, "target", "api-prod")
}
