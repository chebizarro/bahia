package nostr

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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
// publisher, projector and relay AUTH all sign through it, and the
// confidential-state digest, which needs the raw key, fails closed.
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
	require.ErrorIs(t, err, nostrutil.ErrServiceKeyMaterialRequired)
	require.EqualValues(t, len(paths), remote.signs.Load(), "a blocked digest must not sign")
}

// TestLocalServiceKeyerKeepsConfidentialDigest pins the v1 state_hash key to
// the deployed derivation over the configured key text.
func TestLocalServiceKeyerKeepsConfidentialDigest(t *testing.T) {
	key := "1111111111111111111111111111111111111111111111111111111111111111"
	got, err := confidentialStateHashKeyFor(mustLocalKeyer(key))
	require.NoError(t, err)
	require.Equal(t, deriveConfidentialStateHashKey(key), got)
	require.Equal(t, "state_hash", confidentialStateHash(got, `{"a":1}`)[0])
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
