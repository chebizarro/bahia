package reconcile

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

// --- Mock repositories for testing ---

type mockServiceRepo struct {
	services map[uuid.UUID]*domain.Service
}

func (m *mockServiceRepo) Create(_ context.Context, s *domain.Service) error {
	m.services[s.ID] = s
	return nil
}

func (m *mockServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	return m.services[id], nil
}

func (m *mockServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for _, s := range m.services {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

func (m *mockServiceRepo) List(_ context.Context) ([]domain.Service, error) {
	var result []domain.Service
	for _, s := range m.services {
		result = append(result, *s)
	}
	return result, nil
}

func (m *mockServiceRepo) Update(_ context.Context, s *domain.Service) error {
	m.services[s.ID] = s
	return nil
}

func (m *mockServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.services, id)
	return nil
}

type mockEnvironmentRepo struct {
	envs map[uuid.UUID]*domain.Environment
}

func (m *mockEnvironmentRepo) Create(_ context.Context, e *domain.Environment) error {
	m.envs[e.ID] = e
	return nil
}

func (m *mockEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return m.envs[id], nil
}

func (m *mockEnvironmentRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, e := range m.envs {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, nil
}

func (m *mockEnvironmentRepo) List(_ context.Context) ([]domain.Environment, error) {
	var result []domain.Environment
	for _, e := range m.envs {
		result = append(result, *e)
	}
	return result, nil
}

func (m *mockEnvironmentRepo) Update(_ context.Context, e *domain.Environment) error {
	m.envs[e.ID] = e
	return nil
}

func (m *mockEnvironmentRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.envs, id)
	return nil
}

type mockArtifactRepo struct {
	artifacts map[uuid.UUID]*domain.Artifact
}

func (m *mockArtifactRepo) Create(_ context.Context, a *domain.Artifact) error {
	m.artifacts[a.ID] = a
	return nil
}

func (m *mockArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return m.artifacts[id], nil
}

func (m *mockArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == repo && a.ImageDigest == digest {
			return a, nil
		}
	}
	return nil, nil
}

func (m *mockArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	var result []domain.Artifact
	for _, a := range m.artifacts {
		if a.ServiceID == serviceID {
			result = append(result, *a)
		}
	}
	return result, nil
}

func (m *mockArtifactRepo) ListByBuild(_ context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

func (m *mockArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	for _, a := range m.artifacts {
		if a.ImageRepo == imageRepo && a.ImageDigest == imageDigest {
			return a, nil
		}
	}
	return nil, nil
}

type mockStateRepo struct {
	states map[string]*domain.EnvironmentServiceState
}

func stateMapKey(serviceID, envID uuid.UUID) string {
	return serviceID.String() + ":" + envID.String()
}

func (m *mockStateRepo) Upsert(_ context.Context, s *domain.EnvironmentServiceState) error {
	key := stateMapKey(s.ServiceID, s.EnvironmentID)
	m.states[key] = s
	return nil
}

func (m *mockStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	key := stateMapKey(serviceID, envID)
	return m.states[key], nil
}

func (m *mockStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *mockStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *mockStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

func (m *mockStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	var result []domain.EnvironmentServiceState
	for _, s := range m.states {
		result = append(result, *s)
	}
	return result, nil
}

type mockRuntime struct {
	mu          sync.Mutex
	deployed    []string
	undeployed  []string
	deployErr   error
	undeployErr error
}

func (m *mockRuntime) Type() domain.RuntimeType {
	return domain.RuntimeTypeDocker
}

func (m *mockRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{
		ServiceID:     serviceID,
		EnvironmentID: envID,
		HealthStatus:  domain.HealthStatusHealthy,
		Source:        "mock",
		ObservedAt:    time.Now().UTC(),
	}, nil
}

func (m *mockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deployErr != nil {
		return m.deployErr
	}
	m.deployed = append(m.deployed, serviceName+":"+image)
	return nil
}

func (m *mockRuntime) Undeploy(_ context.Context, serviceName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.undeployErr != nil {
		return m.undeployErr
	}
	m.undeployed = append(m.undeployed, serviceName)
	return nil
}

func (m *mockRuntime) StreamLogs(_ context.Context, serviceName string, opts runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}

type mockPublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (m *mockPublisher) Publish(_ context.Context, e events.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *mockPublisher) Subscribe(_ events.EventType, _ events.Handler) {}

// --- Tests ---

func TestDefaultRemediationConfig(t *testing.T) {
	cfg := DefaultRemediationConfig()
	if cfg.Enabled {
		t.Error("expected disabled by default")
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("expected 3 max retries, got %d", cfg.MaxRetries)
	}
	if cfg.Cooldown != 5*time.Minute {
		t.Errorf("expected 5m cooldown, got %s", cfg.Cooldown)
	}
	if cfg.OnDrift != ActionRedeploy {
		t.Errorf("expected redeploy on drift, got %s", cfg.OnDrift)
	}
	if cfg.OnHealthFailure != ActionRollback {
		t.Errorf("expected rollback on health failure, got %s", cfg.OnHealthFailure)
	}
}

func TestRemediator_OnDriftDetected_Disabled(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "prod",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled": false, // disabled
				},
			},
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(nil, envRepo, nil, nil, rt, pub, zap.NewNop())

	err := r.OnDriftDetected(context.Background(), serviceID, envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) > 0 {
		t.Error("expected no deployment when disabled")
	}
	rt.mu.Unlock()
}

