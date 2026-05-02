package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestRuntimeLifecycleDeployUsesAdoptedTargetAndRecordsObservation(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	publisher := &capturePublisher{}
	rt := &lifecycleMockRuntime{}
	resolver := &mockRuntimeResolver{rt: rt}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, resolver, publisher, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	obs, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}
	if obs == nil || obs.ObservedContainerID == "" {
		t.Fatalf("expected deploy observation, got %#v", obs)
	}
	if rt.deployTarget != "legacy-api" {
		t.Fatalf("expected deploy target legacy-api, got %q", rt.deployTarget)
	}
	if rt.deployImage != "ghcr.io/org/api@sha256:abc123" {
		t.Fatalf("unexpected deploy image: %q", rt.deployImage)
	}
	if rt.deployOpts.Environment["APP_ENV"] != "prod" || rt.deployOpts.Labels["bahia.service"] != "api" || rt.deployOpts.Labels["bahia.managed"] != "true" || rt.deployOpts.Labels["existing"] != "label" {
		t.Fatalf("unexpected deploy opts env/labels: %#v", rt.deployOpts)
	}
	if strings.Join(rt.deployOpts.Ports, ",") != "8080:80" || rt.deployOpts.Restart != "unless-stopped" || rt.deployOpts.NetworkMode != "host" {
		t.Fatalf("adopted runtime shape was not preserved: %#v", rt.deployOpts)
	}
	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state == nil || state.DesiredArtifactID == nil || *state.DesiredArtifactID != artifact.ID || state.CurrentObservationID == nil {
		t.Fatalf("state was not updated from deploy observation: %#v", state)
	}
	if !publisher.hasEvent(runtimeActionDeployEvent) {
		t.Fatalf("expected runtime.deploy event, got %#v", publisher.events)
	}
}

func TestRuntimeLifecycleDeployMergesEffectiveSecretsOverAdoptedEnvironment(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	secretRepo := newMockSecretRepo()
	encryptor := secretsAdapter.NewEncryptor("test-runtime-key")
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeLifecycleSecrets(secretRepo, encryptor),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	globalCiphertext, err := encryptor.Encrypt("global-token", domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt global secret: %v", err)
	}
	if err := secretRepo.Create(ctx, &domain.ServiceSecret{ServiceID: svc.ID, Name: "API_TOKEN", EncryptedValue: globalCiphertext, EncryptionMethod: domain.EncryptionAES256, CreatedBy: "test"}); err != nil {
		t.Fatalf("create global secret: %v", err)
	}
	envCiphertext, err := encryptor.Encrypt("secret-prod", domain.EncryptionAES256)
	if err != nil {
		t.Fatalf("encrypt env secret: %v", err)
	}
	if err := secretRepo.Create(ctx, &domain.ServiceSecret{ServiceID: svc.ID, EnvironmentID: &env.ID, Name: "APP_ENV", EncryptedValue: envCiphertext, EncryptionMethod: domain.EncryptionAES256, CreatedBy: "test"}); err != nil {
		t.Fatalf("create env secret: %v", err)
	}

	if _, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID); err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}
	if rt.deployOpts.Environment["APP_ENV"] != "secret-prod" {
		t.Fatalf("secret APP_ENV should override adopted env, got %#v", rt.deployOpts.Environment)
	}
	if rt.deployOpts.Environment["API_TOKEN"] != "global-token" {
		t.Fatalf("effective global secret not merged: %#v", rt.deployOpts.Environment)
	}
}

