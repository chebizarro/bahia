package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mcpProjectionStore struct{ store *localstore.Store }

type mcpKeyEnvelopeSink struct{}

func (mcpKeyEnvelopeSink) PublishKeyEnvelope(context.Context, string, string) error { return nil }

type mcpSecretOrgResolver struct{ orgID string }

func (r mcpSecretOrgResolver) ServiceOrgID(context.Context, uuid.UUID) (string, error) {
	return r.orgID, nil
}
func (r mcpSecretOrgResolver) SecretOrgID(context.Context, uuid.UUID) (string, error) {
	return r.orgID, nil
}

func (s mcpProjectionStore) PublishProjection(_ context.Context, ev nostr.Event, _ string, _ *uuid.UUID) error {
	_, err := s.store.SaveEvent(ev)
	return err
}

func (s mcpProjectionStore) PublishBeforeCommit(_ context.Context, ev nostr.Event, _ string, _ *uuid.UUID) error {
	_, err := s.store.SaveEvent(ev)
	return err
}

func testStateStore(t *testing.T) *localstore.Store {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "state.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestMCPStoreReadsMatchRegistryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}

	serviceServer, services := newTestMCPServiceServer()
	service := domain.Service{
		ID: uuid.New(), Name: "api", RepoURL: "https://example.test/api.git",
		ArtifactRepo: "registry.example.test/api", DefaultBranch: "main",
		RuntimeType: domain.RuntimeTypeCompose,
		CreatedAt:   time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	services.services[service.ID] = &service
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	require.NoError(t, publisher.PublishServiceRegistry(ctx, &service, false))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_services", map[string]any{}},
		{"bahia_get_service", map[string]any{"service_id": service.ID.String()}},
		{"bahia_get_service", map[string]any{"name": service.Name}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := serviceServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}

	environmentServer, environments := newTestMCPEnvironmentServer()
	environment := domain.Environment{
		ID: uuid.New(), Name: "staging", Protected: true,
		DeployStrategy:     domain.DeployStrategyBlueGreen,
		LoomWorkerSelector: map[string]any{"tier": "small"},
		RuntimeConfig:      map[string]any{"replicas": float64(2)},
		CreatedAt:          service.CreatedAt, UpdatedAt: service.UpdatedAt,
	}
	environments.environments[environment.ID] = &environment
	environmentProjector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, environmentServer.registry, sink, nil, zap.NewNop())
	environmentPublisher := nostrpool.NewRelayFirstStatePublisher(environmentProjector, sink)
	require.NoError(t, environmentPublisher.PublishEnvironmentRegistry(ctx, &environment, nil, false))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_environments", map[string]any{}},
		{"bahia_get_environment", map[string]any{"environment_id": environment.ID.String()}},
		{"bahia_get_environment", map[string]any{"name": environment.Name}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := environmentServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}

	// A tombstone replaces the addressable record; no stale row is returned.
	require.NoError(t, publisher.PublishServiceRegistry(ctx, &service, true))
	deleted, err := storeServer.CallTool(ctx, "bahia_list_services", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, float64(0), decodeResultMap(t, deleted)["total"])
}

func TestMCPStoreReadIsDatabaseLess(t *testing.T) {
	store := testStateStore(t)
	sk := nostr.Generate()
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	result, err := server.CallTool(authorizedMCPContext(), "bahia_list_services", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, float64(0), decodeResultMap(t, result)["total"])
}

func TestMCPLLMRouteStoreMatchesRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	repositoryServer, routes, _ := newTestLLMRegistryServer()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	route := domain.LLMRoute{ID: uuid.New(), Name: "chat", Description: "chat route", CreatedAt: created, UpdatedAt: created}
	routes.routes[route.ID] = &route
	tags, content := controlplane.LLMRouteRegistryRecord(&route, false)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: append(nostr.Tags{{"d", route.ID.String()}, {"domain", "llm-route"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", ""}, {"deleted", "false"}, {"t", kinds.CPStateTopicLLMRoute}}, tags...), Content: content}
	ev.Tags[3][1] = fmt.Sprint(nostrpool.KindLLMRouteRegistry)
	signer, err := controlplane.NewPrivateKeySigner(sk.Hex())
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(ctx, signer, &ev))
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	want, err := repositoryServer.CallTool(ctx, "bahia_llm_list_routes", map[string]any{})
	require.NoError(t, err)
	got, err := storeServer.CallTool(ctx, "bahia_llm_list_routes", map[string]any{})
	require.NoError(t, err)
	require.False(t, got.IsError, "%v", got.Content)
	require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
}

func TestMCPBackupStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewBackupCanonicalPublisher(projector, zap.NewNop())
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	repo := domain.BackupRepository{ID: uuid.New(), Name: "archive", Backend: domain.BackupBackendKopia, RepositoryURI: "kopia://archive", CreatedAt: created, UpdatedAt: created}
	policy := domain.BackupPolicy{ID: uuid.New(), Name: "verified", RequireVerification: true, VerificationMode: domain.BackupVerificationKopiaSnapshotVerify, CreatedAt: created, UpdatedAt: created}
	recipe := domain.BackupRecipe{ID: uuid.New(), Name: "postgres", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repo.ID, PolicyID: &policy.ID, TargetRef: "/srv/postgres", CreatedAt: created, UpdatedAt: created}
	definition := domain.BackupDefinition{ID: uuid.New(), Name: "postgres-prod", RepositoryID: repo.ID, RepositoryName: repo.Name, PolicyID: policy.ID, PolicyName: policy.Name, RecipeID: recipe.ID, RecipeName: recipe.Name, RecipeVersion: recipe.Version, CreatedAt: created, UpdatedAt: created}
	for _, publish := range []func(context.Context) error{
		func(ctx context.Context) error { return publisher.PublishRepository(ctx, &repo) },
		func(ctx context.Context) error { return publisher.PublishPolicy(ctx, &policy) },
		func(ctx context.Context) error { return publisher.PublishRecipe(ctx, &recipe) },
		func(ctx context.Context) error { return publisher.PublishDefinition(ctx, &definition) },
	} {
		require.NoError(t, publish(ctx))
	}
	repositoryServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{BackupReadModels: &memoryBackupReadModels{
		repositories: []domain.BackupRepository{repo}, policies: []domain.BackupPolicy{policy},
		recipes: []domain.BackupRecipe{recipe}, definitions: []domain.BackupDefinition{definition},
	}})
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_backup_repositories", map[string]any{}},
		{"bahia_inspect_backup_repository", map[string]any{"repository_id": repo.ID.String()}},
		{"bahia_list_backup_policies", map[string]any{}},
		{"bahia_inspect_backup_policy", map[string]any{"policy_id": policy.ID.String()}},
		{"bahia_list_backup_recipes", map[string]any{}},
		{"bahia_inspect_backup_recipe", map[string]any{"recipe_id": recipe.ID.String()}},
		{"bahia_list_backup_definitions", map[string]any{}},
		{"bahia_inspect_backup_definition", map[string]any{"definition_id": definition.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPBuildArtifactStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	buildRepo, artifactRepo := newTestBuildRepo(), newTestArtifactRepo()
	registry := service.NewRegistryService(nil, nil, buildRepo, artifactRepo, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	repositoryServer := NewServer(registry, zap.NewNop())
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, repositoryServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	serviceID := uuid.New()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	build := domain.Build{ID: uuid.New(), ServiceID: serviceID, GitSHA: "abc123", GitRef: "main", CISystem: "loom", CIRunID: "run-1", Status: domain.BuildStatusSucceeded, CreatedAt: created}
	artifact := domain.Artifact{ID: uuid.New(), BuildID: build.ID, ServiceID: serviceID, ImageRepo: "registry.example.test/api", ImageTag: "v1", ImageDigest: "sha256:abc", ScanStatus: domain.ScanStatusClean, CreatedAt: created}
	buildRepo.builds[build.ID] = &build
	artifactRepo.artifacts[artifact.ID] = &artifact
	require.NoError(t, publisher.PublishBuildRegistry(ctx, &build, false))
	require.NoError(t, publisher.PublishArtifactRegistry(ctx, &artifact, false))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_builds", map[string]any{"service_id": serviceID.String()}},
		{"bahia_get_build", map[string]any{"build_id": build.ID.String()}},
		{"bahia_list_artifacts", map[string]any{"service_id": serviceID.String()}},
		{"bahia_get_artifact", map[string]any{"artifact_id": artifact.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPConfidentialChannelStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	signer, err := keyer.New(ctx, nil, sk.Hex(), nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: sk.Public().Hex(), Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewNotificationCanonicalPublisher(projector, encryptor, nil, zap.NewNop())
	repositoryServer, repo, _ := newTestMCPNotificationServer()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	channel := domain.NotificationChannel{ID: uuid.New(), OrgID: uuid.New(), Name: "alerts", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"url": "https://private.example.test/hook", "secret": "top-secret"}, EventFilter: map[string]any{"severity": "critical"}, Enabled: true, CreatedAt: created, UpdatedAt: created}
	repo.channels[channel.ID] = &channel
	require.NoError(t, publisher.PublishChannel(ctx, &channel))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), ConfidentialReader: encryptor})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_notification_channels", map[string]any{}},
		{"bahia_get_notification_channel", map[string]any{"channel_id": channel.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
			require.NotContains(t, got.Content[0].Text, "top-secret")
		})
	}
}

func TestMCPConfidentialSecretMetadataStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	signer, err := keyer.New(ctx, nil, sk.Hex(), nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: sk.Public().Hex(), Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	orgID := uuid.New()
	publisher := nostrpool.NewSecretCanonicalPublisher(projector, encryptor, mcpSecretOrgResolver{orgID.String()}, zap.NewNop())
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	secret := domain.ServiceSecret{ID: uuid.New(), ServiceID: uuid.New(), Name: "api-token", Version: 2, CreatedBy: "operator", CreatedAt: created, UpdatedAt: created}
	repo := newTestSecretRepo()
	repo.secrets[secret.ID] = &secret
	repositoryServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{SecretsRepo: repo})
	require.NoError(t, publisher.PublishSecretRef(ctx, secret.ToRef()))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), ConfidentialReader: encryptor})
	args := map[string]any{"service_id": secret.ServiceID.String()}
	want, err := repositoryServer.CallTool(ctx, "bahia_list_secrets", args)
	require.NoError(t, err)
	got, err := storeServer.CallTool(ctx, "bahia_list_secrets", args)
	require.NoError(t, err)
	require.False(t, got.IsError, "%v", got.Content)
	require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
}

func TestMCPDeploymentStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceRepo, environmentRepo, intentRepo, runRepo, stateRepo := newTestServiceRepo(), newTestEnvironmentRepo(), newTestDeploymentIntentRepo(), newTestRunRepo(), newTestDeploymentStateRepo()
	registry := service.NewRegistryService(serviceRepo, environmentRepo, nil, nil, intentRepo, runRepo, nil, stateRepo, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	repositoryServer := NewServer(registry, zap.NewNop())
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	serviceID, environmentID := uuid.New(), uuid.New()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	intent := domain.DeploymentIntent{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: uuid.New(), RequestedBy: "operator", Status: domain.IntentStatusPending, CreatedAt: created, UpdatedAt: created}
	run := domain.DeploymentRun{ID: uuid.New(), DeploymentIntentID: intent.ID, Status: domain.RunStatusQueued, CreatedAt: created, UpdatedAt: created}
	state := domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: environmentID, DesiredIntentID: &intent.ID, DriftStatus: domain.DriftStatusDrifted, UpdatedAt: created}
	intentRepo.intents[intent.ID] = &intent
	intentRepo.order = []uuid.UUID{intent.ID}
	runRepo.runs[run.ID] = &run
	stateRepo.states[deploymentStateKey(serviceID, environmentID)] = &state
	require.NoError(t, publisher.PublishDeploymentIntentRegistry(ctx, &intent, false))
	require.NoError(t, publisher.PublishDeploymentRunRegistry(ctx, &run, false))
	require.NoError(t, publisher.PublishState(ctx, &state, nil))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_intents", map[string]any{"service_id": serviceID.String(), "environment_id": environmentID.String()}},
		{"bahia_get_intent", map[string]any{"intent_id": intent.ID.String()}},
		{"bahia_list_runs", map[string]any{"intent_id": intent.ID.String()}},
		{"bahia_get_run", map[string]any{"run_id": run.ID.String()}},
		{"bahia_list_states", map[string]any{}},
		{"bahia_list_drifted", map[string]any{}},
		{"bahia_get_deployment_status", map[string]any{"service_id": serviceID.String(), "environment_id": environmentID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPDNSEndpointStoreReadsMatchProjectionFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewDNSCanonicalPublisher(projector, zap.NewNop())
	endpoint := domain.DNSEndpoint{ID: uuid.New(), Family: domain.DNSEndpointFamilyService, Name: "api", Environment: "prod", FQDN: "api.example.test", Coordinate: "endpoint:service:api:prod", Zone: "example.test", Address: "192.0.2.10", Source: "runtime", DriftStatus: domain.DriftStatusDrifted, Health: domain.HealthStatusHealthy, MaterializedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	_, _, err := publisher.PublishEndpoints(ctx, []domain.DNSEndpoint{endpoint})
	require.NoError(t, err)
	repositoryServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{DNSEndpoints: dnsEndpointListerFunc(func(context.Context) ([]domain.DNSEndpoint, error) { return []domain.DNSEndpoint{endpoint}, nil })})
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, name := range []string{"bahia_dns_list_endpoints", "bahia_dns_list_drift"} {
		t.Run(name, func(t *testing.T) {
			want, err := repositoryServer.CallTool(ctx, name, map[string]any{})
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, name, map[string]any{})
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPRunLogsUseStoreRunMetadata(t *testing.T) {
	ctx := authorizedMCPContext()
	repositoryServer, runID := newTestMCPRunLogServer(t, "stdout line", "stderr line", domain.RunStatusSucceeded)
	run, err := repositoryServer.registry.GetDeploymentRun(ctx, runID)
	require.NoError(t, err)
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, repositoryServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	require.NoError(t, publisher.PublishDeploymentRunRegistry(ctx, run, false))
	storeServer := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), LogService: repositoryServer.logService})
	args := map[string]any{"run_id": runID.String()}
	want, err := repositoryServer.CallTool(ctx, "bahia_get_run_logs", args)
	require.NoError(t, err)
	got, err := storeServer.CallTool(ctx, "bahia_get_run_logs", args)
	require.NoError(t, err)
	require.False(t, got.IsError, "%v", got.Content)
	require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
}