func TestRemediator_OnDriftDetected_Redeploy(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "api"},
	}}

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "prod",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":  true,
					"on_drift": "redeploy",
				},
			},
		},
	}}

	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		artifactID: {
			ID:          artifactID,
			ServiceID:   serviceID,
			ImageRepo:   "ghcr.io/org/api",
			ImageTag:    "v1.2.3",
			ImageDigest: "sha256:abc123",
		},
	}}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &artifactID,
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop())

	err := r.OnDriftDetected(context.Background(), serviceID, envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(rt.deployed))
	}
	if rt.deployed[0] != "api:ghcr.io/org/api@sha256:abc123" {
		t.Errorf("unexpected deployment: %s", rt.deployed[0])
	}
	rt.mu.Unlock()

	pub.mu.Lock()
	var started, completed bool
	for _, e := range pub.events {
		if e.Type == "remediation.started" {
			started = true
		}
		if e.Type == "remediation.completed" {
			completed = true
		}
	}
	pub.mu.Unlock()

	if !started {
		t.Error("expected remediation.started event")
	}
	if !completed {
		t.Error("expected remediation.completed event")
	}
}

func TestRemediator_OnHealthFailure_Rollback(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "web"},
	}}

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "staging",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":           true,
					"on_health_failure": "rollback",
				},
			},
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(svcRepo, envRepo, nil, nil, rt, pub, zap.NewNop())

	err := r.OnHealthFailure(context.Background(), serviceID, envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt.mu.Lock()
	// Rollback should undeploy canary and green.
	canaryFound := false
	greenFound := false
	for _, name := range rt.undeployed {
		if name == "web-canary" {
			canaryFound = true
		}
		if name == "web-green" {
			greenFound = true
		}
	}
	rt.mu.Unlock()

	if !canaryFound {
		t.Error("expected web-canary to be undeployed")
	}
	if !greenFound {
		t.Error("expected web-green to be undeployed")
	}
}

func TestRemediator_MaxRetries(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "api"},
	}}

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "prod",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":          true,
					"max_retries":      float64(2),
					"cooldown_seconds": float64(0), // no cooldown for test
					"on_drift":         "redeploy",
				},
			},
		},
	}}

	artifactID := uuid.New()
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		artifactID: {ID: artifactID, ServiceID: serviceID, ImageRepo: "img", ImageTag: "v1"},
	}}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &artifactID,
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop())

	// First two attempts should succeed.
	_ = r.OnDriftDetected(context.Background(), serviceID, envID)
	_ = r.OnDriftDetected(context.Background(), serviceID, envID)

	// Third attempt should be blocked.
	_ = r.OnDriftDetected(context.Background(), serviceID, envID)

	rt.mu.Lock()
	if len(rt.deployed) != 2 {
		t.Errorf("expected 2 deployments (max_retries=2), got %d", len(rt.deployed))
	}
	rt.mu.Unlock()

	// Verify attempt count.
	if r.GetAttempts(serviceID, envID) != 2 {
		t.Errorf("expected 2 attempts, got %d", r.GetAttempts(serviceID, envID))
	}
}

