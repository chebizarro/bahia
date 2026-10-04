package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mcpProjectionStore struct{ store *localstore.Store }

func (s mcpProjectionStore) Publish(_ context.Context, ev nostr.Event) (int, error) {
	_, err := s.store.SaveEvent(ev)
	if err != nil {
		return 0, err
	}
	return 1, nil
}

type mcpKeyEnvelopeSink struct{}

type mcpPackageFixtureRepo struct {
	repository.PackageControlPlaneRepository
	repo     domain.PackageRepository
	artifact domain.PackageArtifact
}

type mcpMLFixtureRepo struct {
	repository.MLRegistryRepository
	endpoint domain.MLInferenceEndpoint
	state    domain.MLInferenceState
	artifact domain.MLArtifactRef
	edges    []domain.MLProvenanceEdge
}

func (r *mcpMLFixtureRepo) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	if id == r.endpoint.ID {
		return &r.endpoint, nil
	}
	return nil, nil
}
func (r *mcpMLFixtureRepo) GetInferenceState(_ context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	if endpointID == r.state.EndpointID && envID == r.state.EnvironmentID {
		return &r.state, nil
	}
	return nil, nil
}
func (r *mcpMLFixtureRepo) ListInferenceStates(context.Context) ([]domain.MLInferenceState, error) {
	return []domain.MLInferenceState{r.state}, nil
}
func (r *mcpMLFixtureRepo) GetArtifactRef(_ context.Context, id uuid.UUID) (*domain.MLArtifactRef, error) {
	if id == r.artifact.ID {
		return &r.artifact, nil
	}
	return nil, nil
}
func (r *mcpMLFixtureRepo) ListProvenanceEdgesByArtifact(_ context.Context, id uuid.UUID) ([]domain.MLProvenanceEdge, error) {
	if id == r.artifact.ID {
		return r.edges, nil
	}
	return nil, nil
}
func (r *mcpMLFixtureRepo) GetMLDeploymentIntent(context.Context, uuid.UUID) (*domain.MLDeploymentIntent, error) {
	return nil, nil
}
func (r *mcpMLFixtureRepo) GetMLDeploymentRun(context.Context, uuid.UUID) (*domain.MLDeploymentRun, error) {
	return nil, nil
}

func (r *mcpPackageFixtureRepo) ListRepositories(context.Context, bool) ([]domain.PackageRepository, error) {
	return []domain.PackageRepository{r.repo}, nil
}
func (r *mcpPackageFixtureRepo) GetRepository(_ context.Context, id uuid.UUID) (*domain.PackageRepository, error) {
	if id == r.repo.ID {
		return &r.repo, nil
	}
	return nil, repository.ErrNotFound
}
func (r *mcpPackageFixtureRepo) GetRepositoryByName(_ context.Context, name string) (*domain.PackageRepository, error) {
	if name == r.repo.Name {
		return &r.repo, nil
	}
	return nil, repository.ErrNotFound
}
func (r *mcpPackageFixtureRepo) ListArtifacts(_ context.Context, id uuid.UUID, _, _ int) ([]domain.PackageArtifact, error) {
	if id == r.repo.ID {
		return []domain.PackageArtifact{r.artifact}, nil
	}
	return nil, nil
}
func (r *mcpPackageFixtureRepo) GetArtifact(_ context.Context, id uuid.UUID, namespace, name, version, filename string) (*domain.PackageArtifact, error) {
	if id == r.repo.ID && namespace == r.artifact.Namespace && name == r.artifact.PackageName && version == r.artifact.Version && filename == r.artifact.Filename {
		return &r.artifact, nil
	}
	return nil, repository.ErrNotFound
}

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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_services", map[string]any{}},
		{"bahia_get_service", map[string]any{"service_id": service.ID.String()}},
		{"bahia_get_service", map[string]any{"name": service.Name}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := serviceServer.legacyCallTool(ctx, tc.name, tc.args)
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
			want, err := environmentServer.legacyCallTool(ctx, tc.name, tc.args)
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
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	result, err := server.CallTool(authorizedMCPContext(), "bahia_list_services", map[string]any{})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Equal(t, float64(0), decodeResultMap(t, result)["total"])
}

