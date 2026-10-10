package secrets

import (
	"bytes"
	"context"
	"strings"
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

func TestWrappedDataKeyRejectsEnvelopeSubstitution(t *testing.T) {
	ctx := context.Background()
	keyer := localWrapKeyer{key: nostr.Generate()}
	service := keyer.key.Public()
	first, _, err := newWrappedDataKey(ctx, keyer, service)
	require.NoError(t, err)
	second, _, err := newWrappedDataKey(ctx, keyer, service)
	require.NoError(t, err)

	// SQL row substitution must not cause the first key to be accepted under
	// the second row's UUID, even though both use the same NIP-44 identity.
	second.WrappedCiphertext = first.WrappedCiphertext
	_, err = openWrappedDataKey(ctx, keyer, second, service)
	require.ErrorContains(t, err, "identity mismatch")

	for _, envelope := range []string{
		"bahia/unrelated-purpose/v2|" + first.ID.String() + "|" + service.Hex() + "|" + strings.Repeat("a", 64),
		dataKeyWrapPurpose + "|" + first.ID.String() + "|" + nostr.Generate().Public().Hex() + "|" + strings.Repeat("a", 64),
		strings.Repeat("a", 64), // Legacy bare-key plaintext is not a v2 envelope.
	} {
		ciphertext, encryptErr := keyer.Encrypt(ctx, envelope, service)
		require.NoError(t, encryptErr)
		candidate := first
		candidate.WrappedCiphertext = ciphertext
		_, err = openWrappedDataKey(ctx, keyer, candidate, service)
		require.Error(t, err)
	}
}
