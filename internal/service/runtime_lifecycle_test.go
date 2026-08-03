package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"go.uber.org/zap"
)

func TestDesiredSecretBindingsSelectsOnlyReviewedManagedReferences(t *testing.T) {
	selectedID := uuid.New()
	unselectedID := uuid.New()
	bindings, err := desiredSecretBindings(&domain.DesiredServiceSpec{SecretRefs: []domain.DesiredSecretRef{{
		SecretID: selectedID,
		EnvVar:   "API_TOKEN",
	}}}, true)
	if err != nil {
		t.Fatalf("desiredSecretBindings: %v", err)
	}
	if len(bindings) != 1 || bindings[selectedID] != "API_TOKEN" {
		t.Fatalf("unexpected managed secret bindings: %#v", bindings)
	}
	if _, exists := bindings[unselectedID]; exists {
		t.Fatal("unreviewed secret must not be selected")
	}
}

func TestDesiredSecretBindingsRejectsDuplicateManagedSecretID(t *testing.T) {
	secretID := uuid.New()
	_, err := desiredSecretBindings(&domain.DesiredServiceSpec{SecretRefs: []domain.DesiredSecretRef{
		{SecretID: secretID, EnvVar: "TOKEN_ONE"},
		{SecretID: secretID, EnvVar: "TOKEN_TWO"},
	}}, true)
	if err == nil {
		t.Fatal("expected duplicate managed secret ID to fail")
	}
}

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
	encryptor, err := secretsAdapter.NewEncryptor("5555555555555555555555555555555555555555555555555555555555555555")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
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

func TestBuildDesiredStateSnapshotSupportsBahiaManagedComposeUnitWithoutAdoption(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	svc.RuntimeConfig = &domain.ServiceRuntimeConfig{}
	if err := svcRepo.Update(ctx, svc); err != nil {
		t.Fatalf("remove adopted workload marker: %v", err)
	}

	unitRepo := newEnvironmentMutationUnitRepo()
	unit := &domain.DeploymentUnit{
		ID:            uuid.New(),
		EnvironmentID: env.ID,
		Key:           "max",
		RuntimeType:   domain.RuntimeTypeCompose,
		EndpointRef:   "max",
		ComposeDir:    "/srv/bahia/gastown",
		ReconcileMode: domain.ReconcileModeAutoApply,
		OwnershipMode: domain.OwnershipModeBahiaManaged,
		RuntimeConfig: map[string]any{"execution_mode": "sdk"},
	}
	if err := unitRepo.Create(ctx, unit); err != nil {
		t.Fatalf("seed deployment unit: %v", err)
	}
	env.Targeting.DefaultUnitKey = unit.Key
	if err := envRepo.Update(ctx, env); err != nil {
		t.Fatalf("update environment targeting: %v", err)
	}

	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: &lifecycleMockRuntime{}}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	spec, err := lifecycle.BuildDesiredStateSnapshot(ctx, svc.ID, env.ID, artifact.ID, &unit.ID)
	if err != nil {
		t.Fatalf("BuildDesiredStateSnapshot: %v", err)
	}
	if spec.DeploymentUnitID == nil || *spec.DeploymentUnitID != unit.ID {
		t.Fatalf("deployment unit ID = %v, want %s", spec.DeploymentUnitID, unit.ID)
	}
	if spec.DeploymentUnitKey != unit.Key || spec.UnitRuntimeType != domain.RuntimeTypeCompose {
		t.Fatalf("unit identity = key %q runtime %q", spec.DeploymentUnitKey, spec.UnitRuntimeType)
	}
	if spec.ComposeExtension == nil {
		t.Fatalf("Compose extension was not persisted in desired state")
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
	encryptor, err := secretsAdapter.NewEncryptor("5555555555555555555555555555555555555555555555555555555555555555")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeLifecycleSecrets(secretRepo, encryptor),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)
	if err := secretRepo.Create(ctx, &domain.ServiceSecret{ServiceID: svc.ID, EnvironmentID: &env.ID, Name: "API_TOKEN", EncryptedValue: []byte("not-valid-aes"), EncryptionMethod: domain.EncryptionAES256, CreatedBy: "test"}); err != nil {
		t.Fatalf("create invalid secret: %v", err)
	}

	before := *stateRepo.states[stateKey(svc.ID, env.ID)]
	_, err = lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
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

func TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _, obsRepo, stateRepo := newTestRegistryAll()
	rt := &desiredStateLifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	env := &domain.Environment{Name: "prod-compose", RuntimeConfig: map[string]any{"type": "compose", "host_alias": "local", "management_mode": "direct_runtime"}}
	if err := registry.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	targetSvc, targetArtifact := seedComposeManagedService(t, registry, buildRepo, env, "api", "api-prod", "ghcr.io/org/api", "sha256:target")
	storedSvc, storedArtifact := seedComposeManagedService(t, registry, buildRepo, env, "worker", "worker-prod", "ghcr.io/org/worker", "sha256:stored")
	legacySvc, legacyArtifact := seedComposeManagedService(t, registry, buildRepo, env, "admin", "admin-prod", "ghcr.io/org/admin", "sha256:legacy")

	storedSpec, err := NewDesiredStateBuilder().Build(BuildInput{
		Service:       storedSvc,
		Environment:   env,
		Artifact:      storedArtifact,
		RuntimeConfig: storedSvc.RuntimeConfig,
	})
	if err != nil {
		t.Fatalf("build stored sibling spec: %v", err)
	}

	if err := stateRepo.Upsert(ctx, &domain.EnvironmentServiceState{ServiceID: targetSvc.ID, EnvironmentID: env.ID, DesiredArtifactID: &targetArtifact.ID, DriftStatus: domain.DriftStatusUnknown}); err != nil {
		t.Fatalf("seed target state: %v", err)
	}
	if err := stateRepo.Upsert(ctx, &domain.EnvironmentServiceState{ServiceID: storedSvc.ID, EnvironmentID: env.ID, DesiredArtifactID: &storedArtifact.ID, DesiredRuntimeState: storedSpec, DesiredHash: storedSpec.DesiredHash, DriftStatus: domain.DriftStatusInSync}); err != nil {
		t.Fatalf("seed stored sibling state: %v", err)
	}
	if err := stateRepo.Upsert(ctx, &domain.EnvironmentServiceState{ServiceID: legacySvc.ID, EnvironmentID: env.ID, DesiredArtifactID: &legacyArtifact.ID, DriftStatus: domain.DriftStatusUnknown}); err != nil {
		t.Fatalf("seed legacy sibling state: %v", err)
	}

	obs, err := lifecycle.Deploy(ctx, targetSvc.ID, env.ID, &targetArtifact.ID)
	if err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}
	if obs == nil {
		t.Fatal("expected runtime observation")
	}
	if !rt.applied {
		t.Fatal("desired-state applier was not called")
	}
	if rt.applyReq.EnvironmentPlan == nil {
		t.Fatal("desired-state apply request missing environment plan")
	}

	gotServices := map[string]domain.DesiredServiceSpec{}
	for _, spec := range rt.applyReq.EnvironmentPlan.Services {
		gotServices[spec.StableServiceKey] = spec
	}
	for _, key := range []string{"admin-prod", "api-prod", "worker-prod"} {
		if _, ok := gotServices[key]; !ok {
			t.Fatalf("first desired-state plan omitted managed service %q; got keys %#v", key, gotServices)
		}
	}
	if gotServices["admin-prod"].ImageRef != "ghcr.io/org/admin@sha256:legacy" {
		t.Fatalf("legacy sibling image was not reconstructed from persisted artifact: %#v", gotServices["admin-prod"])
	}
	if gotServices["worker-prod"].DesiredHash != storedSpec.DesiredHash {
		t.Fatalf("stored sibling desired state was not preserved: got %q want %q", gotServices["worker-prod"].DesiredHash, storedSpec.DesiredHash)
	}

	legacyState := stateRepo.states[stateKey(legacySvc.ID, env.ID)]
	if legacyState == nil || legacyState.DesiredRuntimeState == nil || legacyState.DesiredHash == "" {
		t.Fatalf("legacy sibling desired spec was not persisted opportunistically: %#v", legacyState)
	}
	if legacyState.DesiredRuntimeState.StableServiceKey != "admin-prod" {
		t.Fatalf("persisted legacy sibling has wrong stable key: %#v", legacyState.DesiredRuntimeState)
	}

	targetState := stateRepo.states[stateKey(targetSvc.ID, env.ID)]
	if targetState == nil || targetState.DesiredRuntimeState == nil || targetState.CurrentObservationID == nil {
		t.Fatalf("target state did not persist desired state and observation: %#v", targetState)
	}
	if targetState.DriftStatus != domain.DriftStatusInSync {
		t.Fatalf("target drift status = %q, want %q", targetState.DriftStatus, domain.DriftStatusInSync)
	}
	if targetState.LastReconciledAt == nil {
		t.Fatal("target state did not record reconciliation timestamp after observation")
	}
	if len(obsRepo.observations) != 1 {
		t.Fatalf("expected one recorded post-apply observation, got %d", len(obsRepo.observations))
	}
	if len(rt.applyResultNames) != 3 {
		t.Fatalf("desired-state apply should cover all managed services, got resources %v", rt.applyResultNames)
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

func seedComposeManagedService(t *testing.T, registry *RegistryService, buildRepo *mockBuildRepo, env *domain.Environment, name, targetName, imageRepo, digest string) (*domain.Service, *domain.Artifact) {
	t.Helper()
	ctx := context.Background()
	svc := &domain.Service{
		Name:         name,
		ArtifactRepo: imageRepo,
		RuntimeType:  domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{
			TargetName:  targetName,
			HostAlias:   "local",
			Environment: map[string]string{"APP_ENV": env.Name},
			Restart:     "unless-stopped",
		}},
	}
	if err := registry.CreateService(ctx, svc); err != nil {
		t.Fatalf("CreateService(%s): %v", name, err)
	}
	build := &domain.Build{ServiceID: svc.ID, GitSHA: digest, GitRef: "main", CISystem: "test", CIRunID: uuid.NewString(), Status: domain.BuildStatusSucceeded}
	if err := buildRepo.Create(ctx, build); err != nil {
		t.Fatalf("Create build(%s): %v", name, err)
	}
	artifact := &domain.Artifact{BuildID: build.ID, ServiceID: svc.ID, ImageRepo: imageRepo, ImageTag: "v1", ImageDigest: digest, ScanStatus: domain.ScanStatusUnknown}
	if err := registry.artifacts.Create(ctx, artifact); err != nil {
		t.Fatalf("Create artifact(%s): %v", name, err)
	}
	return svc, artifact
}

