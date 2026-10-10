package notifications

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// remoteDMKeyer stands in for a NIP-46/NIP-55L service signer: it holds no
// in-process key material and counts every operation.
type remoteDMKeyer struct {
	keyer.KeySigner
	signs, encryptions atomic.Int64
	refuse             bool
}

func (k *remoteDMKeyer) SignEvent(ctx context.Context, ev *nostr.Event) error {
	k.signs.Add(1)
	if k.refuse {
		return errors.New("signer refused")
	}
	return k.KeySigner.SignEvent(ctx, ev)
}

func (k *remoteDMKeyer) Encrypt(ctx context.Context, plaintext string, to nostr.PubKey) (string, error) {
	k.encryptions.Add(1)
	return k.KeySigner.Encrypt(ctx, plaintext, to)
}

func TestNostrDMSenderEncryptsAndSignsThroughInjectedKeyer(t *testing.T) {
	ctx := context.Background()
	remote := &remoteDMKeyer{KeySigner: keyer.NewPlainKeySigner(nostr.Generate())}
	servicePubkey, err := remote.GetPublicKey(ctx)
	require.NoError(t, err)
	recipient := keyer.NewPlainKeySigner(nostr.Generate())
	recipientPubkey, err := recipient.GetPublicKey(ctx)
	require.NoError(t, err)
	channel := &domain.NotificationChannel{Name: "dm", Config: map[string]any{"pubkey": recipientPubkey.Hex()}}

	var published nostr.Event
	sender := NewNostrDMSender(nil, remote, zap.NewNop())
	sender.publish = func(_ context.Context, event nostr.Event) (int, error) { published = event; return 1, nil }
	require.NoError(t, sender.Send(ctx, channel, "test", nil))
	require.EqualValues(t, 1, remote.encryptions.Load())
	require.EqualValues(t, 1, remote.signs.Load())
	require.Equal(t, servicePubkey, published.PubKey)
	require.True(t, published.VerifySignature())
	plaintext, err := recipient.Decrypt(ctx, published.Content, servicePubkey)
	require.NoError(t, err)
	require.Contains(t, plaintext, "Bahia Notification: test")

	remote.refuse = true
	sender.publish = func(context.Context, nostr.Event) (int, error) {
		t.Fatal("published after signing refusal")
		return 0, nil
	}
	require.ErrorContains(t, sender.Send(ctx, channel, "test", nil), "signer refused")
}
