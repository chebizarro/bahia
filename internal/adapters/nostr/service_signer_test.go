package nostr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type refusingServiceSigner struct{ pubkey gonostr.PubKey }

func (s refusingServiceSigner) GetPublicKey(context.Context) (gonostr.PubKey, error) {
	return s.pubkey, nil
}
func (s refusingServiceSigner) SignEvent(context.Context, *gonostr.Event) error {
	return errors.New("signer refused")
}

// remoteServiceKeyer stands in for a NIP-46 or NIP-55L service signer: it
// signs and NIP-44s, but holds no in-process key material.
type remoteServiceKeyer struct {
	inner keyer.KeySigner
	signs atomic.Int64
}

func newRemoteServiceKeyer() *remoteServiceKeyer {
	return &remoteServiceKeyer{inner: keyer.NewPlainKeySigner(gonostr.Generate())}
}

func (k *remoteServiceKeyer) GetPublicKey(ctx context.Context) (gonostr.PubKey, error) {
	return k.inner.GetPublicKey(ctx)
}
func (k *remoteServiceKeyer) SignEvent(ctx context.Context, ev *gonostr.Event) error {
	k.signs.Add(1)
	return k.inner.SignEvent(ctx, ev)
}
func (k *remoteServiceKeyer) Encrypt(ctx context.Context, plaintext string, to gonostr.PubKey) (string, error) {
	return k.inner.Encrypt(ctx, plaintext, to)
}
func (k *remoteServiceKeyer) Decrypt(ctx context.Context, ciphertext string, from gonostr.PubKey) (string, error) {
	return k.inner.Decrypt(ctx, ciphertext, from)
}

// TestRemoteServiceKeyerSignsEveryPath proves that with a non-local Keyer the
// publisher, projector and relay AUTH all sign through it, and that the
// confidential-state digest depends only on the confidential-dedupe system
// key, never on the signer's key material.
func TestRemoteServiceKeyerSignsEveryPath(t *testing.T) {
	ctx := context.Background()
	remote := newRemoteServiceKeyer()
	pubkey, err := remote.GetPublicKey(ctx)
	require.NoError(t, err)

	cfg := config.NostrConfig{PublishEnabled: true}
	publisher := &Publisher{signer: remote}
	projection := NewProjector(cfg, newFakeProjectionSource(), &fakeProjectionPublisher{}, nil, zap.NewNop(), WithProjectorSigner(remote, pubkey.Hex()))
	require.True(t, projection.Enabled())
	pool := NewRelayPool(nil, zap.NewNop(), WithAuthSigner(remote))
	paths := map[string]func(context.Context, *gonostr.Event) error{
		"publisher":  publisher.signEvent,
		"projection": projection.signEvent,
		"relay AUTH": pool.signAuthEvent,
	}
	for name, sign := range paths {
		t.Run(name, func(t *testing.T) {
			ev := &gonostr.Event{Kind: 1, CreatedAt: gonostr.Timestamp(1), Content: name}
			require.NoError(t, sign(ctx, ev))
			require.Equal(t, pubkey, ev.PubKey)
			require.True(t, ev.VerifySignature())
		})
	}
	require.EqualValues(t, len(paths), remote.signs.Load())

	err = projection.publishCanonicalFirst(ctx, KindSecretRegistry, "d", false, nil, "plain", "cipher", "secret.projection", nil)
	require.ErrorIs(t, err, secretstore.ErrNotConfigured)
	require.EqualValues(t, len(paths), remote.signs.Load(), "a record without its digest key must not sign")

	// With the confidential-dedupe system key the digest needs nothing from
	// the signer: a remote signer publishes once and dedupes the repeat.
	keyed := NewProjector(cfg, newFakeProjectionSource(), &fakeProjectionPublisher{}, nil, zap.NewNop(),
		WithProjectorSigner(remote, pubkey.Hex()), WithConfidentialStateHashKeys(testStateHashKeyring(testStateHashKeyFill)))
	for range 2 {
		require.NoError(t, keyed.publishCanonicalFirst(ctx, KindSecretRegistry, "d", false, nil, `{"a":1}`, "cipher-"+gonostr.Generate().Public().Hex(), "secret.projection", nil))
	}
	require.EqualValues(t, len(paths)+1, remote.signs.Load(), "unchanged plaintext under randomized ciphertext signs once")
}

func TestServiceSignerRefusalLeavesEventUnsigned(t *testing.T) {
	ctx := context.Background()
	signer := refusingServiceSigner{}
	publisher := &Publisher{signer: signer}
	projection := &Projector{signer: signer}
	pool := NewRelayPool(nil, zap.NewNop(), WithAuthSigner(signer))
	for name, sign := range map[string]func(context.Context, *gonostr.Event) error{
		"publisher":  publisher.signEvent,
		"projection": projection.signEvent,
		"relay AUTH": pool.signAuthEvent,
	} {
		t.Run(name, func(t *testing.T) {
			ev := &gonostr.Event{Kind: 1, CreatedAt: gonostr.Timestamp(1)}
			require.ErrorContains(t, sign(ctx, ev), "signer refused")
			require.Equal(t, gonostr.PubKey{}, ev.PubKey)
		})
	}
}