type desiredStateLifecycleMockRuntime struct {
	applyReq         runtime.DesiredStateApplyRequest
	applyResultNames []string
	lastDesiredHash  string
	applied          bool
}

func (m *desiredStateLifecycleMockRuntime) Type() domain.RuntimeType {
	return domain.RuntimeTypeCompose
}
func (m *desiredStateLifecycleMockRuntime) SupportsDesiredState() bool { return true }
func (m *desiredStateLifecycleMockRuntime) Deploy(_ context.Context, _, _ string, _ runtime.DeployOptions) error {
	return errors.New("legacy deploy path must not be used for desired-state runtime")
}
func (m *desiredStateLifecycleMockRuntime) Undeploy(_ context.Context, _ string) error { return nil }
func (m *desiredStateLifecycleMockRuntime) StreamLogs(_ context.Context, _ string, _ runtime.LogOptions) (<-chan runtime.LogEntry, error) {
	ch := make(chan runtime.LogEntry)
	close(ch)
	return ch, nil
}
func (m *desiredStateLifecycleMockRuntime) ApplyDesiredState(_ context.Context, req runtime.DesiredStateApplyRequest) (*runtime.DesiredStateApplyResult, error) {
	m.applied = true
	m.applyReq = req
	m.applyResultNames = nil
	if req.EnvironmentPlan != nil {
		for _, spec := range req.EnvironmentPlan.Services {
			m.applyResultNames = append(m.applyResultNames, spec.StableServiceKey)
		}
	}
	if req.TargetService != nil {
		m.lastDesiredHash = req.TargetService.DesiredHash
	}
	return &runtime.DesiredStateApplyResult{
		Renderer:            "compose",
		ExecutionMode:       runtime.ExecutionModeCLI,
		DesiredHash:         m.lastDesiredHash,
		EnvironmentRevision: req.EnvironmentPlan.RevisionHash,
		ResourceNames:       append([]string(nil), m.applyResultNames...),
	}, nil
}
func (m *desiredStateLifecycleMockRuntime) Observe(_ context.Context, serviceID, envID uuid.UUID, serviceName string) (*domain.RuntimeObservation, error) {
	return &domain.RuntimeObservation{
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:target",
		ObservedImageRepo:   "ghcr.io/org/api",
		ObservedContainerID: serviceName + "-container",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "desired-state-mock",
		NormalizedHash:      m.lastDesiredHash,
		ObservedAt:          time.Now().UTC(),
	}, nil
}

