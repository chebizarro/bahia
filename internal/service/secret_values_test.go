package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
)

// fakeSecretValues is the secret store as consumers see it: canonical
// records with credential references, resolved in batches.
type fakeSecretValues struct {
	refs    map[uuid.UUID]domain.SecretRef
	values  map[uuid.UUID]string
	batches [][]uuid.UUID
	handed  []*secretstore.Value
	err     error
}

func newFakeSecretValues() *fakeSecretValues {
	return &fakeSecretValues{refs: map[uuid.UUID]domain.SecretRef{}, values: map[uuid.UUID]string{}}
}

// storeBacked indexes a store-backed secret (a value-less marker row) and
// gives it a canonical record and a value in the store.
func (f *fakeSecretValues) storeBacked(t *testing.T, repo interface {
	Create(context.Context, *domain.ServiceSecret) error
}, serviceID uuid.UUID, envID *uuid.UUID, name, value string) *domain.ServiceSecret {
	t.Helper()
	row := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, Name: name, EncryptionMethod: domain.EncryptionStoreRef, Version: 1, CreatedBy: "test"}
	require.NoError(t, repo.Create(context.Background(), row))
	f.refs[row.ID] = domain.SecretRef{ID: row.ID, ServiceID: serviceID, EnvironmentID: envID, Name: name, Version: 1,
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_" + name, Version: 4}}
	f.values[row.ID] = value
	return row
}

func (f *fakeSecretValues) ResolveAll(_ context.Context, ids []uuid.UUID) ([]secretstore.Resolved, error) {
	f.batches = append(f.batches, append([]uuid.UUID(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	out := make([]secretstore.Resolved, 0, len(ids))
	for _, id := range ids {
		ref, ok := f.refs[id]
		if !ok {
			secretstore.ZeroizeResolved(out)
			return nil, secretstore.ErrNoRecord
		}
		v := secretstore.NewValue([]byte(f.values[id]), time.Now().Add(time.Minute))
		f.handed = append(f.handed, v)
		out = append(out, secretstore.Resolved{Secret: ref, Value: v})
	}
	return out, nil
}

func (f *fakeSecretValues) requireAllZeroized(t *testing.T) {
	t.Helper()
	require.NotEmpty(t, f.handed)
	for _, v := range f.handed {
		require.Nil(t, v.Bytes(), "a resolved value was not zeroized")
	}
}

func TestRuntimeInjectionResolvesStoreBackedSecretsAndRefusesLegacy(t *testing.T) {
	ctx := context.Background()
	repo := newMockSecretRepo()
	values := newFakeSecretValues()
	serviceID := uuid.New()
	db := values.storeBacked(t, repo, serviceID, nil, "DB_PASSWORD", "s3cret")
	api := values.storeBacked(t, repo, serviceID, nil, "API_KEY", "k3y")
	lifecycle := &RuntimeLifecycleService{secrets: repo, secretValues: values}

	opts := &runtime.DeployOptions{}
	accesses, err := lifecycle.mergeEffectiveSecrets(ctx, []domain.ServiceSecret{*db, *api}, opts, nil, false)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"DB_PASSWORD": "s3cret", "API_KEY": "k3y"}, opts.Environment)
	require.Len(t, values.batches, 1, "one deploy resolves its secrets in one batch")
	require.Len(t, accesses, 2)
	require.Equal(t, 4, accesses[0].Version, "the audit names the pinned credential version")
	values.requireAllZeroized(t)

	// A row that still holds a value sealed with the retired service key is
	// refused, naming it, before the store is asked for anything.
	legacy := &domain.ServiceSecret{ID: uuid.New(), ServiceID: serviceID, Name: "LEGACY", EncryptedValue: []byte("sealed"), EncryptionMethod: domain.EncryptionAES256}
	opts = &runtime.DeployOptions{}
	_, err = lifecycle.mergeEffectiveSecrets(ctx, []domain.ServiceSecret{*db, *legacy}, opts, nil, false)
	require.ErrorIs(t, err, domain.ErrLegacySecretValue)
	require.ErrorContains(t, err, "LEGACY")
	require.Empty(t, opts.Environment)
	require.Len(t, values.batches, 1)

	// The store failing fails the deploy; nothing is injected.
	values.err = secretstore.ErrUnavailable
	_, err = lifecycle.mergeEffectiveSecrets(ctx, []domain.ServiceSecret{*db}, opts, nil, false)
	require.ErrorIs(t, err, secretstore.ErrUnavailable)
	require.Empty(t, opts.Environment)

	// Without a store, secret-dependent deploys fail closed.
	lifecycle.secretValues = nil
	_, err = lifecycle.mergeEffectiveSecrets(ctx, []domain.ServiceSecret{*db}, opts, nil, false)
	require.True(t, errors.Is(err, secretstore.ErrNotConfigured), "err = %v", err)
}

// The PostgreSQL index row and the canonical record must agree: a record
// that names another service or name is refused, not served.
func TestRuntimeInjectionRefusesIndexDisagreeingWithCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	repo := newMockSecretRepo()
	values := newFakeSecretValues()
	row := values.storeBacked(t, repo, uuid.New(), nil, "TOKEN", "v")
	ref := values.refs[row.ID]
	ref.Name = "OTHER"
	values.refs[row.ID] = ref
	lifecycle := &RuntimeLifecycleService{secrets: repo, secretValues: values}
	opts := &runtime.DeployOptions{}
	_, err := lifecycle.mergeEffectiveSecrets(ctx, []domain.ServiceSecret{*row}, opts, nil, false)
	require.ErrorContains(t, err, "disagrees with the canonical record")
	require.Empty(t, opts.Environment)
	values.requireAllZeroized(t)
}
