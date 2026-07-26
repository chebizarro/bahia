package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	runtimeAdapter "github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

func TestEnsureAdoptionDeploymentUnitCreatesServiceScopedUnitIdempotently(t *testing.T) {
	repo := &mockDeploymentUnitRepo{}
	adoption := &AdoptionService{}
	env := &domain.Environment{ID: uuid.New()}
	target := AdoptionTarget{EndpointRef: "edge-01-docker"}
	discovered := runtimeAdapter.DiscoveredContainer{
		SourceRuntime: "compose",
		Compose:       &domain.ComposeMetadata{WorkingDir: "/srv/data/bahia-managed"},
	}

	first, err := adoption.ensureAdoptionDeploymentUnit(context.Background(), repo, env, target, discovered, "groups-relay")
	if err != nil {
		t.Fatal(err)
	}
	second, err := adoption.ensureAdoptionDeploymentUnit(context.Background(), repo, env, target, discovered, "groups-relay")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || repo.createCount != 1 {
		t.Fatalf("unit was not reused: first=%s second=%s creates=%d", first.ID, second.ID, repo.createCount)
	}
	if first.Key != "groups-relay" || first.RuntimeType != domain.RuntimeTypeCompose ||
		first.EndpointRef != "edge-01-docker" || first.ComposeDir != "/srv/data/bahia-managed" ||
		first.ReconcileMode != domain.ReconcileModeAutoApply || first.OwnershipMode != domain.OwnershipModeBahiaManaged {
		t.Fatalf("unexpected deployment unit: %#v", first)
	}
}

type mockDeploymentUnitRepo struct {
	units       []domain.DeploymentUnit
	createCount int
}

func (r *mockDeploymentUnitRepo) Create(_ context.Context, unit *domain.DeploymentUnit) error {
	if unit.ID == uuid.Nil {
		unit.ID = uuid.New()
	}
	r.units = append(r.units, *unit)
	r.createCount++
	return nil
}

func (r *mockDeploymentUnitRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentUnit, error) {
	for i := range r.units {
		if r.units[i].ID == id {
			return &r.units[i], nil
		}
	}
	return nil, nil
}

func (r *mockDeploymentUnitRepo) GetByEnvironmentKey(_ context.Context, environmentID uuid.UUID, key string) (*domain.DeploymentUnit, error) {
	for i := range r.units {
		if r.units[i].EnvironmentID == environmentID && r.units[i].Key == key {
			return &r.units[i], nil
		}
	}
	return nil, nil
}

func (r *mockDeploymentUnitRepo) ListByEnvironment(_ context.Context, environmentID uuid.UUID) ([]domain.DeploymentUnit, error) {
	var out []domain.DeploymentUnit
	for _, unit := range r.units {
		if unit.EnvironmentID == environmentID {
			out = append(out, unit)
		}
	}
	return out, nil
}

func (r *mockDeploymentUnitRepo) ResolveDefault(_ context.Context, env *domain.Environment) (*domain.DeploymentUnit, error) {
	return domain.NewImplicitDefaultDeploymentUnit(env)
}

func TestAdoptionServiceImportInfersSingleOrgAndPersistsRuntimeIdentities(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	orgID := uuid.New()
	orgRepo := &mockOrgRepo{orgs: []domain.Organization{{ID: orgID, Name: "platform", DisplayName: "Platform"}}}
	identityRepo := newMockAdoptedIdentityRepo()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionOrganizations(orgRepo),
		WithAdoptionRuntimeIdentities(identityRepo),
	)

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v", results)
	}
	svc, err := svcRepo.GetByName(ctx, "demo-web")
	if err != nil || svc == nil {
		t.Fatalf("expected adopted service, svc=%#v err=%v", svc, err)
	}
	if svc.OrgID != orgID {
		t.Fatalf("service org_id = %s, want %s", svc.OrgID, orgID)
	}
	env, err := envRepo.GetByName(ctx, "prod")
	if err != nil || env == nil {
		t.Fatalf("expected adopted environment, env=%#v err=%v", env, err)
	}
	if env.OrgID != orgID {
		t.Fatalf("environment org_id = %s, want %s", env.OrgID, orgID)
	}
	for _, kind := range []string{"container_id", "image_digest", "compose_coordinates", "endpoint_target"} {
		if identityRepo.byKind(kind) == nil {
			t.Fatalf("missing persisted adopted runtime identity kind %q: %#v", kind, identityRepo.identities)
		}
	}
}

