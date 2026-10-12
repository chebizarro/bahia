package app

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
)

type heldRecords struct {
	secret *domain.SecretRef
	org    uuid.UUID
}

func (h *heldRecords) CurrentSecretRef(context.Context, uuid.UUID) (*domain.SecretRef, error) {
	if h.secret == nil {
		return nil, nil
	}
	v := *h.secret
	return &v, nil
}

func (h *heldRecords) ServiceOrgID(context.Context, uuid.UUID) (string, error) {
	return h.org.String(), nil
}

// heldStore delivers "value-v<version>" and counts deliveries.
type heldStore struct {
	resolves int
	down     bool
}

func (s *heldStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, _ secretstore.Scope) (*secretstore.Value, error) {
	s.resolves++
	if s.down {
		return nil, secretstore.ErrUnavailable
	}
	return secretstore.NewValue([]byte("value-v"+string(rune('0'+ref.Version))), time.Now().Add(time.Minute)), nil
}

func (s *heldStore) ResolveAll(ctx context.Context, reqs []secretstore.Request) ([]*secretstore.Value, error) {
	panic("held secrets resolve one at a time")
}

func (s *heldStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	panic("unused")
}

func (s *heldStore) Health(context.Context) error { return nil }

// A configuration-referenced daemon credential resolves on first use, not
// at startup; a failure is not cached; the value is held until the secret
// is re-pointed, which the next use notices without contacting the store.
func TestHeldSecretResolvesOnFirstUseAndFollowsRepointing(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	records := &heldRecords{org: uuid.New()}
	store := &heldStore{down: true}
	resolver := secretstore.NewResolver(store)
	held := newHeldSecret(secretstore.NewTextResolver(resolver, nil), id.String(), "test credential")

	_, err := held.get(ctx)
	require.ErrorIs(t, err, secretstore.ErrUnavailable, "records are attached after construction")
	resolver.UseRecords(records)
	_, err = held.get(ctx)
	require.ErrorIs(t, err, secretstore.ErrNoRecord)

	records.secret = &domain.SecretRef{ID: id, ServiceID: uuid.New(), Name: "TOKEN", Version: 1,
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_t", Version: 1}}
	_, err = held.get(ctx)
	require.ErrorIs(t, err, secretstore.ErrUnavailable)
	store.down = false
	for range 3 {
		text, err := held.get(ctx)
		require.NoError(t, err)
		require.Equal(t, "value-v1", text)
	}
	require.Equal(t, 2, store.resolves, "one failed and one held resolve")

	records.secret.CredentialRef = &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_t", Version: 2}
	text, err := held.get(ctx)
	require.NoError(t, err)
	require.Equal(t, "value-v2", text)
	require.Equal(t, 3, store.resolves)

	records.secret = &domain.SecretRef{ID: id, ServiceID: uuid.New(), Name: "TOKEN", EncryptionMethod: domain.EncryptionAES256}
	_, err = held.get(ctx)
	require.ErrorIs(t, err, domain.ErrLegacySecretValue)
}
