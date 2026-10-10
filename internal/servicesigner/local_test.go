package servicesigner

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

var (
	testService = nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	testClient  = nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
)

func TestOpenWithoutIdentityIsNotConfigured(t *testing.T) {
	signer, err := Open(t.Context(), config.NostrConfig{}, Options{})
	require.ErrorIs(t, err, ErrNotConfigured)
	require.Nil(t, signer)
}

func TestOpenLocalPreservesConfiguredIdentity(t *testing.T) {
	for name, cfg := range map[string]config.NostrConfig{
		"implicit":    {PrivateKey: testService.Hex()},
		"explicit":    {PrivateKey: testService.Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerLocal}},
		"pinned":      {PrivateKey: testService.Hex(), PublicKey: testService.Public().Hex()},
		"padded hex ": {PrivateKey: " " + testService.Hex() + "\n"},
	} {
		t.Run(name, func(t *testing.T) {
			signer, err := Open(t.Context(), cfg, Options{})
			require.NoError(t, err)
			pubkey, err := signer.GetPublicKey(t.Context())
			require.NoError(t, err)
			require.Equal(t, testService.Public(), pubkey)
			ev := nostr.Event{Kind: 1, CreatedAt: 1, Content: "x"}
			require.NoError(t, signer.SignEvent(t.Context(), &ev))
			require.True(t, ev.VerifySignature())
		})
	}
}

func TestOpenLocalRejectsMismatchedPinAndMalformedKey(t *testing.T) {
	_, err := Open(t.Context(), config.NostrConfig{PrivateKey: testService.Hex(), PublicKey: testClient.Public().Hex()}, Options{})
	require.ErrorContains(t, err, "differs from configured service pubkey")
	_, err = Open(t.Context(), config.NostrConfig{PrivateKey: "nsec1notahexkey"}, Options{})
	require.ErrorContains(t, err, "64-character hex")
	require.NotContains(t, err.Error(), "nsec1notahexkey")
}

func TestLocalBinaryCipherRoundTripsExactBytes(t *testing.T) {
	signer, err := Open(t.Context(), config.NostrConfig{PrivateKey: testService.Hex()}, Options{})
	require.NoError(t, err)
	binary, ok := signer.(BinaryCipher)
	require.True(t, ok, "local signer implements BinaryCipher")
	plaintext := []byte{0, 0xff, 0x80, 0x7f, 0}
	ciphertext, err := binary.EncryptBytes(t.Context(), plaintext, testClient.Public())
	require.NoError(t, err)
	require.True(t, validNIP44Payload(ciphertext))
	opened, err := binary.DecryptBytes(t.Context(), ciphertext, testClient.Public())
	require.NoError(t, err)
	require.True(t, bytes.Equal(plaintext, opened))
	_, err = binary.EncryptBytes(t.Context(), nil, testClient.Public())
	require.Error(t, err)
}

type stubKeyer struct {
	nostr.Keyer
	pubkey nostr.PubKey
	closed bool
}

func (s *stubKeyer) GetPublicKey(context.Context) (nostr.PubKey, error) { return s.pubkey, nil }
func (s *stubKeyer) Close() error                                       { s.closed = true; return nil }

func TestOpenNIP55LUsesInjectedConstructorAndVerifiesIdentity(t *testing.T) {
	cfg := config.NostrConfig{PublicKey: testService.Public().Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerNIP55L, NIP55L: config.NostrSignerNIP55LConfig{AppID: "bahia", BusAddress: "unix:path=/tmp/bus"}}}
	_, err := Open(t.Context(), cfg, Options{})
	require.ErrorContains(t, err, "not available in this build")

	var got NIP55LConfig
	stub := &stubKeyer{pubkey: testService.Public()}
	signer, err := Open(t.Context(), cfg, Options{NewNIP55L: func(_ context.Context, c NIP55LConfig) (nostr.Keyer, error) { got = c; return stub, nil }})
	require.NoError(t, err)
	require.Same(t, stub, signer)
	require.Equal(t, NIP55LConfig{ServicePubkey: testService.Public(), AppID: "bahia", BusAddress: "unix:path=/tmp/bus", CallTimeout: defaultTimeout}, got)

	wrong := &stubKeyer{pubkey: testClient.Public()}
	_, err = Open(t.Context(), cfg, Options{NewNIP55L: func(context.Context, NIP55LConfig) (nostr.Keyer, error) { return wrong, nil }})
	require.ErrorContains(t, err, "differs from configured service pubkey")
	require.True(t, wrong.closed, "a rejected signer is closed")

	failed := errors.New("no signer on bus")
	_, err = Open(t.Context(), cfg, Options{NewNIP55L: func(context.Context, NIP55LConfig) (nostr.Keyer, error) { return nil, failed }})
	require.ErrorIs(t, err, failed)
}

func TestOpenRemoteRequiresPinnedPubkey(t *testing.T) {
	for _, method := range []string{config.NostrSignerNIP46, config.NostrSignerNIP55L} {
		_, err := Open(t.Context(), config.NostrConfig{Signer: config.NostrSignerConfig{Method: method}}, Options{NewNIP55L: func(context.Context, NIP55LConfig) (nostr.Keyer, error) {
			t.Fatal("constructor reached without a pinned pubkey")
			return nil, nil
		}})
		require.ErrorContains(t, err, "requires nostr.public_key")
	}
}