func TestAdoptionServiceImportRequiresOrgWhenMultipleOrgsAreAvailable(t *testing.T) {
	orgRepo := &mockOrgRepo{orgs: []domain.Organization{
		{ID: uuid.New(), Name: "platform-a", DisplayName: "Platform A"},
		{ID: uuid.New(), Name: "platform-b", DisplayName: "Platform B"},
	}}
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionOrganizations(orgRepo),
	)

	_, err := adoption.Import(context.Background(), AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: "http://docker.example", EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires org_id") {
		t.Fatalf("Import error = %v, want org_id requirement", err)
	}
}

func TestAdoptionServiceImportSeedsModelsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	obsRepo := registry.observations.(*mockObsRepo)
	publisher := &capturePublisher{}
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, publisher, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 import result, got %d", len(results))
	}
	if results[0].Status != adoptionStatusCreated || results[0].Error != "" {
		t.Fatalf("unexpected import result: %#v", results[0])
	}

	env, err := envRepo.GetByName(ctx, "prod")
	if err != nil || env == nil {
		t.Fatalf("expected environment to be created, env=%#v err=%v", env, err)
	}
	if env.RuntimeConfig["type"] != "docker" || env.RuntimeConfig["docker_host"] != server.URL || env.RuntimeConfig["host_alias"] != "local" || env.RuntimeConfig["management_mode"] != "direct_runtime" {
		t.Fatalf("unexpected environment runtime config: %#v", env.RuntimeConfig)
	}

	svc, err := svcRepo.GetByName(ctx, "demo-web")
	if err != nil || svc == nil {
		t.Fatalf("expected service demo-web, svc=%#v err=%v", svc, err)
	}
	if svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("expected adopted runtime config: %#v", svc.RuntimeConfig)
	}
	adopted := svc.RuntimeConfig.Adopted
	if adopted.TargetName != "demo-web-1" || adopted.HostAlias != "local" || adopted.SourceRuntime != "compose" {
		t.Fatalf("unexpected adopted identity: %#v", adopted)
	}
	if adopted.Environment["APP_ENV"] != "prod" || adopted.Ports[0] != "8080:80" || adopted.Restart != "unless-stopped" || adopted.Compose == nil {
		t.Fatalf("unexpected adopted runtime shape: %#v", adopted)
	}

	if len(buildRepo.builds) != 1 {
		t.Fatalf("expected one synthetic build, got %d", len(buildRepo.builds))
	}
	if len(artifactRepo.artifacts) != 1 {
		t.Fatalf("expected one artifact, got %d", len(artifactRepo.artifacts))
	}
	state := stateRepo.states[stateKey(*results[0].ServiceID, *results[0].EnvironmentID)]
	if state == nil || state.DesiredArtifactID == nil || *state.DesiredArtifactID != *results[0].ArtifactID {
		t.Fatalf("state was not seeded with desired artifact: %#v result=%#v", state, results[0])
	}
	if state.CurrentObservationID == nil {
		t.Fatalf("state was not updated with current observation: %#v", state)
	}
	if len(obsRepo.observations) != 1 {
		t.Fatalf("expected one recorded observation, got %d", len(obsRepo.observations))
	}
	if !publisher.hasEvent(adoptionImportedEvent) {
		t.Fatalf("expected adoption.imported event, got %#v", publisher.events)
	}

	results, err = adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("second Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusUpdated {
		t.Fatalf("expected idempotent update result, got %#v", results)
	}
	if len(buildRepo.builds) != 1 || len(artifactRepo.artifacts) != 1 {
		t.Fatalf("expected build/artifact dedupe, builds=%d artifacts=%d", len(buildRepo.builds), len(artifactRepo.artifacts))
	}
	if len(obsRepo.observations) != 2 {
		t.Fatalf("expected re-import to record another observation, got %d", len(obsRepo.observations))
	}
}

func TestAdoptionServiceComposeTakeoverPolicyWarningsAndBlocksImport(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionComposeTakeoverPolicy(false),
	)

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}}})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v", previews)
	}
	candidate := previews[0].Containers[0]
	if candidate.Adoptable {
		t.Fatalf("compose-origin candidate should be non-adoptable when takeover is disabled")
	}
	if !containsString(candidate.Warnings, "compose-origin workload will be taken over by Bahia direct Docker runtime actions") || !containsString(candidate.Warnings, "compose takeover is disabled by adoption policy") {
		t.Fatalf("compose takeover warnings missing: %#v", candidate.Warnings)
	}

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "unsupported adoption warnings") {
		t.Fatalf("expected compose takeover import failure, got %#v", results)
	}
	if len(svcRepo.services) != 0 || len(envRepo.envs) != 0 || len(buildRepo.builds) != 0 || len(artifactRepo.artifacts) != 0 {
		t.Fatalf("blocked compose takeover should not create rows")
	}
}

