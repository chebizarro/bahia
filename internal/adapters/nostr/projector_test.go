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
	services   map[uuid.UUID]domain.Service
	envs       map[uuid.UUID]domain.Environment
	states     map[string]domain.EnvironmentServiceState
	builds     map[uuid.UUID]domain.Build
	artifacts  map[uuid.UUID]domain.Artifact
	intents    map[uuid.UUID]domain.DeploymentIntent
	runs       map[uuid.UUID]domain.DeploymentRun
	policies   map[uuid.UUID]domain.DeploymentPolicy
	llmRoutes  map[uuid.UUID]domain.LLMRoute
	llmStates  map[string]domain.LLMRouteState
	llmIntents map[uuid.UUID]domain.LLMDeploymentIntent
	llmRuns    map[uuid.UUID]domain.LLMDeploymentRun
}

func newFakeProjectionSource() *fakeProjectionSource {
	return &fakeProjectionSource{
		services:   map[uuid.UUID]domain.Service{},
		envs:       map[uuid.UUID]domain.Environment{},
		states:     map[string]domain.EnvironmentServiceState{},
		builds:     map[uuid.UUID]domain.Build{},
		artifacts:  map[uuid.UUID]domain.Artifact{},
		intents:    map[uuid.UUID]domain.DeploymentIntent{},
		runs:       map[uuid.UUID]domain.DeploymentRun{},
		policies:   map[uuid.UUID]domain.DeploymentPolicy{},
		llmRoutes:  map[uuid.UUID]domain.LLMRoute{},
		llmStates:  map[string]domain.LLMRouteState{},
		llmIntents: map[uuid.UUID]domain.LLMDeploymentIntent{},
		llmRuns:    map[uuid.UUID]domain.LLMDeploymentRun{},
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

func TestProjectorRepublishSnapshotRepairsReadModels(t *testing.T) {
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