var _ runtime.DesiredStateApplier = (*desiredStateLifecycleMockRuntime)(nil)

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
	deployed      []string
	deployErr     error
	restartTarget string
	stopTarget    string
}

func (m *lifecycleMockRuntime) Type() domain.RuntimeType { return domain.RuntimeTypeDocker }

func (m *lifecycleMockRuntime) Deploy(_ context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	m.deployTarget = serviceName
	m.deployImage = image
	m.deployOpts = opts
	m.deployed = append(m.deployed, serviceName)
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

func TestRuntimeLifecycleDeployWithStatusReportsStepProgression(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	var steps []DeployStep
	statusFn := func(_ context.Context, step DeployStep, _ string) {
		steps = append(steps, step)
	}

	obs, err := lifecycle.DeployWithStatus(ctx, svc.ID, env.ID, &artifact.ID, statusFn)
	if err != nil {
		t.Fatalf("DeployWithStatus returned error: %v", err)
	}
	if obs == nil {
		t.Fatal("expected observation, got nil")
	}

	expectedSteps := []DeployStep{
		DeployStepBuildingDesiredState,
		DeployStepLockingEnvironment,
		DeployStepRendering,
		DeployStepApplying,
		DeployStepObserving,
		DeployStepProjecting,
	}
	if len(steps) != len(expectedSteps) {
		t.Fatalf("expected %d steps, got %d: %v", len(expectedSteps), len(steps), steps)
	}
	for i, expected := range expectedSteps {
		if steps[i] != expected {
			t.Fatalf("step %d: expected %q, got %q", i, expected, steps[i])
		}
	}
}

func TestRuntimeLifecycleDeployNilStatusCallbackDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop())

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// Deploy (no status callback) should work exactly as before.
	obs, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
	if err != nil {
		t.Fatalf("Deploy returned error: %v", err)
	}
	if obs == nil || obs.ObservedContainerID == "" {
		t.Fatalf("expected observation, got %#v", obs)
	}
}

func TestRuntimeLifecycleConcurrentDeploysSerialize(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)

	// Track deploy ordering with a channel.
	orderCh := make(chan int, 10)
	rt := &orderingMockRuntime{orderCh: orderCh}
	resolver := &mockRuntimeResolver{rt: rt}

	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, resolver, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)

	svc, env, artifact := seedRuntimeLifecycleFixtures(t, registry)

	// Launch two concurrent deploys for the same environment.
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := lifecycle.Deploy(ctx, svc.ID, env.ID, &artifact.ID)
			errs <- err
		}()
	}

	// Collect results.
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent deploy %d failed: %v", i, err)
		}
	}

	// Verify deploys completed (both should have run, serialized).
	close(orderCh)
	var order []int
	for v := range orderCh {
		order = append(order, v)
	}
	if len(order) != 2 {
		t.Fatalf("expected 2 serialized deploys, got %d", len(order))
	}
}