func TestAdoptionServiceImportTransactionalRollbackOnStateFailure(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	obsRepo := registry.observations.(*mockObsRepo)
	stateRepo.upsertErr = fmt.Errorf("simulated state failure")
	publisher := &capturePublisher{}
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, publisher, zap.NewNop(),
		WithAdoptionTxExecutor(newMockAdoptionTxExecutor(svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, nil)),
	)

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "simulated state failure") {
		t.Fatalf("expected transactional failure result, got %#v", results)
	}
	if results[0].ServiceID != nil || results[0].EnvironmentID != nil || results[0].BuildID != nil || results[0].ArtifactID != nil {
		t.Fatalf("failed transactional import leaked rolled-back ids: %#v", results[0])
	}
	if len(svcRepo.services) != 0 || len(envRepo.envs) != 0 || len(buildRepo.builds) != 0 || len(artifactRepo.artifacts) != 0 || len(stateRepo.states) != 0 || len(obsRepo.observations) != 0 {
		t.Fatalf("failed import left partial rows: services=%d envs=%d builds=%d artifacts=%d states=%d observations=%d",
			len(svcRepo.services), len(envRepo.envs), len(buildRepo.builds), len(artifactRepo.artifacts), len(stateRepo.states), len(obsRepo.observations))
	}
	if publisher.hasEvent(adoptionImportedEvent) {
		t.Fatalf("failed transactional import published event: %#v", publisher.events)
	}
}

func TestAdoptionServiceImportRetriesAfterTransactionalDuplicateRace(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	obsRepo := registry.observations.(*mockObsRepo)
	txExecutor := newMockAdoptionTxExecutor(svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, nil)
	txExecutor.failures = []error{&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}}
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionTxExecutor(txExecutor),
	)

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated || results[0].Error != "" {
		t.Fatalf("expected retry to converge on successful import, got %#v", results)
	}
	if len(svcRepo.services) != 1 || len(buildRepo.builds) != 1 || len(artifactRepo.artifacts) != 1 {
		t.Fatalf("retry-safe import did not create canonical rows: services=%d builds=%d artifacts=%d", len(svcRepo.services), len(buildRepo.builds), len(artifactRepo.artifacts))
	}
}

func TestAdoptionServiceImportTransactionalDuplicateImportConverges(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	stateRepo := registry.state.(*mockStateRepo)
	obsRepo := registry.observations.(*mockObsRepo)
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionTxExecutor(newMockAdoptionTxExecutor(svcRepo, envRepo, buildRepo, artifactRepo, stateRepo, obsRepo, nil)),
	)

	for i, wantStatus := range []string{adoptionStatusCreated, adoptionStatusUpdated} {
		results, err := adoption.Import(ctx, AdoptionImportRequest{
			Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
			ImportAll: true,
		})
		if err != nil {
			t.Fatalf("Import %d returned error: %v", i+1, err)
		}
		if len(results) != 1 || results[0].Status != wantStatus || results[0].Error != "" {
			t.Fatalf("unexpected import %d result: %#v", i+1, results)
		}
	}
	if len(svcRepo.services) != 1 || len(envRepo.envs) != 1 || len(buildRepo.builds) != 1 || len(artifactRepo.artifacts) != 1 || len(stateRepo.states) != 1 {
		t.Fatalf("duplicate import did not converge: services=%d envs=%d builds=%d artifacts=%d states=%d",
			len(svcRepo.services), len(envRepo.envs), len(buildRepo.builds), len(artifactRepo.artifacts), len(stateRepo.states))
	}
	if len(obsRepo.observations) != 2 {
		t.Fatalf("expected each committed import to record one observation, got %d", len(obsRepo.observations))
	}
	svc, err := svcRepo.GetByName(ctx, "demo-web")
	if err != nil || svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("expected adopted service, svc=%#v err=%v", svc, err)
	}
	if svc.RuntimeConfig.Adopted.ContainerID != "container-123" {
		t.Fatalf("adopted container_id not recorded: %#v", svc.RuntimeConfig.Adopted)
	}
	for _, build := range buildRepo.builds {
		if build.CIRunID != "local:demo-web-1:sha256:repo123" {
			t.Fatalf("stable adoption run id = %q", build.CIRunID)
		}
	}
}

