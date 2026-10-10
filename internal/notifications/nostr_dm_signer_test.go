package notifications

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type refusingDMSigner struct{}

func (refusingDMSigner) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return nostr.PubKey{}, nil
}
func (refusingDMSigner) SignEvent(context.Context, *nostr.Event) error {
	return errors.New("signer refused")
}

func TestNostrDMSenderInjectedSignerControlsEventAuthor(t *testing.T) {
	var signerKey [32]byte
	signerKey[31] = 2
	signer := keyer.NewPlainKeySigner(signerKey)
	signerPubkey, err := signer.GetPublicKey(context.Background())
	require.NoError(t, err)
	localKey := strings.Repeat("1", 64)
	recipient, err := nostr.SecretKeyFromHex(localKey)
	require.NoError(t, err)
	channel := &domain.NotificationChannel{Name: "dm", Config: map[string]any{"pubkey": recipient.Public().Hex()}}
	var published nostr.Event
	sender := NewNostrDMSender(nil, localKey, zap.NewNop(), signer)
	sender.publish = func(_ context.Context, event nostr.Event) (int, error) { published = event; return 1, nil }
	require.NoError(t, sender.Send(context.Background(), channel, "test", nil))
	require.Equal(t, signerPubkey, published.PubKey)
	require.True(t, published.CheckID())
	require.True(t, published.VerifySignature())

	sender.signer = refusingDMSigner{}
	sender.publish = func(context.Context, nostr.Event) (int, error) {
		t.Fatal("published after signing refusal")
		return 0, nil
	}
	require.ErrorContains(t, sender.Send(context.Background(), channel, "test", nil), "signer refused")
}