// inMemoryApplyLock is a test-only EnvironmentApplyLocker using sync.Mutex.
type inMemoryApplyLock struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
}

func newInMemoryApplyLock() *inMemoryApplyLock {
	return &inMemoryApplyLock{locks: map[uuid.UUID]*sync.Mutex{}}
}

func (a *inMemoryApplyLock) Lock(_ context.Context, envID uuid.UUID) (func(), error) {
	envLock := a.lockFor(envID)
	envLock.Lock()
	return func() { envLock.Unlock() }, nil
}

func (a *inMemoryApplyLock) TryLock(_ context.Context, envID uuid.UUID) (func(), bool, error) {
	envLock := a.lockFor(envID)
	if !envLock.TryLock() {
		return nil, false, nil
	}
	return func() { envLock.Unlock() }, true, nil
}

func (a *inMemoryApplyLock) lockFor(envID uuid.UUID) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.locks[envID]; !ok {
		a.locks[envID] = &sync.Mutex{}
	}
	return a.locks[envID]
}

// orderingMockRuntime records deploy order via a channel.
type orderingMockRuntime struct {
	lifecycleMockRuntime
	orderCh chan int
	counter int32
}

func (m *orderingMockRuntime) Deploy(ctx context.Context, serviceName, image string, opts runtime.DeployOptions) error {
	n := atomic.AddInt32(&m.counter, 1)
	m.orderCh <- int(n)
	return nil
}

func TestRuntimeLifecycleAutoRemediationDoesNotBlockBehindActiveUserApply(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	rt := &lifecycleMockRuntime{}
	lock := newInMemoryApplyLock()
	lifecycle := NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo,
		&mockRuntimeResolver{rt: rt}, &events.NoopPublisher{}, zap.NewNop(),
		WithRuntimeApplyLock(lock),
	)
	svc, env, _ := seedRuntimeLifecycleFixtures(t, registry)

	unlock, err := lock.Lock(ctx, env.ID)
	if err != nil {
		t.Fatalf("lock setup failed: %v", err)
	}
	defer unlock()

	_, err = lifecycle.AutoRemediateDesiredState(ctx, svc.ID, env.ID, nil)
	if !errors.Is(err, ErrEnvironmentApplyLockContended) {
		t.Fatalf("expected lock contention, got %v", err)
	}
	if len(rt.deployed) != 0 {
		t.Fatalf("auto-remediation deployed despite active user lock: %#v", rt.deployed)
	}
}

func TestRuntimeLifecycleDispatchesCorrectly(t *testing.T) {
	ctx := context.Background()
	registry, svcRepo, envRepo, _, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	publisher := &capturePublisher{}
	rt := &lifecycleMockRuntime{}
	lifecycle := NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, &mockRuntimeResolver{rt: rt}, publisher, zap.NewNop())

	svc, env, _ := seedRuntimeLifecycleFixtures(t, registry)

	// Restart dispatches through LifecycleRuntime, not deployDesiredState.
	if _, err := lifecycle.Restart(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Restart error: %v", err)
	}
	if rt.restartTarget != "legacy-api" {
		t.Fatalf("expected restart target, got %q", rt.restartTarget)
	}
	if rt.deployTarget != "" {
		t.Fatalf("restart should not trigger deploy, got target %q", rt.deployTarget)
	}

	// Stop dispatches through LifecycleRuntime, not deployDesiredState.
	if _, err := lifecycle.Stop(ctx, svc.ID, env.ID); err != nil {
		t.Fatalf("Stop error: %v", err)
	}
	if rt.stopTarget != "legacy-api" {
		t.Fatalf("expected stop target, got %q", rt.stopTarget)
	}

	// Verify events.
	if !publisher.hasEvent(runtimeActionRestartEvent) {
		t.Fatalf("expected runtime.restart event")
	}
	if !publisher.hasEvent(runtimeActionStopEvent) {
		t.Fatalf("expected runtime.stop event")
	}
}
