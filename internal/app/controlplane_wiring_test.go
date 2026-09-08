package app

import (
	"context"

	canonicalnostr "fiatjaf.com/nostr"
	"reflect"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/mcp"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type appWiringNostrEventRepo struct{}

func (r *appWiringNostrEventRepo) Record(context.Context, *repository.NostrEventRecord) (bool, error) {
	return true, nil
}

func (r *appWiringNostrEventRepo) GetByID(context.Context, string) (*repository.NostrEventRecord, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) ListByKinds(context.Context, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) FindByTag(context.Context, string, string, []int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) ListByEntity(context.Context, string, uuid.UUID, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) LatestCreatedAtForKinds(context.Context, []int) (*time.Time, error) {
	return nil, nil
}

func (r *appWiringNostrEventRepo) LatestCreatedAtForKindsAndAuthors(context.Context, []int, []string) (*time.Time, error) {
	return nil, nil
}

func TestControlPlaneSubscriberAuthorScopesDoNotWidenDefaultScope(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.AuthorizedPubkeys = []string{"default-operator"}
	cfg.Adoption.AllowedPubkeys = []string{"adoption-operator"}
	cfg.DirectRuntime.AllowedPubkeys = []string{"runtime-operator"}

	scopes := controlPlaneSubscriberAuthorScopes(cfg, service.AssistantIdentity{Pubkey: "assistant-operator"})

	require.Equal(t, []string{"default-operator", "assistant-operator"}, scopes.Default)
	require.Equal(t, []string{"adoption-operator"}, scopes.Adoption)
	require.Equal(t, []string{"runtime-operator"}, scopes.DirectRuntime)
}

type appWiringBackupExecutor struct{}

func (appWiringBackupExecutor) ProcessBackupRun(context.Context, uuid.UUID) error { return nil }

type appWiringBackupResponder struct{}

func (appWiringBackupResponder) PublishBackupRunStatus(context.Context, *domain.BackupRun, string, string) error {
	return nil
}
func (appWiringBackupResponder) PublishBackupRunResult(context.Context, *domain.BackupRun, *domain.BackupVerificationRecord, string) error {
	return nil
}

func TestControlPlaneReactorBackupOptionsInjectFinalSliceDependencies(t *testing.T) {
	backupRegistry := &service.BackupRegistryService{}
	executor := appWiringBackupExecutor{}
	responder := appWiringBackupResponder{}
	reactor := controlplane.NewReactor(controlplane.Config{}, nil, nil, nil, zap.NewNop(),
		controlplane.WithBackupRegistry(backupRegistry),
		controlplane.WithBackupRunExecutor(executor),
		controlplane.WithBackupRunResponder(responder),
	)

	value := reflect.ValueOf(reactor).Elem()
	for _, fieldName := range []string{"backupRegistry", "backupExecutor", "backupResponder"} {
		field := value.FieldByName(fieldName)
		require.True(t, field.IsValid(), "reactor %s field must exist", fieldName)
		require.False(t, field.IsNil(), "reactor %s field must be injected", fieldName)
	}
}

type appWiringBackupMCPPublisher struct{}

func (appWiringBackupMCPPublisher) Publish(context.Context, nostr.Event) (int, error) { return 1, nil }

func TestConfigurePolicyToolMCPDepsWiresSignerFirstPublishers(t *testing.T) {
	signer, err := controlplane.NewPrivateKeySigner(nostr.Generate().Hex())
	require.NoError(t, err)
	deps := mcp.ServerDeps{}

	policyPublisher := configurePolicyToolMCPDeps(&deps, appWiringBackupMCPPublisher{}, signer, []string{"ws://relay.test"})

	require.NotNil(t, policyPublisher)
	require.Same(t, policyPublisher, deps.PolicyCommandPublisher)
	require.NotNil(t, deps.ToolApprovalCommandPublisher)
}

func TestConfigurePolicyToolMCPDepsFailsClosedWhenPublishingDepsMissing(t *testing.T) {
	signer, err := controlplane.NewPrivateKeySigner(nostr.Generate().Hex())
	require.NoError(t, err)

	for _, tt := range []struct {
		name      string
		publisher controlplane.NostrEventPublisher
		signer    canonicalnostr.Signer
		relays    []string
	}{
		{name: "nil publisher", signer: signer, relays: []string{"ws://relay.test"}},
		{name: "nil signer", publisher: appWiringBackupMCPPublisher{}, relays: []string{"ws://relay.test"}},
		{name: "no relays", publisher: appWiringBackupMCPPublisher{}, signer: signer},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := mcp.ServerDeps{}
			policyPublisher := configurePolicyToolMCPDeps(&deps, tt.publisher, tt.signer, tt.relays)
			require.Nil(t, policyPublisher)
			require.Nil(t, deps.PolicyCommandPublisher)
			require.Nil(t, deps.ToolApprovalCommandPublisher)
		})
	}
}