func TestAdoptionServiceScanRedactsSensitiveEnvironmentAndLabels(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}}})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v", previews)
	}
	container := previews[0].Containers[0]
	if got := container.SafeEnvironment["APP_ENV"]; got != "prod" {
		t.Fatalf("safe env APP_ENV = %q", got)
	}
	if _, ok := container.SafeEnvironment["DB_PASSWORD"]; ok {
		t.Fatal("sensitive env DB_PASSWORD leaked into safe preview environment")
	}
	if _, ok := container.SafeEnvironment["MINT_LND_REST_MACAROON"]; ok {
		t.Fatal("sensitive env MINT_LND_REST_MACAROON leaked into safe preview environment")
	}
	if _, ok := container.SafeEnvironment["FLEET_CURATOR_BUNKER_URL"]; ok {
		t.Fatal("sensitive env FLEET_CURATOR_BUNKER_URL leaked into safe preview environment")
	}
	if !containsString(container.RedactedEnvironmentKeys, "DB_PASSWORD") || !containsString(container.RedactedEnvironmentKeys, "AWS_SECRET_ACCESS_KEY") || !containsString(container.RedactedEnvironmentKeys, "DATABASE_URL") || !containsString(container.RedactedEnvironmentKeys, "MINT_LND_REST_MACAROON") || !containsString(container.RedactedEnvironmentKeys, "FLEET_CURATOR_BUNKER_URL") {
		t.Fatalf("missing redacted env keys: %#v", container.RedactedEnvironmentKeys)
	}
	if _, ok := container.SafeLabels["com.example.secret-token"]; ok {
		t.Fatal("sensitive label leaked into safe preview labels")
	}
	if !containsString(container.RedactedLabelKeys, "com.example.secret-token") {
		t.Fatalf("missing redacted label keys: %#v", container.RedactedLabelKeys)
	}
}

func TestAdoptionServiceImportStoresSensitiveEnvironmentAsSecrets(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	secretRepo := newMockSecretRepo()
	encryptor, err := secretsAdapter.NewEncryptor("4444444444444444444444444444444444444444444444444444444444444444")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionSecrets(secretRepo, encryptor),
	)

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v", results)
	}
	if !containsString(results[0].RedactedEnvironmentKeys, "DB_PASSWORD") {
		t.Fatalf("import result missing redacted env keys: %#v", results[0].RedactedEnvironmentKeys)
	}
	svc, err := svcRepo.GetByName(ctx, "secret-app")
	if err != nil || svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("expected adopted service, svc=%#v err=%v", svc, err)
	}
	if svc.RuntimeConfig.Adopted.Environment["APP_ENV"] != "prod" {
		t.Fatalf("safe env not retained: %#v", svc.RuntimeConfig.Adopted.Environment)
	}
	if _, ok := svc.RuntimeConfig.Adopted.Environment["DB_PASSWORD"]; ok {
		t.Fatalf("sensitive env persisted in plaintext runtime config: %#v", svc.RuntimeConfig.Adopted.Environment)
	}
	if _, ok := svc.RuntimeConfig.Adopted.Labels["com.example.secret-token"]; ok {
		t.Fatalf("sensitive label persisted in adopted labels: %#v", svc.RuntimeConfig.Adopted.Labels)
	}
	secrets, err := secretRepo.ListEffective(ctx, svc.ID, *results[0].EnvironmentID)
	if err != nil {
		t.Fatalf("ListEffective secrets: %v", err)
	}
	secretValues := map[string]string{}
	for _, secret := range secrets {
		value, err := encryptor.Decrypt(secret.EncryptedValue, secret.EncryptionMethod)
		if err != nil {
			t.Fatalf("decrypt secret %q: %v", secret.Name, err)
		}
		secretValues[secret.Name] = value
	}
	if secretValues["DB_PASSWORD"] != "super-secret" || secretValues["AWS_SECRET_ACCESS_KEY"] != "aws-secret" || secretValues["DATABASE_URL"] != "postgres://user:pass@db/prod" {
		t.Fatalf("unexpected imported secret values: %#v", secretValues)
	}
}

