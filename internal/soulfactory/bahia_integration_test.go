package soulfactory

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type sfMockServiceRepo struct{ services map[uuid.UUID]*domain.Service }

type sfMockEnvRepo struct {
	envs map[uuid.UUID]*domain.Environment
}

type sfMockBuildRepo struct{ builds map[uuid.UUID]*domain.Build }

type sfMockArtifactRepo struct {
	artifacts map[uuid.UUID]*domain.Artifact
}

type sfMockIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
}

type sfMockRunRepo struct {
	runs map[uuid.UUID]*domain.DeploymentRun
}

type sfMockObservationRepo struct {
	observations map[string][]domain.RuntimeObservation
}

type sfMockStateRepo struct {
	states map[string]*domain.EnvironmentServiceState
}

type sfRuntimeArtifactVerifier struct{}

func (sfRuntimeArtifactVerifier) VerifyImage(context.Context, string, string) (*service.ImageVerification, error) {
	return &service.ImageVerification{
		Exists: true, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, nil
}

func newSoulFactoryRegistryHarness() (*service.RegistryService, *sfMockBuildRepo, *sfMockArtifactRepo, *sfMockIntentRepo, *sfMockObservationRepo, *sfMockStateRepo) {
	services := &sfMockServiceRepo{services: map[uuid.UUID]*domain.Service{}}
	envs := &sfMockEnvRepo{envs: map[uuid.UUID]*domain.Environment{}}
	builds := &sfMockBuildRepo{builds: map[uuid.UUID]*domain.Build{}}
	artifacts := &sfMockArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}}
	intents := &sfMockIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runs := &sfMockRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	observations := &sfMockObservationRepo{observations: map[string][]domain.RuntimeObservation{}}
	states := &sfMockStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		services, envs, builds, artifacts, intents, runs, observations, states,
		sfRuntimeArtifactVerifier{}, &events.NoopPublisher{}, zap.NewNop(),
		service.WithManualArtifactRegistration(true),
	)
	return registry, builds, artifacts, intents, observations, states
}

func TestBahiaIntegrationDoesNotCreateSyntheticInitialDeployment(t *testing.T) {
	registry, builds, artifacts, intents, _, _ := newSoulFactoryRegistryHarness()
	envID := uuid.New()
	if err := registry.CreateEnvironment(t.Context(), &domain.Environment{ID: envID, Name: "agents", Protected: false, DeployStrategy: domain.DeployStrategyReplace, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{AgentEnvironmentID: envID.String()}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, NostrNpub: "npub1", CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID

	intent, err := integration.CreateInitialDeployment(t.Context(), soul, serviceID, nil)
	if err != nil {
		t.Fatalf("CreateInitialDeployment() error = %v", err)
	}
	if intent != nil {
		t.Fatalf("CreateInitialDeployment() = %+v, want nil without explicit runtime artifact opt-in", intent)
	}
	if len(builds.builds) != 0 || len(artifacts.artifacts) != 0 || len(intents.intents) != 0 {
		t.Fatalf("synthetic Bahia deployables created: builds=%d artifacts=%d intents=%d", len(builds.builds), len(artifacts.artifacts), len(intents.intents))
	}
}

func TestBahiaIntegrationCreatesInitialDeploymentFromRuntimeArtifactMetadata(t *testing.T) {
	registry, builds, artifacts, intents, _, states := newSoulFactoryRegistryHarness()
	envID := uuid.New()
	if err := registry.CreateEnvironment(t.Context(), &domain.Environment{ID: envID, Name: "agents", Protected: false, DeployStrategy: domain.DeployStrategyReplace, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}

	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{AgentEnvironmentID: envID.String(), DeployRuntimeArtifacts: true}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, NostrNpub: "npub1", Runtime: domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw}, CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID

	intent, err := integration.CreateInitialDeployment(t.Context(), soul, serviceID, runtimeArtifactResult())
	if err != nil {
		t.Fatalf("CreateInitialDeployment() error = %v", err)
	}
	if intent == nil {
		t.Fatal("CreateInitialDeployment() returned nil intent")
	}
	if len(builds.builds) != 1 {
		t.Fatalf("build count = %d, want 1", len(builds.builds))
	}
	if len(artifacts.artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(artifacts.artifacts))
	}
	for _, artifact := range artifacts.artifacts {
		if artifact.ImageRepo != "agents/scout" || artifact.ImageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatalf("artifact = %+v, want runtime repo+digest", artifact)
		}
	}
	if len(intents.intents) != 1 {
		t.Fatalf("intent count = %d, want 1", len(intents.intents))
	}
	state, err := registry.GetEnvironmentServiceState(t.Context(), serviceID, envID)
	if err != nil {
		t.Fatalf("GetEnvironmentServiceState() error = %v", err)
	}
	if state == nil || state.DesiredIntentID == nil || *state.DesiredIntentID != intent.ID {
		t.Fatalf("state = %+v, want desired intent %s", state, intent.ID)
	}
	if state.DriftStatus != domain.DriftStatusDeploying {
		t.Fatalf("state.DriftStatus = %s, want %s", state.DriftStatus, domain.DriftStatusDeploying)
	}
	if got := states.states[stateKey(serviceID, envID)]; got == nil {
		t.Fatal("state repo missing expected environment state")
	}
	deployStatus, err := integration.SyncSoulStatus(t.Context(), soul)
	if err != nil {
		t.Fatalf("SyncSoulStatus() error = %v", err)
	}
	if deployStatus != "deploying" {
		t.Fatalf("SyncSoulStatus() = %q, want deploying", deployStatus)
	}
}