func TestConfigureBackupMCPDepsProvidesPublisherAndPostgresReadModels(t *testing.T) {
	var _ mcp.BackupReadModelRepository = (*repository.PgBackupControlPlaneRepository)(nil)

	signer, err := controlplane.NewPrivateKeySigner(nostr.Generate().Hex())
	require.NoError(t, err)
	readModels := repository.NewPgBackupControlPlaneRepository(nil)
	deps := mcp.ServerDeps{}

	configureBackupMCPDeps(&deps, readModels, appWiringBackupMCPPublisher{}, signer, []string{"ws://relay.test"})

	require.NotNil(t, deps.BackupCommandPublisher)
	require.Same(t, readModels, deps.BackupReadModels)
	server := mcp.NewServerWithOptions(nil, zap.NewNop(), deps)
	ctx := auth.ContextWithPrincipal(context.Background(), auth.SystemPrincipal("controlplane-wiring-test"))
	result, err := server.CallTool(ctx, "request_backup_run", map[string]interface{}{
		"recipe":          "recipe:postgres:v1",
		"idempotency_key": "backup-run:test",
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "configured backup MCP mutating tool should not return dependency errors: %#v", result)
}

func TestControlPlaneReactorAuditOptionIndependentOfPackageFeature(t *testing.T) {
	repo := &appWiringNostrEventRepo{}
	tests := []struct {
		name       string
		packageSvc *service.PackageRegistryService
	}{
		{name: "packages disabled"},
		{name: "packages enabled", packageSvc: &service.PackageRegistryService{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := appendControlPlaneAuditOption(nil, repo)
			opts = appendPackageControlPlaneOptions(opts, tt.packageSvc, nil)
			reactor := controlplane.NewReactor(controlplane.Config{}, nil, nil, nil, zap.NewNop(), opts...)

			field := reflect.ValueOf(reactor).Elem().FieldByName("nostrEvents")
			require.True(t, field.IsValid(), "reactor nostrEvents field must exist")
			require.False(t, field.IsNil(), "outbound audit repository must be injected regardless of package feature state")
		})
	}
}

func TestMCPServerDepsWiresAuthorizedPubkeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.AuthorizedPubkeys = []string{"default-operator"}

	deps := mcp.ServerDeps{
		AuthorizedPubkeys: cfg.Nostr.AuthorizedPubkeys,
	}
	server := mcp.NewServerWithOptions(nil, zap.NewNop(), deps)

	unauthorizedCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-unauthorized",
		PubKey:  "unauthorized-pubkey",
		Method:  auth.MethodNIP98,
	})
	unauthorizedResult, err := server.CallTool(unauthorizedCtx, "bahia_delete_service", map[string]interface{}{
		"service_id": uuid.New().String(),
	})
	require.NoError(t, err)
	require.True(t, unauthorizedResult.IsError, "unauthorized caller must be denied")
	require.Contains(t, unauthorizedResult.Content[0].Text, "access denied")

	authorizedCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-default-operator",
		PubKey:  "default-operator",
		Method:  auth.MethodNIP98,
	})
	authorizedResult, err := server.CallTool(authorizedCtx, "bahia_delete_service", map[string]interface{}{
		"service_id": uuid.New().String(),
	})
	require.NoError(t, err)
	require.True(t, authorizedResult.IsError, "tool handler error expected with nil registry")
	require.NotContains(t, authorizedResult.Content[0].Text, "access denied", "authorized caller must not be denied by auth gate")
}

