package secrets

import (
	"bytes"
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type localWrapKeyer struct{ key nostr.SecretKey }

func (k localWrapKeyer) GetPublicKey(context.Context) (nostr.PubKey, error) {
	return k.key.Public(), nil
}
func (k localWrapKeyer) Encrypt(_ context.Context, plain string, peer nostr.PubKey) (string, error) {
	conversation, err := nip44.GenerateConversationKey(peer, k.key)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plain, conversation)
}
func (k localWrapKeyer) Decrypt(_ context.Context, cipher string, peer nostr.PubKey) (string, error) {
	conversation, err := nip44.GenerateConversationKey(peer, k.key)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(cipher, conversation)
}
func (k localWrapKeyer) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	panic("unused")
}
func (k localWrapKeyer) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	panic("unused")
}
func (k localWrapKeyer) SignEvent(context.Context, *nostr.Event) error { panic("unused") }

func TestVersionedDataKeyRoundTripAndIdentityBinding(t *testing.T) {
	keyer := localWrapKeyer{key: nostr.Generate()}
	wrapped, key, err := newWrappedDataKey(context.Background(), keyer, keyer.key.Public())
	require.NoError(t, err)
	reopened, err := openWrappedDataKey(context.Background(), keyer, wrapped, keyer.key.Public())
	require.NoError(t, err)
	require.Equal(t, key.ID(), reopened.ID())
	secretID := uuid.New()
	first, err := key.Seal(secretID, 3, []byte("secret"))
	require.NoError(t, err)
	second, err := key.Seal(secretID, 3, []byte("secret"))
	require.NoError(t, err)
	require.False(t, bytes.Equal(first, second))
	plain, err := reopened.Open(secretID, 3, first)
	require.NoError(t, err)
	require.Equal(t, []byte("secret"), plain)
	_, err = reopened.Open(secretID, 4, first)
	require.Error(t, err)
	_, err = reopened.Open(uuid.New(), 3, first)
	require.Error(t, err)
	first[1] ^= 1
	_, err = reopened.Open(secretID, 3, first)
	require.Error(t, err)
	_, err = openWrappedDataKey(context.Background(), keyer, wrapped, nostr.Generate().Public())
	require.Error(t, err)
}
