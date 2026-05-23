package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
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

func TestConfigureBackupMCPDepsProvidesPublisherAndPostgresReadModels(t *testing.T) {
	var _ mcp.BackupReadModelRepository = (*repository.PgBackupControlPlaneRepository)(nil)

	signer, err := controlplane.NewPrivateKeySigner(nostr.GeneratePrivateKey())
	require.NoError(t, err)
	readModels := repository.NewPgBackupControlPlaneRepository(nil)
	deps := mcp.ServerDeps{}

	configureBackupMCPDeps(&deps, readModels, appWiringBackupMCPPublisher{}, signer, []string{"ws://relay.test"})

	require.NotNil(t, deps.BackupCommandPublisher)
	require.Same(t, readModels, deps.BackupReadModels)
	server := mcp.NewServerWithOptions(nil, zap.NewNop(), deps)
	result, err := server.CallTool(context.Background(), "request_backup_run", map[string]interface{}{
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