func TestMCPServerDepsEmptyAllowlistDeniesExternalCallers(t *testing.T) {
	deps := mcp.ServerDeps{}
	server := mcp.NewServerWithOptions(nil, zap.NewNop(), deps)
	ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-someone",
		PubKey:  "some-pubkey",
		Method:  auth.MethodNIP98,
	})

	result, err := server.CallTool(ctx, "bahia_delete_service", map[string]interface{}{
		"service_id": uuid.New().String(),
	})
	require.NoError(t, err)
	require.True(t, result.IsError, "empty allowlist must deny external callers")
	require.Contains(t, result.Content[0].Text, "access denied")
}

func TestConfigureAuthorizationMCPDepsAuthorizesConfiguredPubkeys(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.AuthorizedPubkeys = []string{"default-operator"}

	deps := mcp.ServerDeps{}
	configureAuthorizationMCPDeps(&deps, cfg, nil)
	server := mcp.NewServerWithOptions(nil, zap.NewNop(), deps)

	unauthorizedCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-unauthorized",
		PubKey:  "unauthorized-pubkey",
		Method:  auth.MethodNIP98,
	})
	for _, tool := range []string{"bahia_delete_service", "bahia_register_build"} {
		result, err := server.CallTool(unauthorizedCtx, tool, map[string]interface{}{
			"service_id": uuid.New().String(),
		})
		require.NoError(t, err)
		require.True(t, result.IsError, "unauthorized caller must be denied for %s", tool)
		require.Contains(t, result.Content[0].Text, "access denied", "unauthorized caller must get access denied for %s", tool)
	}

	authorizedCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject: "npub-default-operator",
		PubKey:  "default-operator",
		Method:  auth.MethodNIP98,
	})
	for _, tool := range []string{"bahia_delete_service", "bahia_register_build"} {
		result, err := server.CallTool(authorizedCtx, tool, map[string]interface{}{
			"service_id": uuid.New().String(),
		})
		require.NoError(t, err)
		require.True(t, result.IsError, "tool handler error expected with nil registry for %s", tool)
		require.NotContains(t, result.Content[0].Text, "access denied", "authorized caller must not be denied by auth gate for %s", tool)
	}
}

func TestConfigureAuthorizationMCPDepsWiresTenantRBACFailClosed(t *testing.T) {
	const callerPubkey = "default-operator"
	cfg := config.Defaults()
	cfg.Nostr.AuthorizedPubkeys = []string{callerPubkey}

	t.Run("member lookup is propagated to the canonical server constructor", func(t *testing.T) {
		rbac := newTenantRBAC(appWiringMemberLookup{})
		require.NotNil(t, rbac)

		deps := mcp.ServerDeps{}
		configureAuthorizationMCPDeps(&deps, cfg, rbac)
		require.Same(t, rbac, deps.RBAC)
	})

	t.Run("missing member lookup remains unconfigured and denies", func(t *testing.T) {
		rbac := newTenantRBAC(nil)
		require.Nil(t, rbac)

		deps := mcp.ServerDeps{}
		configureAuthorizationMCPDeps(&deps, cfg, rbac)
		registryWithoutRepositories := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zap.NewNop())
		server := mcp.NewServerWithOptions(registryWithoutRepositories, zap.NewNop(), deps)
		ctx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
			Subject: "npub-default-operator",
			PubKey:  callerPubkey,
			Method:  auth.MethodNIP98,
		})

		for tool, arguments := range map[string]map[string]interface{}{
			"bahia_list_builds":         {"service_id": uuid.New().String()},
			"bahia_get_build":           {"build_id": uuid.New().String()},
			"bahia_register_build":      {"service_id": uuid.New().String()},
			"bahia_update_build_status": {"build_id": uuid.New().String(), "status": string(domain.BuildStatusRunning)},
		} {
			t.Run(tool, func(t *testing.T) {
				result, err := server.CallTool(ctx, tool, arguments)
				require.NoError(t, err)
				require.True(t, result.IsError)
				require.Contains(t, result.Content[0].Text, "service authorization is not configured")
			})
		}
	})
}

type appWiringMemberLookup struct{}

func (appWiringMemberLookup) GetMember(context.Context, uuid.UUID, string) (*domain.OrgMember, error) {
	return nil, repository.ErrNotFound
}

func (appWiringMemberLookup) ListByPubkey(context.Context, string) ([]domain.OrgMember, error) {
	return nil, nil
}
