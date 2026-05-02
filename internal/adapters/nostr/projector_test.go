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
	services map[uuid.UUID]domain.Service
	envs     map[uuid.UUID]domain.Environment
	states   map[string]domain.EnvironmentServiceState
	intents  map[uuid.UUID]domain.DeploymentIntent
	runs     map[uuid.UUID]domain.DeploymentRun
}

func newFakeProjectionSource() *fakeProjectionSource {
	return &fakeProjectionSource{
		services: map[uuid.UUID]domain.Service{},
		envs:     map[uuid.UUID]domain.Environment{},
		states:   map[string]domain.EnvironmentServiceState{},
		intents:  map[uuid.UUID]domain.DeploymentIntent{},
		runs:     map[uuid.UUID]domain.DeploymentRun{},
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

func (s *fakeProjectionSource) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := s.intents[id]
	if !ok {
		return nil, nil
	}
	return &intent, nil
}

func (s *fakeProjectionSource) GetDeploymentRun(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	run, ok := s.runs[id]
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