func TestAdoptionServiceImportRejectsSensitiveEnvironmentWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	server := newSensitiveAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "secret storage") {
		t.Fatalf("expected sensitive import failure, got %#v", results)
	}
	if len(svcRepo.services) != 0 || len(envRepo.envs) != 0 {
		t.Fatalf("sensitive import without secrets should not create service/env, services=%d envs=%d", len(svcRepo.services), len(envRepo.envs))
	}
}

func TestAdoptionServiceUsesManagedEndpointRefAndDoesNotPersistDockerHost(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionRuntimeConfig(config.RuntimeConfig{
			Endpoints: map[string]config.RuntimeEndpointConfig{
				"prod-docker": {DockerHost: server.URL},
			},
		}, false),
	)

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusCreated {
		t.Fatalf("unexpected import result: %#v", results)
	}
	env, err := envRepo.GetByName(ctx, "prod")
	if err != nil || env == nil {
		t.Fatalf("expected environment, env=%#v err=%v", env, err)
	}
	if env.RuntimeConfig["endpoint_ref"] != "prod-docker" {
		t.Fatalf("endpoint_ref not persisted: %#v", env.RuntimeConfig)
	}
	if _, ok := env.RuntimeConfig["docker_host"]; ok {
		t.Fatalf("managed endpoint import persisted docker_host: %#v", env.RuntimeConfig)
	}
	svc, err := svcRepo.GetByName(ctx, "demo-web")
	if err != nil || svc == nil || svc.RuntimeConfig == nil || svc.RuntimeConfig.Adopted == nil {
		t.Fatalf("expected adopted service, svc=%#v err=%v", svc, err)
	}
	if svc.RuntimeConfig.Adopted.EndpointRef != "prod-docker" {
		t.Fatalf("adopted endpoint_ref = %q", svc.RuntimeConfig.Adopted.EndpointRef)
	}
}

func TestAdoptionServiceRejectsRawDockerHostWhenPolicyDisabled(t *testing.T) {
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(
		registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop(),
		WithAdoptionRuntimeConfig(config.RuntimeConfig{Endpoints: map[string]config.RuntimeEndpointConfig{}}, false),
	)

	_, err := adoption.Scan(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock"}}})
	if err == nil || !strings.Contains(err.Error(), "raw docker_host targets are disabled") {
		t.Fatalf("Scan error = %v, want raw host policy rejection", err)
	}
}

func TestAdoptionServiceImportUsesExistingAdoptedIdentityDespiteNewOverride(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	first, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123", ServiceNameOverride: "first-name"}},
	})
	if err != nil || len(first) != 1 || first[0].Status != adoptionStatusCreated {
		t.Fatalf("first import failed: results=%#v err=%v", first, err)
	}
	second, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123", ServiceNameOverride: "second-name"}},
	})
	if err != nil || len(second) != 1 || second[0].Status != adoptionStatusUpdated {
		t.Fatalf("second import failed: results=%#v err=%v", second, err)
	}
	if second[0].ServiceName != "first-name" || second[0].ServiceID == nil || first[0].ServiceID == nil || *second[0].ServiceID != *first[0].ServiceID {
		t.Fatalf("expected re-import to update canonical service, first=%#v second=%#v", first[0], second[0])
	}
	if len(svcRepo.services) != 1 {
		t.Fatalf("expected one service after re-import, got %d", len(svcRepo.services))
	}
}

func TestAdoptionServiceImportRejectsForeignArtifactDigest(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	foreignServiceID := uuid.New()
	foreignBuildID := uuid.New()
	if err := artifactRepo.Create(ctx, &domain.Artifact{BuildID: foreignBuildID, ServiceID: foreignServiceID, ImageRepo: "registry.example/web", ImageTag: "other", ImageDigest: "sha256:repo123", ScanStatus: domain.ScanStatusUnknown}); err != nil {
		t.Fatalf("seed foreign artifact: %v", err)
	}
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "already belongs to service") {
		t.Fatalf("expected foreign artifact failure, got %#v", results)
	}
}

func TestAdoptionServiceImportRejectsIncompatibleExistingEnvironment(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	if err := envRepo.Create(ctx, &domain.Environment{Name: "prod", RuntimeConfig: map[string]any{"type": "compose", "compose_dir": "/srv/app"}}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:   []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		ImportAll: true,
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "incompatible runtime type") {
		t.Fatalf("expected incompatible environment failure, got %#v", results)
	}
}