func TestBahiaIntegrationRuntimeArtifactOptInRequiresDigestMetadata(t *testing.T) {
	registry, builds, artifacts, intents, _, _ := newSoulFactoryRegistryHarness()
	envID := uuid.New()
	if err := registry.CreateEnvironment(t.Context(), &domain.Environment{ID: envID, Name: "agents", Protected: false, DeployStrategy: domain.DeployStrategyReplace, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{AgentEnvironmentID: envID.String(), DeployRuntimeArtifacts: true}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}
	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID

	for name, mutate := range map[string]func(map[string]interface{}){
		"missing digest": func(artifact map[string]interface{}) { delete(artifact, "image_digest") },
		"malformed digest": func(artifact map[string]interface{}) {
			artifact["image_digest"] = "latest"
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := runtimeArtifactResult()
			mutate(result.Result["artifact"].(map[string]interface{}))
			_, err = integration.CreateInitialDeployment(t.Context(), soul, serviceID, result)
			if !errors.Is(err, ErrDeployableArtifactRequired) {
				t.Fatalf("CreateInitialDeployment() error = %v, want ErrDeployableArtifactRequired", err)
			}
			if len(builds.builds) != 0 || len(artifacts.artifacts) != 0 || len(intents.intents) != 0 {
				t.Fatalf("Bahia deployables created despite invalid digest: builds=%d artifacts=%d intents=%d", len(builds.builds), len(artifacts.artifacts), len(intents.intents))
			}
		})
	}
}

