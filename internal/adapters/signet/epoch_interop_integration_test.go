//go:build signetinterop

package signet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// This test uses the actual authenticated NIP-46 connection, not a replacement
// RPC function. The disposable Signet identity and lease are operator-provisioned.
type epochInteropFixture struct {
	Disposable            bool   `json:"disposable"`
	SignetCommit          string `json:"signet_commit"`
	BunkerURI             string `json:"bunker_uri"`
	OwnerSecretKeyHex     string `json:"owner_secret_key_hex"`
	ExpectedServicePubkey string `json:"expected_service_pubkey"`
	Epoch                 uint64 `json:"epoch"`
	ExpiresAt             string `json:"expires_at"`
}

const expectedInteropSignetCommit = "d097cea2219a3784ccf011d1e9bf94f9fb7f8ce2"

func loadEpochInteropFixture(t *testing.T) (epochInteropFixture, time.Time) {
	t.Helper()
	path := os.Getenv("BAHIA_SIGNET_INTEROP_CONFIG")
	if path == "" {
		t.Fatal("BAHIA_SIGNET_INTEROP_CONFIG must name a private disposable fixture JSON file")
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077, "fixture file must not be group/world readable")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture epochInteropFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.True(t, fixture.Disposable, "only disposable synthetic service identities are allowed")
	require.Equal(t, expectedInteropSignetCommit, fixture.SignetCommit)
	require.NotEmpty(t, fixture.BunkerURI)
	require.NotEmpty(t, fixture.OwnerSecretKeyHex)
	require.NotEmpty(t, fixture.ExpectedServicePubkey)
	require.Greater(t, fixture.Epoch, uint64(1), "acquire twice so stale-epoch proof uses a positive epoch")
	expiresAt, err := time.Parse(time.RFC3339Nano, fixture.ExpiresAt)
	require.NoError(t, err)
	require.True(t, time.Now().Add(30*time.Second).Before(expiresAt), "lease must remain live throughout test")
	return fixture, expiresAt
}

func TestLiveSignetEpochNIP44AndSBOMDSSE(t *testing.T) {
	fixture, expiresAt := loadEpochInteropFixture(t)
	owner, err := nostr.SecretKeyFromHex(fixture.OwnerSecretKeyHex)
	require.NoError(t, err)
	servicePubkey, err := nostr.PubKeyFromHex(fixture.ExpectedServicePubkey)
	require.NoError(t, err)
	lease := WriterLease{Epoch: fixture.Epoch, OwnerPubkey: owner.Public(), ExpiresAt: expiresAt}
	client, err := NewClient(Config{
		BunkerURI: fixture.BunkerURI, ClientSecretKey: fixture.OwnerSecretKeyHex,
		RequireReal: true, ExpectedServicePubkey: fixture.ExpectedServicePubkey,
		EpochLease: func(context.Context) (WriterLease, error) { return lease, nil },
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.NoError(t, client.Connect(ctx))
	actual, err := client.epochSigner.GetPublicKey(ctx)
	require.NoError(t, err)
	require.Equal(t, servicePubkey, actual)

	// The peer is the separate authenticated owner identity; both directions
	// use the same service/peer NIP-44 conversation key.
	textCipher, err := client.NIP44Encrypt(ctx, owner.Public(), "Bahia Signet text interop")
	require.NoError(t, err)
	textPlain, err := client.NIP44Decrypt(ctx, owner.Public(), textCipher)
	require.NoError(t, err)
	require.Equal(t, "Bahia Signet text interop", textPlain)
	binaryPlain := []byte{0, 0xff, 0x80, 0x7f}
	binaryCipher, err := client.NIP44EncryptBytes(ctx, owner.Public(), binaryPlain)
	require.NoError(t, err)
	binaryOpened, err := client.epochSigner.decryptBytes(ctx, binaryCipher, owner.Public())
	require.NoError(t, err)
	require.True(t, bytes.Equal(binaryPlain, binaryOpened))

	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	att, err := sbom.NewAttestationBuilder("interop", "1", owner.Public().Hex()).BuildAttestation(sbom.BuildAttestationInput{
		SubjectName: "disposable-artifact", SubjectDigest: "sha256:" + fmt.Sprintf("%064x", 1),
		SBOMData: []byte("{\"spdxVersion\":\"SPDX-2.3\"}"), Format: domain.SBOMFormatSPDX,
		Location:  domain.SBOMLocation{Type: domain.SBOMStorageBlossom, URI: "https://example.invalid/disposable-sbom"},
		Timestamp: &stamp,
	})
	require.NoError(t, err)
	require.NoError(t, sbom.SignAttestation(ctx, att, client.epochSigner))
	require.Equal(t, servicePubkey.Hex(), att.Envelope.Signatures[0].KeyID)
	require.NoError(t, sbom.VerifyAttestationSignature(att, servicePubkey.Hex()))

	// Bypass only the Bahia adapter's local gate for negative *remote* proofs.
	// The same authenticated NIP-46 connection must yield no value without an
	// epoch or with a stale epoch after a successful fenced operation.
	client.mu.Lock()
	bunker := client.bunker
	client.mu.Unlock()
	require.NotNil(t, bunker)
	staleEpoch := strconv.FormatUint(fixture.Epoch-1, 10)
	for _, request := range []struct {
		method string
		params []string
	}{
		{"nip44_decrypt", []string{owner.Public().Hex(), textCipher}},
		{"nip44_decrypt", []string{owner.Public().Hex(), textCipher, staleEpoch}},
		{"sign_bahia_sbom_dsse", []string{att.Envelope.Payload}},
		{"sign_bahia_sbom_dsse", []string{att.Envelope.Payload, staleEpoch}},
	} {
		result, callErr := bunker.RPC(ctx, request.method, request.params)
		require.True(t, callErr != nil || result == "", "%s returned a value without current epoch", request.method)
	}
}