func TestMCPAllStoreReadToolsAreDatabaseLess(t *testing.T) {
	store := testStateStore(t)
	sk := nostr.Generate()
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	id := uuid.New().String()
	args := map[string]any{"service_id": id, "environment_id": id, "artifact_id": id, "build_id": id, "intent_id": id, "run_id": id, "policy_id": id, "channel_id": id, "endpoint_id": id, "repository_id": id, "recipe_id": id, "definition_id": id, "restore_id": id, "retention_run_id": id, "worker_pubkey": "worker", "pubkey": "worker", "name": "missing"}
	ctx := authorizedMCPContext()
	count := 0
	for _, tool := range server.GetTools() {
		if _, handled := server.callStoreReadTool(ctx, tool.Name, args); !handled {
			continue
		}
		count++
		t.Run(tool.Name, func(t *testing.T) {
			result, err := server.CallTool(ctx, tool.Name, args)
			require.NoError(t, err)
			require.NotNil(t, result)
			if result.IsError {
				require.NotContains(t, strings.ToLower(result.Content[0].Text), "not configured")
			}
		})
	}
	require.Greater(t, count, 40)
	for _, name := range []string{"bahia_fips_list_mesh_nodes", "bahia_fips_mesh_status"} {
		t.Run(name, func(t *testing.T) {
			result, err := server.CallTool(ctx, name, args)
			require.NoError(t, err)
			require.False(t, result.IsError, "%v", result.Content)
		})
	}
	t.Run("bahia_worker_preview_eligibility", func(t *testing.T) {
		result, err := server.CallTool(ctx, "bahia_worker_preview_eligibility", map[string]any{"preview_id": "db-less", "workload_type": "worker_policy"})
		require.NoError(t, err)
		require.NotNil(t, result)
		if result.IsError {
			require.NotContains(t, strings.ToLower(result.Content[0].Text), "not configured")
		}
	})
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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	want, err := repositoryServer.legacyCallTool(ctx, "bahia_llm_list_routes", map[string]any{})
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
	run := domain.BackupRun{ID: uuid.New(), RecipeID: recipe.ID, RepositoryID: repo.ID, Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, TargetRef: recipe.TargetRef, CreatedAt: created, UpdatedAt: created}
	restore := domain.BackupRestoreRun{ID: uuid.New(), BackupRunID: run.ID, RecipeID: recipe.ID, RepositoryID: repo.ID, Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, CreatedAt: created, UpdatedAt: created}
	retention := domain.BackupRetentionRun{ID: uuid.New(), RepositoryID: repo.ID, Status: domain.RunStatusSucceeded, Backend: domain.BackupBackendKopia, CreatedAt: created, UpdatedAt: created}
	verification := domain.BackupVerificationRecord{ID: uuid.New(), BackupRunID: run.ID, Mode: domain.BackupVerificationKopiaSnapshotVerify, Status: domain.BackupVerificationSucceeded, Verified: true, CreatedAt: created, UpdatedAt: created}
	for _, publish := range []func(context.Context) error{
		func(ctx context.Context) error { return publisher.PublishRepository(ctx, &repo) },
		func(ctx context.Context) error { return publisher.PublishPolicy(ctx, &policy) },
		func(ctx context.Context) error { return publisher.PublishRecipe(ctx, &recipe) },
		func(ctx context.Context) error { return publisher.PublishDefinition(ctx, &definition) },
		func(ctx context.Context) error { return publisher.PublishRun(ctx, &run) },
		func(ctx context.Context) error { return publisher.PublishVerification(ctx, &verification) },
		func(ctx context.Context) error { return publisher.PublishRestore(ctx, &restore) },
		func(ctx context.Context) error { return publisher.PublishRetention(ctx, &retention) },
	} {
		require.NoError(t, publish(ctx))
	}
	repositoryServer := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{BackupReadModels: &memoryBackupReadModels{
		repositories: []domain.BackupRepository{repo}, policies: []domain.BackupPolicy{policy},
		recipes: []domain.BackupRecipe{recipe}, definitions: []domain.BackupDefinition{definition},
		runs: []domain.BackupRun{run}, restores: []domain.BackupRestoreRun{restore}, retentions: []domain.BackupRetentionRun{retention},
		verification: &verification,
	}})
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
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
		{"bahia_list_backup_runs", map[string]any{}},
		{"bahia_inspect_backup_run", map[string]any{"run_id": run.ID.String()}},
		{"bahia_list_backup_restores", map[string]any{}},
		{"bahia_inspect_backup_restore", map[string]any{"restore_id": restore.ID.String()}},
		{"bahia_list_backup_retention_runs", map[string]any{}},
		{"bahia_inspect_backup_retention_run", map[string]any{"retention_run_id": retention.ID.String()}},
	} {
		for _, name := range []string{tc.name, strings.TrimPrefix(tc.name, "bahia_")} {
			t.Run(name, func(t *testing.T) {
				want, err := repositoryServer.legacyCallTool(ctx, name, tc.args)
				require.NoError(t, err)
				got, err := storeServer.CallTool(ctx, name, tc.args)
				require.NoError(t, err)
				require.False(t, got.IsError, "%v", got.Content)
				require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
			})
		}
	}
}

func TestMCPBuildArtifactStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	buildRepo, artifactRepo := newTestBuildRepo(), newTestArtifactRepo()
	registry := service.NewRegistryService(nil, nil, buildRepo, artifactRepo, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	repositoryServer := newTestServer(registry, zap.NewNop())
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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
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
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), ConfidentialReader: encryptor})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_notification_channels", map[string]any{}},
		{"bahia_get_notification_channel", map[string]any{"channel_id": channel.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
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
	repositoryServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{SecretsRepo: repo})
	require.NoError(t, publisher.PublishSecretRef(ctx, secret.ToRef()))
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), ConfidentialReader: encryptor})
	args := map[string]any{"service_id": secret.ServiceID.String()}
	want, err := repositoryServer.legacyCallTool(ctx, "bahia_list_secrets", args)
	require.NoError(t, err)
	got, err := storeServer.CallTool(ctx, "bahia_list_secrets", args)
	require.NoError(t, err)
	require.False(t, got.IsError, "%v", got.Content)
	require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
}

func TestMCPPaymentStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	signer, err := keyer.New(ctx, nil, sk.Hex(), nil)
	require.NoError(t, err)
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: signer, ServicePubkey: sk.Public().Hex(), Publisher: mcpKeyEnvelopeSink{}})
	encryptor := controlplane.NewConfidentialEncryptor(manager, zap.NewNop())
	repositoryServer, paymentRepo, runID, workerPubkey := newTestMCPPaymentServer(t)
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	statePublisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	created := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	run := domain.DeploymentRun{ID: runID, DeploymentIntentID: uuid.New(), WorkerPubkey: workerPubkey, Status: domain.RunStatusRunning, CreatedAt: created, UpdatedAt: created}
	require.NoError(t, statePublisher.PublishDeploymentRunRegistry(ctx, &run, false))
	worker := domain.Worker{PubKey: workerPubkey, Name: "payment-worker", MaxDurationSecs: 300, Pricing: []domain.WorkerPricing{{MintURL: "https://mint.example", PricePerSecond: 4, Unit: "sat"}}, Status: domain.WorkerStatusOnline}
	workerSigner, err := controlplane.NewPrivateKeySigner(sk.Hex())
	require.NoError(t, err)
	require.NoError(t, controlplane.NewWorkerStatePublisher(sink, workerSigner).Publish(ctx, &worker))
	payment := domain.PaymentRecord{ID: uuid.New(), DeploymentRunID: runID, WorkerPubkey: workerPubkey, MintURL: "https://mint.example", AmountSats: 16, Direction: domain.PaymentDirectionPayment, Status: domain.PaymentStatusSent, Metadata: map[string]any{"source": "mcp-parity"}, CreatedAt: created, UpdatedAt: created}
	require.NoError(t, paymentRepo.Create(ctx, &payment))
	require.NoError(t, nostrpool.NewPaymentCanonicalPublisher(projector, encryptor, zap.NewNop()).PublishPaymentRecord(ctx, &payment))
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), ConfidentialReader: encryptor})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_estimate_cost", map[string]any{"run_id": runID.String(), "estimated_duration_secs": float64(12)}},
		{"bahia_get_run_cost", map[string]any{"run_id": runID.String()}},
		{"bahia_get_payment_history", map[string]any{"worker_pubkey": workerPubkey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPWorkerStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	signer, err := controlplane.NewPrivateKeySigner(sk.Hex())
	require.NoError(t, err)
	publisher := controlplane.NewWorkerStatePublisher(sink, signer)
	repositoryServer, repo := newTestMCPWorkerServer()
	now := time.Now().UTC().Truncate(time.Second)
	worker := domain.Worker{PubKey: "worker-a", Name: "build-worker", Software: []domain.WorkerSoftware{{Name: "docker", Version: "27"}}, Pricing: []domain.WorkerPricing{{MintURL: "https://mint.example", PricePerSecond: 5, Unit: "sat"}}, LastAdvertisementAt: now, Status: domain.WorkerStatusOnline, CreatedAt: now, UpdatedAt: now}
	repo.workers[worker.PubKey] = &worker
	require.NoError(t, publisher.Publish(ctx, &worker))
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_workers", map[string]any{}},
		{"bahia_get_worker", map[string]any{"pubkey": worker.PubKey}},
		{"bahia_get_worker_pricing", map[string]any{"pubkey": worker.PubKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
	legacyFor(repositoryServer).WorkerReadModels = service.NewWorkerReadModelService(repo, nil, nil, service.NewWorkerPolicyService(repo, zap.NewNop()), service.NewMLPlacementService(repo, zap.NewNop()), zap.NewNop())
	for _, args := range []map[string]any{
		{"preview_id": "policy-preview", "workload_type": "worker_policy", "policy": map[string]any{"strategy": "cheapest"}},
		{"preview_id": "ml-preview", "workload_type": "inference", "runtime_kind": "external_api"},
	} {
		name := args["preview_id"].(string)
		t.Run(name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, "bahia_worker_preview_eligibility", args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, "bahia_worker_preview_eligibility", args)
			require.NoError(t, err)
			require.Equal(t, want.IsError, got.IsError, "want=%v got=%v", want.Content, got.Content)
			if want.IsError {
				require.Equal(t, want.Content[0].Text, got.Content[0].Text)
				return
			}
			wantMap, gotMap := decodeResultMap(t, want), decodeResultMap(t, got)
			delete(wantMap["eligibility_preview"].(map[string]any), "updated_at")
			delete(gotMap["eligibility_preview"].(map[string]any), "updated_at")
			require.Equal(t, wantMap, gotMap)
		})
	}
}

func TestMCPWorkerReadModelsStoreMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	signer, err := controlplane.NewPrivateKeySigner(sk.Hex())
	require.NoError(t, err)
	workerRepo := newTestPaymentWorkerRepo()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	worker := domain.Worker{PubKey: "worker-model", Status: domain.WorkerStatusOnline, UpdatedAt: now}
	workerRepo.workers[worker.PubKey] = &worker
	models := service.NewWorkerReadModelService(workerRepo, nil, nil, nil, nil, zap.NewNop())
	controlplane.NewWorkerReadModelPublisher(sink, signer, models, zap.NewNop()).PublishForWorker(ctx, worker.PubKey)
	repositoryServer := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{WorkerReadModels: models, Workers: workerRepo})
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_worker_get_assignments", map[string]any{"worker_pubkey": worker.PubKey}},
		{"bahia_worker_list_assignments", map[string]any{}},
		{"bahia_worker_get_drain_status", map[string]any{"worker_pubkey": worker.PubKey}},
		{"bahia_worker_list_drain_status", map[string]any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPPolicyStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	repositoryServer, repo := newTestMCPPolicyServer()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	policy := domain.DeploymentPolicy{ID: uuid.New(), Name: "trusted", Enforcement: domain.PolicyEnforcementBlock, Enabled: true, Rules: []domain.PolicyRule{{Type: domain.RuleRequireSignature}}, CreatedAt: created, UpdatedAt: created}
	repo.policies[policy.ID] = &policy
	tags, content := controlplane.PolicyRegistryRecord(&policy, false)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(), Tags: append(nostr.Tags{{"d", policy.ID.String()}, {"domain", "policy"}, {"schema", "bahia.cp-state.v1"}, {"legacy_kind", fmt.Sprint(nostrpool.KindPolicyRegistry)}, {"deleted", "false"}, {"t", kinds.CPStateTopicPolicyRegistry}}, tags...), Content: content}
	signer, err := controlplane.NewPrivateKeySigner(sk.Hex())
	require.NoError(t, err)
	require.NoError(t, controlplane.SignGoNostrEvent(ctx, signer, &ev))
	_, err = store.SaveEvent(ev)
	require.NoError(t, err)
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_list_policies", map[string]any{}},
		{"bahia_get_policy", map[string]any{"policy_id": policy.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPPackageStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceServer, _ := newTestMCPServiceServer()
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, serviceServer.registry, sink, nil, zap.NewNop())
	publisher := nostrpool.NewRelayFirstStatePublisher(projector, sink)
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	repo := domain.PackageRepository{ID: uuid.New(), Name: "libs", Format: domain.PackageRepositoryFormatNPM, CreatedAt: created, UpdatedAt: created}
	artifact := domain.PackageArtifact{ID: uuid.New(), RepositoryID: repo.ID, RepositoryName: repo.Name, Format: repo.Format, PackageName: "foo", Version: "1.0.0", Filename: "foo.tgz", CreatedAt: created, UpdatedAt: created}
	require.NoError(t, publisher.PublishPackageRepositoryRegistry(ctx, &repo, false))
	require.NoError(t, publisher.PublishPackageArtifactRegistry(ctx, &artifact, false))
	fixture := &mcpPackageFixtureRepo{repo: repo, artifact: artifact}
	repositoryServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{PackageProjection: fixture})
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_package_list", map[string]any{}},
		{"bahia_package_list", map[string]any{"repository_id": repo.ID.String()}},
		{"bahia_package_get", map[string]any{"repository_id": repo.ID.String()}},
		{"bahia_package_get", map[string]any{"repository_id": repo.ID.String(), "package_name": artifact.PackageName, "version": artifact.Version, "filename": artifact.Filename}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPMLStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	environmentServer, environments := newTestMCPEnvironmentServer()
	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	environment := domain.Environment{ID: uuid.New(), Name: "staging", CreatedAt: created, UpdatedAt: created}
	environments.environments[environment.ID] = &environment
	endpoint := domain.MLInferenceEndpoint{ID: uuid.New(), Name: "embeddings", EnvironmentID: environment.ID, CreatedAt: created, UpdatedAt: created}
	state := domain.MLInferenceState{EndpointID: endpoint.ID, EnvironmentID: environment.ID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: created}
	artifact := domain.MLArtifactRef{ID: uuid.New(), URI: "blossom://model", CreatedAt: created}
	fixture := &mcpMLFixtureRepo{endpoint: endpoint, state: state, artifact: artifact, edges: []domain.MLProvenanceEdge{}}
	mlRegistry := service.NewMLRegistryService(fixture, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	repositoryServer := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{MLRegistry: mlRegistry})
	projector := nostrpool.NewProjector(config.NostrConfig{PrivateKey: sk.Hex(), PublishEnabled: true}, environmentServer.registry, sink, nil, zap.NewNop(), nostrpool.WithMLProjectionSource(fixture))
	publisher := nostrpool.NewMLCanonicalPublisher(projector, zap.NewNop())
	require.NoError(t, publisher.PublishEndpointState(ctx, &state))
	require.NoError(t, publisher.PublishProvenanceGraph(ctx, &artifact))
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"bahia_ml_list_state", map[string]any{}},
		{"bahia_ml_get_state", map[string]any{"endpoint_id": endpoint.ID.String(), "environment_id": environment.ID.String()}},
		{"bahia_ml_get_provenance", map[string]any{"artifact_id": artifact.ID.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, tc.name, tc.args)
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
}

