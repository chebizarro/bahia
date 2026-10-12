package app

import (
	"context"
	"sync"
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

// heldStore delivers "value-v<version>" under a lease ending at
// leaseUntil and counts deliveries; block, when set, holds every delivery
// until it is closed.
type heldStore struct {
	mu         sync.Mutex
	resolves   int
	down       bool
	revoked    bool
	leaseUntil time.Time
	block      chan struct{}
}

func (s *heldStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, _ secretstore.Scope) (*secretstore.Value, error) {
	s.mu.Lock()
	block := s.block
	s.resolves++
	down, revoked, lease := s.down, s.revoked, s.leaseUntil
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	switch {
	case down:
		return nil, secretstore.ErrUnavailable
	case revoked:
		return nil, secretstore.ErrRevoked
	}
	if lease.IsZero() {
		lease = time.Now().Add(time.Minute)
	}
	return secretstore.NewValue([]byte("value-v"+string(rune('0'+ref.Version))), lease), nil
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
	held := newHeldSecret(secretstore.NewTextResolver(resolver, nil, nil), id.String(), "test credential")

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

	records.secret = &domain.SecretRef{ID: id, ServiceID: uuid.New(), Name: "TOKEN", EncryptionMethod: domain.EncryptionMethod("aes256gcm")}
	_, err = held.get(ctx)
	require.ErrorIs(t, err, domain.ErrLegacySecretValue)
}

// A held credential lasts only until the store's lease ends: the next use
// resolves again, so a revoked credential stops working and its text is
// dropped; a deleted secret drops it at once.
func TestHeldSecretFollowsLeaseRevocationAndDeletion(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	records := &heldRecords{org: uuid.New(), secret: &domain.SecretRef{ID: id, ServiceID: uuid.New(), Name: "TOKEN", Version: 1,
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_t", Version: 1}}}
	store := &heldStore{leaseUntil: now.Add(time.Minute)}
	resolver := secretstore.NewResolver(store)
	resolver.UseRecords(records)
	held := newHeldSecret(secretstore.NewTextResolver(resolver, nil, nil), id.String(), "test credential")
	held.now = func() time.Time { return now }

	text, err := held.get(ctx)
	require.NoError(t, err)
	require.Equal(t, "value-v1", text)
	store.revoked = true
	now = now.Add(30 * time.Second)
	text, err = held.get(ctx)
	require.NoError(t, err, "within the lease the held text serves without a round trip")
	require.Equal(t, "value-v1", text)
	require.Equal(t, 1, store.resolves)

	now = now.Add(time.Minute)
	_, err = held.get(ctx)
	require.ErrorIs(t, err, secretstore.ErrRevoked, "after the lease the revocation is noticed")
	require.Empty(t, held.text, "the held text is dropped on failure")
	store.revoked = false
	store.leaseUntil = now.Add(time.Minute)
	text, err = held.get(ctx)
	require.NoError(t, err)
	require.Equal(t, "value-v1", text)

	records.secret = nil
	_, err = held.get(ctx)
	require.ErrorIs(t, err, secretstore.ErrNoRecord)
	require.Empty(t, held.text, "a deleted secret drops the held text")
}

// Concurrent uses share one resolve; a caller whose context ends does not
// wait for it.
func TestHeldSecretSharesOneResolve(t *testing.T) {
	id := uuid.New()
	records := &heldRecords{org: uuid.New(), secret: &domain.SecretRef{ID: id, ServiceID: uuid.New(), Name: "TOKEN", Version: 1,
		CredentialRef: &domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: "cred_t", Version: 1}}}
	store := &heldStore{block: make(chan struct{})}
	resolver := secretstore.NewResolver(store)
	resolver.UseRecords(records)
	held := newHeldSecret(secretstore.NewTextResolver(resolver, nil, nil), id.String(), "test credential")

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := held.get(context.Background())
			results <- err
		}()
	}
	require.Eventually(t, func() bool {
		held.mu.Lock()
		defer held.mu.Unlock()
		return held.flight != nil
	}, 5*time.Second, time.Millisecond)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := held.get(canceled)
	require.ErrorIs(t, err, context.Canceled, "a caller whose context ended is not held by the in-flight resolve")
	close(store.block)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, 1, store.resolves, "concurrent uses share one resolve")
}
