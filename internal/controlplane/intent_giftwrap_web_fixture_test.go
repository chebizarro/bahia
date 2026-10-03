package controlplane

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip44"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The committed fixture is emitted by web/src/lib/nostr/intent-giftwrap.js.
// This test exercises the daemon's production unwrap boundary, not a parallel decoder.
func TestWebGiftWrapFixtureUnwrapsAsIntent(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "web", "tests", "fixtures", "intent-giftwrap-web.json")
	data, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	var fixture struct {
		ServiceSecretKeyHex string      `json:"service_secret_key_hex"`
		ServicePubkey       string      `json:"service_pubkey"`
		OperatorPubkey      string      `json:"operator_pubkey"`
		Inner               nostr.Event `json:"inner"`
		Outer               nostr.Event `json:"outer"`
		GoOuter             nostr.Event `json:"go_outer"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	secretBytes, err := hex.DecodeString(fixture.ServiceSecretKeyHex)
	require.NoError(t, err)
	var secret nostr.SecretKey
	copy(secret[:], secretBytes)
	require.Equal(t, fixture.ServicePubkey, secret.Public().Hex())
	require.Equal(t, fixture.OperatorPubkey, fixture.Inner.PubKey.Hex())
	require.True(t, fixture.Inner.VerifySignature())
	ingress := NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{
		Signer: keyer.NewPlainKeySigner(secret), SensitiveDomains: []string{"org", "secret", "notification"}, Logger: zap.NewNop(),
	})
	rumor, err := ingress.UnwrapIntent(context.Background(), &fixture.Outer)
	require.NoError(t, err)
	require.NotNil(t, rumor)
	require.Equal(t, fixture.Inner.ID, rumor.ID)
	require.Equal(t, fixture.OperatorPubkey, rumor.PubKey.Hex())
	require.Equal(t, fixture.Inner.Content, rumor.Content)
	intent, err := ParseIntent(rumor)
	require.NoError(t, err)
	require.Equal(t, "secret", intent.Domain)
	require.Equal(t, "fixture-secret", intent.Content["name"])

	// The reverse fixture is generated with fixed test-only ephemeral key and
	// NIP-44 nonces. Production GiftWrap continues to use fresh randomness.
	goOuter := deterministicGoIntentWrap(t, fixture.Inner, fixture.ServicePubkey)
	if os.Getenv("UPDATE_GIFTWRAP_FIXTURE") == "1" {
		fixture.GoOuter = goOuter
		updated, err := json.MarshalIndent(fixture, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(fixturePath, append(updated, '\n'), 0600))
	}
	require.Equal(t, goOuter, fixture.GoOuter, "Go-generated wrap differs from committed web fixture")
	goRumor, err := ingress.UnwrapIntent(context.Background(), &fixture.GoOuter)
	require.NoError(t, err)
	require.Equal(t, fixture.Inner.ID, goRumor.ID)
}

func deterministicGoIntentWrap(t *testing.T, inner nostr.Event, servicePubkey string) nostr.Event {
	t.Helper()
	operatorKey, err := nostr.SecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000003")
	require.NoError(t, err)
	ephemeralKey, err := nostr.SecretKeyFromHex("0000000000000000000000000000000000000000000000000000000000000004")
	require.NoError(t, err)
	recipient, err := nostr.PubKeyFromHex(servicePubkey)
	require.NoError(t, err)
	rumor := inner
	rumor.Sig = [64]byte{}
	operatorConversation, err := nip44.GenerateConversationKey(recipient, operatorKey)
	require.NoError(t, err)
	sealCiphertext, err := nip44.Encrypt(rumor.String(), operatorConversation, nip44.WithCustomNonce([]byte("0123456789abcdef0123456789abcdef")))
	require.NoError(t, err)
	seal := nostr.Event{Kind: 13, CreatedAt: 1791038200, Tags: nostr.Tags{}, Content: sealCiphertext}
	require.NoError(t, seal.Sign(operatorKey))
	ephemeralConversation, err := nip44.GenerateConversationKey(recipient, ephemeralKey)
	require.NoError(t, err)
	wrapCiphertext, err := nip44.Encrypt(seal.String(), ephemeralConversation, nip44.WithCustomNonce([]byte("fedcba9876543210fedcba9876543210")))
	require.NoError(t, err)
	outer := nostr.Event{Kind: 1059, CreatedAt: 1791036400, Tags: nostr.Tags{{"p", servicePubkey}}, Content: wrapCiphertext}
	require.NoError(t, outer.Sign(ephemeralKey))
	return outer
}
