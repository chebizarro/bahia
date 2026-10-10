package nostr

import (
	"context"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
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

func TestServiceSignerInjectionOverridesLocalSigning(t *testing.T) {
	ctx := context.Background()
	var remoteKey [32]byte
	remoteKey[31] = 2
	signer := keyer.NewPlainKeySigner(remoteKey)
	pubkey, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	localKey := "1111111111111111111111111111111111111111111111111111111111111111"

	publisher := &Publisher{privateKey: localKey, signer: signer}
	projection := &Projector{privateKey: localKey, signer: signer, servicePubkey: pubkey.Hex(), logger: zap.NewNop()}
	pool := NewRelayPool(nil, zap.NewNop(), WithPrivateKey(localKey), WithAuthSigner(signer))
	for name, sign := range map[string]func(context.Context, *gonostr.Event) error{
		"publisher":  publisher.signEvent,
		"projection": projection.signEvent,
		"relay AUTH": pool.signAuthEvent,
	} {
		t.Run(name, func(t *testing.T) {
			ev := &gonostr.Event{Kind: 1, CreatedAt: gonostr.Timestamp(1), Content: name}
			require.NoError(t, sign(ctx, ev))
			require.Equal(t, pubkey, ev.PubKey)
			require.True(t, ev.CheckID())
			require.True(t, ev.VerifySignature())
		})
	}
	author, err := projection.authorPubkey()
	require.NoError(t, err)
	require.Equal(t, pubkey.Hex(), author)
}

func TestInjectedServiceSignerRefusalDoesNotFallBackToLocalKey(t *testing.T) {
	ctx := context.Background()
	localKey := "1111111111111111111111111111111111111111111111111111111111111111"
	signer := refusingServiceSigner{}
	publisher := &Publisher{privateKey: localKey, signer: signer}
	projection := &Projector{privateKey: localKey, signer: signer}
	pool := NewRelayPool(nil, zap.NewNop(), WithPrivateKey(localKey), WithAuthSigner(signer))
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