func TestRemediator_Cooldown(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	svcRepo := &mockServiceRepo{services: map[uuid.UUID]*domain.Service{
		serviceID: {ID: serviceID, Name: "api"},
	}}

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "prod",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":          true,
					"cooldown_seconds": float64(300), // 5 minutes
					"on_drift":         "redeploy",
				},
			},
		},
	}}

	artifactID := uuid.New()
	artifactRepo := &mockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		artifactID: {ID: artifactID, ServiceID: serviceID, ImageRepo: "img", ImageTag: "v1"},
	}}

	stateRepo := &mockStateRepo{states: map[string]*domain.EnvironmentServiceState{
		stateMapKey(serviceID, envID): {
			ServiceID:         serviceID,
			EnvironmentID:     envID,
			DesiredArtifactID: &artifactID,
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(svcRepo, envRepo, artifactRepo, stateRepo, rt, pub, zap.NewNop())

	// First attempt should succeed.
	_ = r.OnDriftDetected(context.Background(), serviceID, envID)

	// Second attempt should be blocked (cooldown not elapsed).
	_ = r.OnDriftDetected(context.Background(), serviceID, envID)

	rt.mu.Lock()
	if len(rt.deployed) != 1 {
		t.Errorf("expected 1 deployment (cooldown blocking second), got %d", len(rt.deployed))
	}
	rt.mu.Unlock()
}

func TestRemediator_ResetState(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	r := NewRemediator(nil, nil, nil, nil, nil, &mockPublisher{}, zap.NewNop())

	// Simulate some attempts.
	key := stateKey(serviceID, envID)
	r.states[key] = &remediationState{attempts: 5}

	r.ResetState(serviceID, envID)

	if r.GetAttempts(serviceID, envID) != 0 {
		t.Error("expected attempts to be reset")
	}
}

func TestRemediator_ActionNone(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "prod",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":  true,
					"on_drift": "none", // no action
				},
			},
		},
	}}

	rt := &mockRuntime{}
	pub := &mockPublisher{}

	r := NewRemediator(nil, envRepo, nil, nil, rt, pub, zap.NewNop())

	err := r.OnDriftDetected(context.Background(), serviceID, envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rt.mu.Lock()
	if len(rt.deployed) > 0 || len(rt.undeployed) > 0 {
		t.Error("expected no action when action is 'none'")
	}
	rt.mu.Unlock()
}

func TestRemediator_ConfigParsing(t *testing.T) {
	envID := uuid.New()

	envRepo := &mockEnvironmentRepo{envs: map[uuid.UUID]*domain.Environment{
		envID: {
			ID:   envID,
			Name: "test",
			RuntimeConfig: map[string]any{
				"auto_remediation": map[string]any{
					"enabled":           true,
					"max_retries":       float64(5),
					"cooldown":          "10m",
					"on_drift":          "redeploy",
					"on_health_failure": "none",
				},
			},
		},
	}}

	r := NewRemediator(nil, envRepo, nil, nil, nil, &mockPublisher{}, zap.NewNop())
	cfg, err := r.getConfig(context.Background(), envID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.Enabled {
		t.Error("expected enabled")
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("expected 5 max_retries, got %d", cfg.MaxRetries)
	}
	if cfg.Cooldown != 10*time.Minute {
		t.Errorf("expected 10m cooldown, got %s", cfg.Cooldown)
	}
	if cfg.OnDrift != ActionRedeploy {
		t.Errorf("expected redeploy, got %s", cfg.OnDrift)
	}
	if cfg.OnHealthFailure != ActionNone {
		t.Errorf("expected none, got %s", cfg.OnHealthFailure)
	}
}