func TestBahiaIntegrationLifecycleUpdatesState(t *testing.T) {
	registry, _, _, intents, observations, _ := newSoulFactoryRegistryHarness()
	envID := uuid.New()
	if err := registry.CreateEnvironment(t.Context(), &domain.Environment{ID: envID, Name: "agents", Protected: false, DeployStrategy: domain.DeployStrategyReplace, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	integration, err := NewBahiaIntegration(registry, BahiaIntegrationConfig{AgentEnvironmentID: envID.String(), DeployRuntimeArtifacts: true}, slogDefaultLogger())
	if err != nil {
		t.Fatalf("NewBahiaIntegration() error = %v", err)
	}

	soul := &domain.AgentSoul{ID: uuid.New(), AgentID: "scout", Name: "Scout", Tier: domain.SoulTierStandard, Runtime: domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw}, CreatedAt: time.Now().UTC()}
	serviceID, err := integration.RegisterSoulAsService(t.Context(), soul)
	if err != nil {
		t.Fatalf("RegisterSoulAsService() error = %v", err)
	}
	soul.BahiaServiceID = &serviceID
	if _, err := integration.CreateInitialDeployment(t.Context(), soul, serviceID, runtimeArtifactResult()); err != nil {
		t.Fatalf("CreateInitialDeployment() error = %v", err)
	}

	if err := integration.HandleLifecycleAction(t.Context(), soul, domain.SoulActionSuspend); err != nil {
		t.Fatalf("HandleLifecycleAction(suspend) error = %v", err)
	}
	if got := latestObservation(observations, serviceID, envID); got == nil || got.HealthStatus != domain.HealthStatusStopped {
		t.Fatalf("latest suspend observation = %+v, want stopped", got)
	}
	status, err := integration.SyncSoulStatus(t.Context(), soul)
	if err != nil {
		t.Fatalf("SyncSoulStatus() after suspend error = %v", err)
	}
	if status != "stopped" {
		t.Fatalf("SyncSoulStatus() after suspend = %q, want stopped", status)
	}

	if err := integration.HandleLifecycleAction(t.Context(), soul, domain.SoulActionResume); err != nil {
		t.Fatalf("HandleLifecycleAction(resume) error = %v", err)
	}
	if len(intents.intents) != 2 {
		t.Fatalf("intent count after resume = %d, want 2", len(intents.intents))
	}

	if err := integration.HandleLifecycleAction(t.Context(), soul, domain.SoulActionRedeploy); err != nil {
		t.Fatalf("HandleLifecycleAction(redeploy) error = %v", err)
	}
	if len(intents.intents) != 3 {
		t.Fatalf("intent count after redeploy = %d, want 3", len(intents.intents))
	}

	if err := integration.HandleLifecycleAction(t.Context(), soul, domain.SoulActionRevoke); err != nil {
		t.Fatalf("HandleLifecycleAction(revoke) error = %v", err)
	}
	if got := latestObservation(observations, serviceID, envID); got == nil || got.HealthStatus != domain.HealthStatusStopped {
		t.Fatalf("latest revoke observation = %+v, want stopped", got)
	}
}

func slogDefaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func stateKey(serviceID, envID uuid.UUID) string { return serviceID.String() + ":" + envID.String() }

func latestObservation(repo *sfMockObservationRepo, serviceID, envID uuid.UUID) *domain.RuntimeObservation {
	items := repo.observations[stateKey(serviceID, envID)]
	if len(items) == 0 {
		return nil
	}
	last := items[len(items)-1]
	return &last
}

func runtimeArtifactResult() *RuntimeControlResultEnvelope {
	return &RuntimeControlResultEnvelope{
		Method:         RuntimeMethodProvision,
		RequestEvent:   "runtime-request-event",
		IdempotencyKey: "sha256:runtime-idempotency",
		Status:         "success",
		Result: map[string]interface{}{
			"artifact": map[string]interface{}{
				"image_repo":          "agents/scout",
				"image_tag":           "runtime-build-7",
				"image_digest":        "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"manifest_media_type": "application/vnd.oci.image.manifest.v1+json",
			},
			"build": map[string]interface{}{
				"git_sha":   "abcdef1",
				"git_ref":   "refs/heads/main",
				"ci_system": "openclaw-ci",
				"ci_run_id": "openclaw-run-7",
			},
		},
	}
}

func (m *sfMockServiceRepo) Create(_ context.Context, svc *domain.Service) error {
	if svc.ID == uuid.Nil {
		svc.ID = uuid.New()
	}
	m.services[svc.ID] = svc
	return nil
}
func (m *sfMockServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	return m.services[id], nil
}
func (m *sfMockServiceRepo) GetByName(_ context.Context, name string) (*domain.Service, error) {
	for _, svc := range m.services {
		if svc.Name == name {
			return svc, nil
		}
	}
	return nil, nil
}
func (m *sfMockServiceRepo) List(_ context.Context) ([]domain.Service, error) {
	out := make([]domain.Service, 0, len(m.services))
	for _, svc := range m.services {
		out = append(out, *svc)
	}
	return out, nil
}
func (m *sfMockServiceRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Service, error) {
	out := []domain.Service{}
	for _, svc := range m.services {
		if svc.OrgID == orgID {
			out = append(out, *svc)
		}
	}
	return out, nil
}
func (m *sfMockServiceRepo) Update(_ context.Context, svc *domain.Service) error {
	m.services[svc.ID] = svc
	return nil
}
func (m *sfMockServiceRepo) Delete(_ context.Context, id uuid.UUID) error {
	delete(m.services, id)
	return nil
}

func (m *sfMockEnvRepo) Create(_ context.Context, env *domain.Environment) error {
	if env.ID == uuid.Nil {
		env.ID = uuid.New()
	}
	m.envs[env.ID] = env
	return nil
}
func (m *sfMockEnvRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	return m.envs[id], nil
}
func (m *sfMockEnvRepo) GetByName(_ context.Context, name string) (*domain.Environment, error) {
	for _, env := range m.envs {
		if env.Name == name {
			return env, nil
		}
	}
	return nil, nil
}
func (m *sfMockEnvRepo) List(_ context.Context) ([]domain.Environment, error) {
	out := make([]domain.Environment, 0, len(m.envs))
	for _, env := range m.envs {
		out = append(out, *env)
	}
	return out, nil
}
func (m *sfMockEnvRepo) ListByOrg(_ context.Context, orgID uuid.UUID) ([]domain.Environment, error) {
	out := []domain.Environment{}
	for _, env := range m.envs {
		if env.OrgID == orgID {
			out = append(out, *env)
		}
	}
	return out, nil
}
func (m *sfMockEnvRepo) Update(_ context.Context, env *domain.Environment) error {
	m.envs[env.ID] = env
	return nil
}
func (m *sfMockEnvRepo) Delete(_ context.Context, id uuid.UUID) error { delete(m.envs, id); return nil }

func (m *sfMockBuildRepo) Create(_ context.Context, build *domain.Build) error {
	if build.ID == uuid.Nil {
		build.ID = uuid.New()
	}
	m.builds[build.ID] = build
	return nil
}
func (m *sfMockBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	return m.builds[id], nil
}
func (m *sfMockBuildRepo) GetByCISystemRunID(_ context.Context, ciSystem, ciRunID string) (*domain.Build, error) {
	for _, build := range m.builds {
		if build.CISystem == ciSystem && build.CIRunID == ciRunID {
			return build, nil
		}
	}
	return nil, nil
}
func (m *sfMockBuildRepo) ListByService(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Build, error) {
	out := []domain.Build{}
	for _, build := range m.builds {
		if build.ServiceID == serviceID {
			out = append(out, *build)
		}
	}
	return out, nil
}
func (m *sfMockBuildRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.BuildStatus) error {
	if build := m.builds[id]; build != nil {
		build.Status = status
	}
	return nil
}

func (m *sfMockArtifactRepo) Create(_ context.Context, artifact *domain.Artifact) error {
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	m.artifacts[artifact.ID] = artifact
	return nil
}
func (m *sfMockArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	return m.artifacts[id], nil
}
func (m *sfMockArtifactRepo) GetByDigest(_ context.Context, repo, digest string) (*domain.Artifact, error) {
	for _, artifact := range m.artifacts {
		if artifact.ImageRepo == repo && artifact.ImageDigest == digest {
			return artifact, nil
		}
	}
	return nil, nil
}
func (m *sfMockArtifactRepo) GetByImageRepoDigest(_ context.Context, imageRepo, imageDigest string) (*domain.Artifact, error) {
	for _, artifact := range m.artifacts {
		if artifact.ImageRepo == imageRepo && artifact.ImageDigest == imageDigest {
			return artifact, nil
		}
	}
	return nil, nil
}
func (m *sfMockArtifactRepo) ListByService(_ context.Context, serviceID uuid.UUID, limit, offset int) ([]domain.Artifact, error) {
	out := []domain.Artifact{}
	for _, artifact := range m.artifacts {
		if artifact.ServiceID == serviceID {
			out = append(out, *artifact)
		}
	}
	return out, nil
}
func (m *sfMockArtifactRepo) ListByBuild(_ context.Context, buildID uuid.UUID) ([]domain.Artifact, error) {
	out := []domain.Artifact{}
	for _, artifact := range m.artifacts {
		if artifact.BuildID == buildID {
			out = append(out, *artifact)
		}
	}
	return out, nil
}

func (m *sfMockIntentRepo) Create(_ context.Context, intent *domain.DeploymentIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	m.intents[intent.ID] = intent
	return nil
}
func (m *sfMockIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	return m.intents[id], nil
}
func (m *sfMockIntentRepo) GetByHiveResultEventID(_ context.Context, eventID string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (m *sfMockIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit, offset int) ([]domain.DeploymentIntent, error) {
	out := []domain.DeploymentIntent{}
	for _, intent := range m.intents {
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			out = append(out, *intent)
		}
	}
	return out, nil
}
func (m *sfMockIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	if intent := m.intents[id]; intent != nil {
		intent.Status = status
	}
	return nil
}
func (m *sfMockIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	if intent := m.intents[id]; intent != nil {
		intent.ApprovalStatus = status
	}
	return nil
}
func (m *sfMockIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	if intent := m.intents[id]; intent != nil {
		intent.DesiredState = desiredState
		intent.DesiredHash = desiredHash
	}
	return nil
}

func (m *sfMockRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	m.runs[run.ID] = run
	return nil
}
func (m *sfMockRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	return m.runs[id], nil
}
func (m *sfMockRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	out := []domain.DeploymentRun{}
	for _, run := range m.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (m *sfMockRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if run := m.runs[id]; run != nil {
		run.Status = status
		run.ExitCode = exitCode
	}
	return nil
}

func (m *sfMockObservationRepo) Create(_ context.Context, obs *domain.RuntimeObservation) error {
	key := stateKey(obs.ServiceID, obs.EnvironmentID)
	m.observations[key] = append(m.observations[key], *obs)
	return nil
}
func (m *sfMockObservationRepo) GetLatest(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	return latestObservation(m, serviceID, envID), nil
}
func (m *sfMockObservationRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, limit int) ([]domain.RuntimeObservation, error) {
	out := append([]domain.RuntimeObservation(nil), m.observations[stateKey(serviceID, envID)]...)
	return out, nil
}

func (m *sfMockStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	copy := *state
	copy.UpdatedAt = time.Now().UTC()
	m.states[stateKey(state.ServiceID, state.EnvironmentID)] = &copy
	return nil
}
func (m *sfMockStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	return m.states[stateKey(serviceID, envID)], nil
}
func (m *sfMockStateRepo) ListByEnvironment(_ context.Context, envID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	out := []domain.EnvironmentServiceState{}
	for _, state := range m.states {
		if state.EnvironmentID == envID {
			out = append(out, *state)
		}
	}
	return out, nil
}
func (m *sfMockStateRepo) ListByService(_ context.Context, serviceID uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	out := []domain.EnvironmentServiceState{}
	for _, state := range m.states {
		if state.ServiceID == serviceID {
			out = append(out, *state)
		}
	}
	return out, nil
}
func (m *sfMockStateRepo) ListDrifted(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (m *sfMockStateRepo) ListAll(_ context.Context) ([]domain.EnvironmentServiceState, error) {
	out := []domain.EnvironmentServiceState{}
	for _, state := range m.states {
		out = append(out, *state)
	}
	return out, nil
}
func (m *sfMockStateRepo) ListDueForObservation(_ context.Context, _ time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