func TestRuntimeLifecycleDeployFailureDoesNotMutateDesiredState(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{deployErr: fmt.Errorf("simulated deploy failure")}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	oldArtifactID := uuid.New()
	if err := stateRepo.Upsert(ctx, &domain.EnvironmentServiceState{ServiceID: svc.ID, EnvironmentID: env.ID, DesiredArtifactID: &oldArtifactID, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil || !strings.Contains(err.Error(), "simulated deploy failure") {
		t.Fatalf("expected deploy failure, got %v", err)
	}
	state := stateRepo.states[stateKey(svc.ID, env.ID)]
	if state == nil || state.DesiredArtifactID == nil || *state.DesiredArtifactID != oldArtifactID || state.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("failed deploy should preserve desired state and drift: %#v", state)
	}
}

func TestRuntimeLifecycleRejectsNonAdoptedOrNonDirectRuntimeWorkloads(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	env.RuntimeConfig["management_mode"] = "deployment_controller"
	if err := envRepo.Update(ctx, env); err != nil {
		t.Fatalf("update env: %v", err)
	}
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil || !strings.Contains(err.Error(), directRuntimeGuardrailMessage) {
		t.Fatalf("expected management mode guardrail, got %v", err)
	}
	if rt.deployTarget != "" {
		t.Fatalf("runtime deploy should not be called, got target %q", rt.deployTarget)
	}

	env.RuntimeConfig["management_mode"] = "direct_runtime"
	env.RuntimeConfig["host_alias"] = "other-host"
	if err := envRepo.Update(ctx, env); err != nil {
		t.Fatalf("restore env: %v", err)
	}
	_, err = lifecycle.Stop(ctx, svc.ID, env.ID)
	if err == nil || !strings.Contains(err.Error(), directRuntimeGuardrailMessage) {
		t.Fatalf("expected host alias guardrail, got %v", err)
	}
	if rt.stopTarget != "" {
		t.Fatalf("runtime stop should not be called, got target %q", rt.stopTarget)
	}

	env.RuntimeConfig["host_alias"] = "local"
	if err := envRepo.Update(ctx, env); err != nil {
		t.Fatalf("restore env host alias: %v", err)
	}
	svc.RuntimeConfig = nil
	if err := svcRepo.Update(ctx, svc); err != nil {
		t.Fatalf("update service: %v", err)
	}
	_, err = lifecycle.Restart(ctx, svc.ID, env.ID)
	if err == nil || !strings.Contains(err.Error(), directRuntimeGuardrailMessage) {
		t.Fatalf("expected adopted workload guardrail, got %v", err)
	}
	if rt.restartTarget != "" {
		t.Fatalf("runtime restart should not be called, got target %q", rt.restartTarget)
	}
}

func TestRuntimeLifecycleDeploySecretDecryptFailureDoesNotMutateDesiredState(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	secretRepo := newMockSecretRepo()
	encryptor := secretsAdapter.NewEncryptor("test-runtime-key")
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeLifecycleSecrets(secretRepo, encryptor),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	if err := secretRepo.Create(ctx, &domain.ServiceSecret{ServiceID: svc.ID, EnvironmentID: &env.ID, Name: "API_TOKEN", EncryptedValue: []byte("not-valid-aes"), EncryptionMethod: domain.EncryptionAES256, CreatedBy: "test"}); err != nil {
		t.Fatalf("create invalid secret: %v", err)
	}

	before := *stateRepo.states[stateKey(svc.ID, env.ID)]
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err == nil || !strings.Contains(err.Error(), "decrypting effective secret") {
		t.Fatalf("expected decrypt error, got %v", err)
	}
	if rt.deployTarget != "" {
		t.Fatalf("runtime deploy should not be called, got target %q", rt.deployTarget)
	}
	after := stateRepo.states[stateKey(svc.ID, env.ID)]
	if after == nil || after.DesiredArtifactID == nil || before.DesiredArtifactID == nil || *after.DesiredArtifactID != *before.DesiredArtifactID || after.DriftStatus != before.DriftStatus {
		t.Fatalf("state should not be mutated on secret merge failure: before=%#v after=%#v", before, after)
	}
}

func TestRuntimeLifecycleDeployFallsBackToAdoptedEnvironmentWithoutSecretRepo(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	if _, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID); err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}
	if rt.deployOpts.Environment["APP_ENV"] != "prod" {
		t.Fatalf("expected adopted env fallback, got %#v", rt.deployOpts.Environment)
	}
}

func TestRuntimeLifecycleDeployRejectsForeignArtifact(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, _ := seedRuntimeLifecycleFixtures(t, registry)
	foreignBuildID := uuid.New()
	foreignArtifact := &domain.Artifact{BuildID: foreignBuildID, ServiceID: uuid.New(), ImageRepo: "ghcr.io/other/api", ImageTag: "v2", ImageDigest: "sha256:foreign", ScanStatus: domain.ScanStatusUnknown}
	if err := artifactRepo.Create(ctx, foreignArtifact); err != nil {
		t.Fatalf("Create foreign artifact: %v", err)
	}
	_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &foreignArtifact.ID)
	if err == nil || !strings.Contains(err.Error(), "belongs to service") {
		t.Fatalf("expected foreign artifact error, got %v", err)
	}
	if rt.deployTarget != "" {
		t.Fatalf("runtime deploy should not be called, got target %q", rt.deployTarget)
	}
}