func TestMCPDeploymentStoreReadsMatchRepositoryFixture(t *testing.T) {
	ctx := authorizedMCPContext()
	sk := nostr.Generate()
	store := testStateStore(t)
	sink := mcpProjectionStore{store}
	serviceRepo, environmentRepo, intentRepo, runRepo, stateRepo := newTestServiceRepo(), newTestEnvironmentRepo(), newTestDeploymentIntentRepo(), newTestRunRepo(), newTestDeploymentStateRepo()
	registry := service.NewRegistryService(serviceRepo, environmentRepo, nil, nil, intentRepo, runRepo, nil, stateRepo, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	repositoryServer := newTestServer(registry, zap.NewNop())
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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
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
			want, err := repositoryServer.legacyCallTool(ctx, tc.name, tc.args)
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
	repositoryServer := newTestServerWithLegacyDeps(nil, zap.NewNop(), legacyMCPReadDeps{DNSEndpoints: dnsEndpointListerFunc(func(context.Context) ([]domain.DNSEndpoint, error) { return []domain.DNSEndpoint{endpoint}, nil })})
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex()})
	for _, name := range []string{"bahia_dns_list_endpoints", "bahia_dns_list_drift", "bahia_assistant_dns_list_endpoints", "bahia_assistant_dns_list_drift", "bahia_fips_list_mesh_nodes", "bahia_fips_mesh_status"} {
		t.Run(name, func(t *testing.T) {
			want, err := repositoryServer.legacyCallTool(ctx, name, map[string]any{})
			require.NoError(t, err)
			got, err := storeServer.CallTool(ctx, name, map[string]any{})
			require.NoError(t, err)
			require.False(t, got.IsError, "%v", got.Content)
			require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
		})
	}
	gotResources, err := storeServer.GetResources(ctx)
	require.NoError(t, err)
	require.Contains(t, gotResources, dnsEndpointResource(endpoint, endpoint.FQDN))
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
	storeServer := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{StateStore: store, ServicePubkey: sk.Public().Hex(), LogService: repositoryServer.logService})
	args := map[string]any{"run_id": runID.String()}
	want, err := repositoryServer.legacyCallTool(ctx, "bahia_get_run_logs", args)
	require.NoError(t, err)
	got, err := storeServer.CallTool(ctx, "bahia_get_run_logs", args)
	require.NoError(t, err)
	require.False(t, got.IsError, "%v", got.Content)
	require.JSONEq(t, want.Content[0].Text, got.Content[0].Text)
}