func TestAdoptionServiceImportSelectionReportsScanFailure(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "docker unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "container-123"}},
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || results[0].ContainerID != "container-123" {
		t.Fatalf("expected selected scan failure result, got %#v", results)
	}
}

func TestAdoptionServiceImportSelectionReportsUndiscoveredContainer(t *testing.T) {
	ctx := context.Background()
	server := newAdoptionDockerServer(t)
	defer server.Close()
	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	results, err := adoption.Import(ctx, AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: server.URL, EnvironmentName: "prod"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "missing-container"}},
	})
	if err != nil {
		t.Fatalf("Import returned error: %v", err)
	}
	if len(results) != 1 || results[0].Status != adoptionStatusFailed || !strings.Contains(results[0].Error, "not discovered") {
		t.Fatalf("expected undiscovered selection failure, got %#v", results)
	}
}

func TestAdoptionServiceScanReportsNonAdoptableWarnings(t *testing.T) {
	ctx := context.Background()
	server := newUnsafeAdoptionDockerServer(t)
	defer server.Close()

	registry, svcRepo, envRepo, buildRepo, artifactRepo, _, _ := newTestRegistry()
	adoption := NewAdoptionService(registry, svcRepo, envRepo, buildRepo, artifactRepo, registry.state, registry.observations, &events.NoopPublisher{}, zap.NewNop())

	previews, err := adoption.Scan(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: server.URL}}})
	if err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}
	if len(previews) != 1 || len(previews[0].Containers) != 1 {
		t.Fatalf("unexpected previews: %#v", previews)
	}
	container := previews[0].Containers[0]
	if container.Adoptable {
		t.Fatalf("expected unsafe container to be non-adoptable")
	}
	if len(container.Warnings) == 0 {
		t.Fatalf("expected warnings for unsafe container")
	}
}

func newAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			if r.URL.Query().Get("all") != "1" {
				t.Errorf("containers query did not include all=1")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-123","Names":["/demo-web-1"],"Image":"registry.example/web:1.2.3","ImageID":"sha256:image123","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-123",
				"Name":"/demo-web-1",
				"Image":"sha256:image123",
				"Config":{
					"Image":"registry.example/web:1.2.3",
					"Env":["APP_ENV=prod"],
					"Labels":{
						"com.docker.compose.project":"demo",
						"com.docker.compose.service":"web",
						"org.opencontainers.image.revision":"abc123"
					},
					"Cmd":["serve"],
					"Entrypoint":["/entrypoint.sh"],
					"WorkingDir":"/app"
				},
				"State":{"Status":"running","Health":{"Status":"healthy"}},
				"HostConfig":{"Binds":["/host/data:/data:ro"],"NetworkMode":"demo_default","RestartPolicy":{"Name":"unless-stopped"}},
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostPort":"8080"}]},"Networks":{"demo_default":{"Aliases":["web","demo-web-1"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:image123/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:image123","RepoDigests":["registry.example/web@sha256:repo123"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

func newSensitiveAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-secret","Names":["/secret-app"],"Image":"registry.example/secret-app:2.0.0","ImageID":"sha256:secretimage","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-secret/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-secret",
				"Name":"/secret-app",
				"Image":"sha256:secretimage",
				"Config":{
					"Image":"registry.example/secret-app:2.0.0",
					"Env":["APP_ENV=prod","DB_PASSWORD=super-secret","AWS_SECRET_ACCESS_KEY=aws-secret","DATABASE_URL=postgres://user:pass@db/prod","MINT_LND_REST_MACAROON=hex-secret","FLEET_CURATOR_BUNKER_URL=bunker://pub?secret=secret"],
					"Labels":{"com.example.owner":"platform","com.example.secret-token":"label-secret"},
					"Cmd":["serve"]
				},
				"State":{"Status":"running"},
				"HostConfig":{"NetworkMode":"bridge","RestartPolicy":{"Name":"always"}},
				"NetworkSettings":{"Ports":{},"Networks":{"bridge":{"Aliases":["secret-app"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:secretimage/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:secretimage","RepoDigests":["registry.example/secret-app@sha256:secretrepo"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

func newUnsafeAdoptionDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"Id":"container-unsafe","Names":["/unsafe"],"Image":"registry.example/unsafe:latest","ImageID":"sha256:unsafe","State":"running"}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/container-unsafe/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"Id":"container-unsafe",
				"Name":"/unsafe",
				"Image":"sha256:unsafe",
				"Config":{"Image":"registry.example/unsafe:latest","Labels":{}},
				"State":{"Status":"running"},
				"HostConfig":{"NetworkMode":"custom"},
				"Mounts":[{"Type":"tmpfs","Destination":"/cache"}],
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostIP":"127.0.0.1","HostPort":"8080"},{"HostIP":"::","HostPort":"8080"}]},"Networks":{"custom":{"Aliases":["surprise"]}}}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/images/sha256:unsafe/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Id":"sha256:unsafe","RepoDigests":["registry.example/unsafe@sha256:unsafe"]}`))
		default:
			http.Error(w, fmt.Sprintf("unexpected request: %s %s", r.Method, r.URL.String()), http.StatusNotFound)
		}
	}))
}

type mockOrgRepo struct {
	orgs []domain.Organization
}

func (m *mockOrgRepo) Create(_ context.Context, org *domain.Organization) error {
	if org.ID == uuid.Nil {
		org.ID = uuid.New()
	}
	m.orgs = append(m.orgs, *org)
	return nil
}

func (m *mockOrgRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	for i := range m.orgs {
		if m.orgs[i].ID == id {
			return &m.orgs[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockOrgRepo) GetByName(_ context.Context, name string) (*domain.Organization, error) {
	for i := range m.orgs {
		if m.orgs[i].Name == name {
			return &m.orgs[i], nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockOrgRepo) List(_ context.Context) ([]domain.Organization, error) {
	out := make([]domain.Organization, len(m.orgs))
	copy(out, m.orgs)
	return out, nil
}

func (m *mockOrgRepo) Update(_ context.Context, org *domain.Organization) error {
	for i := range m.orgs {
		if m.orgs[i].ID == org.ID {
			m.orgs[i] = *org
			return nil
		}
	}
	return repository.ErrNotFound
}

func (m *mockOrgRepo) Delete(_ context.Context, id uuid.UUID) error {
	for i := range m.orgs {
		if m.orgs[i].ID == id {
			m.orgs = append(m.orgs[:i], m.orgs[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type mockAdoptedIdentityRepo struct {
	identities map[string]domain.AdoptedRuntimeIdentity
}

func newMockAdoptedIdentityRepo() *mockAdoptedIdentityRepo {
	return &mockAdoptedIdentityRepo{identities: map[string]domain.AdoptedRuntimeIdentity{}}
}

func (m *mockAdoptedIdentityRepo) UpsertMany(_ context.Context, identities []domain.AdoptedRuntimeIdentity) error {
	for _, identity := range identities {
		if identity.ID == uuid.Nil {
			identity.ID = uuid.New()
		}
		m.identities[identity.OrgID.String()+"/"+identity.Fingerprint] = identity
	}
	return nil
}

func (m *mockAdoptedIdentityRepo) FindByFingerprints(_ context.Context, orgID uuid.UUID, fingerprints []string) ([]domain.AdoptedRuntimeIdentity, error) {
	var out []domain.AdoptedRuntimeIdentity
	for _, fingerprint := range fingerprints {
		if identity, ok := m.identities[orgID.String()+"/"+fingerprint]; ok {
			out = append(out, identity)
		}
	}
	return out, nil
}

func (m *mockAdoptedIdentityRepo) byKind(kind string) *domain.AdoptedRuntimeIdentity {
	for _, identity := range m.identities {
		if identity.FingerprintKind == kind {
			copy := identity
			return &copy
		}
	}
	return nil
}

type capturePublisher struct {
	events []events.Event
}

func (p *capturePublisher) Publish(_ context.Context, e events.Event) {
	p.events = append(p.events, e)
}

func (p *capturePublisher) Subscribe(_ events.EventType, _ events.Handler) {}

type mockAdoptionTxExecutor struct {
	failures     []error
	services     *mockServiceRepo
	environments *mockEnvRepo
	builds       *mockBuildRepo
	artifacts    *mockArtifactRepo
	state        *mockStateRepo
	observations *mockObsRepo
	secrets      *mockSecretRepo
	identities   *mockAdoptedIdentityRepo
}

func newMockAdoptionTxExecutor(services *mockServiceRepo, environments *mockEnvRepo, builds *mockBuildRepo, artifacts *mockArtifactRepo, state *mockStateRepo, observations *mockObsRepo, secrets *mockSecretRepo) *mockAdoptionTxExecutor {
	return &mockAdoptionTxExecutor{services: services, environments: environments, builds: builds, artifacts: artifacts, state: state, observations: observations, secrets: secrets}
}

func (e *mockAdoptionTxExecutor) WithinTx(ctx context.Context, fn func(repos repository.TxRepos) error) error {
	if len(e.failures) > 0 {
		err := e.failures[0]
		e.failures = e.failures[1:]
		return err
	}
	txServices := cloneMockServiceRepo(e.services)
	txEnvironments := cloneMockEnvRepo(e.environments)
	txBuilds := cloneMockBuildRepo(e.builds)
	txArtifacts := cloneMockArtifactRepo(e.artifacts)
	txState := cloneMockStateRepo(e.state)
	txObservations := cloneMockObsRepo(e.observations)
	txSecrets := cloneMockSecretRepo(e.secrets)
	txIdentities := cloneMockAdoptedIdentityRepo(e.identities)

	txRepos := repository.TxRepos{
		Services:     txServices,
		Environments: txEnvironments,
		Builds:       txBuilds,
		Artifacts:    txArtifacts,
		State:        txState,
		Observations: txObservations,
		Secrets:      txSecrets,
	}
	if txIdentities != nil {
		txRepos.AdoptedIdentities = txIdentities
	}
	if err := fn(txRepos); err != nil {
		return err
	}

	e.services.services = txServices.services
	e.environments.envs = txEnvironments.envs
	e.builds.builds = txBuilds.builds
	e.artifacts.artifacts = txArtifacts.artifacts
	e.state.states = txState.states
	e.observations.observations = txObservations.observations
	if e.secrets != nil && txSecrets != nil {
		e.secrets.secrets = txSecrets.secrets
	}
	if e.identities != nil && txIdentities != nil {
		e.identities.identities = txIdentities.identities
	}
	_ = ctx
	return nil
}

func cloneMockServiceRepo(src *mockServiceRepo) *mockServiceRepo {
	clone := newMockServiceRepo()
	for id, svc := range src.services {
		copied := *svc
		clone.services[id] = &copied
	}
	return clone
}

func cloneMockEnvRepo(src *mockEnvRepo) *mockEnvRepo {
	clone := newMockEnvRepo()
	for id, env := range src.envs {
		copied := *env
		if env.RuntimeConfig != nil {
			copied.RuntimeConfig = map[string]any{}
			for k, v := range env.RuntimeConfig {
				copied.RuntimeConfig[k] = v
			}
		}
		clone.envs[id] = &copied
	}
	return clone
}

func cloneMockBuildRepo(src *mockBuildRepo) *mockBuildRepo {
	clone := newMockBuildRepo()
	for id, build := range src.builds {
		copied := *build
		clone.builds[id] = &copied
	}
	return clone
}

func cloneMockArtifactRepo(src *mockArtifactRepo) *mockArtifactRepo {
	clone := newMockArtifactRepo()
	clone.getByIDErr = src.getByIDErr
	for id, artifact := range src.artifacts {
		copied := *artifact
		clone.artifacts[id] = &copied
	}
	return clone
}

func cloneMockStateRepo(src *mockStateRepo) *mockStateRepo {
	clone := newMockStateRepo()
	clone.upsertErr = src.upsertErr
	clone.getErr = src.getErr
	for key, state := range src.states {
		copied := *state
		clone.states[key] = &copied
	}
	return clone
}

func cloneMockObsRepo(src *mockObsRepo) *mockObsRepo {
	clone := newMockObsRepo()
	for id, obs := range src.observations {
		copied := *obs
		clone.observations[id] = &copied
	}
	return clone
}

func cloneMockSecretRepo(src *mockSecretRepo) *mockSecretRepo {
	if src == nil {
		return nil
	}
	clone := newMockSecretRepo()
	for id, secret := range src.secrets {
		copied := *secret
		clone.secrets[id] = &copied
	}
	return clone
}

func cloneMockAdoptedIdentityRepo(src *mockAdoptedIdentityRepo) *mockAdoptedIdentityRepo {
	if src == nil {
		return nil
	}
	clone := newMockAdoptedIdentityRepo()
	for fingerprint, identity := range src.identities {
		clone.identities[fingerprint] = identity
	}
	return clone
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (p *capturePublisher) hasEvent(eventType events.EventType) bool {
	for _, event := range p.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