func TestRuntimeLifecycleRestartAndStopUseAdoptedTarget(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, _ := seedRuntimeLifecycleFixtures(t, registry)
	if _, err := lifecycle.Restart(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if rt.restartTarget != "legacy-api" {
		t.Fatalf("expected restart target legacy-api, got %q", rt.restartTarget)
	}
	if _, err := lifecycle.Stop(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if rt.stopTarget != "legacy-api" {
		t.Fatalf("expected stop target legacy-api, got %q", rt.stopTarget)
	}
}

func seedRuntimeLifecycleFixtures(t *testing.T, registry *RegistryService) (*domain.Service, *domain.Environment, *domain.Artifact) {
	t.Helper()
	ctx := context.Background()
	svc := &domain.Service{
		Name:         "api",
		ArtifactRepo: "ghcr.io/org/api",
		RuntimeType:  domain.RuntimeTypeDocker,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{
			TargetName:  "legacy-api",
			HostAlias:   "local",
			Environment: map[string]string{"APP_ENV": "prod"},
			Labels:      map[string]string{"existing": "label"},
			Ports:       []string{"8080:80"},
			Volumes:     []string{"/host:/data:ro"},
			Restart:     "unless-stopped",
			Command:     []string{"serve"},
			Entrypoint:  []string{"/entrypoint.sh"},
			WorkingDir:  "/app",
			NetworkMode: "host",
		}},
	}
	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	env := &domain.Environment{Name: "prod", RuntimeConfig: map[string]any{"type": "docker", "docker_host": "unix:///docker.sock", "host_alias": "local", "management_mode": "direct_runtime"}}
	if err := registry.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	build := &domain.Build{ServiceID: svc.ID, GitSHA: "abc", GitRef: "main", CISystem: "test", CIRunID: uuid.NewString(), Status: domain.BuildStatusSucceeded}
	if err := registry.builds.Create(ctx, build); err != nil {
		t.Fatalf("Create build: %v", err)
	}
	artifact := &domain.Artifact{BuildID: build.ID, ServiceID: svc.ID, ImageRepo: "ghcr.io/org/api", ImageTag: "v1", ImageDigest: "sha256:abc123", ScanStatus: domain.ScanStatusUnknown}
	if err := registry.artifacts.Create(ctx, artifact); err != nil {
		t.Fatalf("Create artifact: %v", err)
	}
	if err := registry.state.Upsert(ctx, &domain.EnvironmentServiceState{ServiceID: svc.ID, EnvironmentID: env.ID, DesiredArtifactID: &artifact.ID, DriftStatus: domain.DriftStatusUnknown}); err != nil {
		t.Fatalf("seed direct runtime state: %v", err)
	}
	return svc, env, artifact
}

type mockRuntimeResolver struct {
	rt runtime.Runtime
}

func (r *mockRuntimeResolver) Resolve(_ *domain.Service, _ *domain.Environment) (runtime.Runtime, error) {
	return r.rt, nil
}

type lifecycleMockRuntime struct {
	deployTarget  string
	deployImage   string
	deployOpts    runtime.DeployOptions
	deployErr     error
	restartTarget string
	stopTarget    string
}

func (m *lifecycleMockRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }

func (m *lifecycleMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	m.deployTarget = serviceName
	m.deployImage = image
	m.deployOpts = opts
	return m.deployErr
}

func (m *lifecycleMockRuntime) Undeploy(_ context.Context, _ string) error { return nil }

func (m *lifecycleMockRuntime) StreamLogs(_ context.Context, _ string, _ runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}

func (m *lifecycleMockRuntime) Restart(_ context.Context, targetName string) error {
	m.restartTarget = targetName
	return nil
}

func (m *lifecycleMockRuntime) Stop(_ context.Context, targetName string) error {
	m.stopTarget = targetName
	return nil
}

func (m *lifecycleMockRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:abc123",
		ObservedImageRepo:   "ghcr.io/org/api",
		ObservedContainerID: serviceName + "-container",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "mock",
		ObservedAt:          time.Now().UTC(),
	}, nil
}

var _ runtime.LifecycleRuntime = (*lifecycleMockRuntime)(nil)
