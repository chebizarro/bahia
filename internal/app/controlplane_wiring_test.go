package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
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
