package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	secretsAdapter "github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// A store-backed secret leaves a value-less marker in the PostgreSQL index.
// Runtime injection still reads PostgreSQL (it moves to the secret store in
// bahia-cd0wr.4.7), so it must fail naming the secret rather than deploy
// without it.
func TestRuntimeInjectionFailsOnStoreBackedSecret(t *testing.T) {
	ctx := context.Background()
	encryptor, err := secretsAdapter.NewEncryptor("5555555555555555555555555555555555555555555555555555555555555555")
	require.NoError(t, err)
	repo := newMockSecretRepo()
	serviceID := uuid.New()
	sealed, err := encryptor.Encrypt("legacy-value", domain.EncryptionAES256)
	require.NoError(t, err)
	legacy := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, Name: "LEGACY", EncryptedValue: sealed, EncryptionMethod: domain.EncryptionAES256, CreatedBy: "t"}
	marker := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, Name: "DB_PASSWORD", EncryptionMethod: domain.EncryptionStoreRef, Version: 2, CreatedBy: "t"}
	require.NoError(t, repo.Create(ctx, legacy))
	require.NoError(t, repo.Create(ctx, marker))
	lifecycle := &RuntimeLifecycleService{secrets: repo, secretEncryptor: encryptor}

	for _, secrets := range [][]domain.ServiceSecret{{*marker}, {*legacy, *marker}} {
		opts := &runtime.DeployOptions{}
		_, err := lifecycle.mergeEffectiveSecrets(ctx, secrets, opts, nil, false)
		require.ErrorIs(t, err, domain.ErrSecretInStore)
		require.ErrorContains(t, err, "DB_PASSWORD")
		require.ErrorContains(t, err, "bahia-cd0wr.4.7")
		require.NotContains(t, opts.Environment, "DB_PASSWORD")
	}
}

func TestVMBootstrapRefusesStoreBackedSecret(t *testing.T) {
	_, _, _, v, principal := vmFixture(t)
	svc, env := uuid.New(), uuid.New()
	v.ServiceID, v.EnvironmentID = &svc, &env
	secret := domain.ServiceSecret{ID: uuid.New(), ServiceID: svc, EnvironmentID: &env, Name: "RDP_PASSWORD", EncryptionMethod: domain.EncryptionStoreRef}
	v.Bootstrap = []domain.VMBootstrapBinding{{TargetKey: "rdp", Ref: domain.SecretRef{ID: secret.ID}}}
	secrets := &vmSecretRepo{secret: secret}
	resolver := &vmSecretResolver{repo: secrets}
	bootstrap, err := NewVMBootstrapService(secrets, resolver, &vmPermissionFake{})
	require.NoError(t, err)
	err = bootstrap.Authorize(context.Background(), principal, v)
	require.True(t, errors.Is(err, domain.ErrSecretInStore), "err = %v", err)
	require.Zero(t, resolver.calls)
}
