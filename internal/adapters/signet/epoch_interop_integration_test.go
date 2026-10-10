//go:build signetinterop

package signet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
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

const expectedInteropSignetCommit = "21eef0c050e426b158488214ed23f4e93bbd7169"

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
	require.NoError(t, validateInteropBunkerURI(fixture.BunkerURI, fixture.ExpectedServicePubkey))
	require.Greater(t, fixture.Epoch, uint64(1), "acquire twice so stale-epoch proof uses a positive epoch")
	expiresAt, err := time.Parse(time.RFC3339Nano, fixture.ExpiresAt)
	require.NoError(t, err)
	require.True(t, time.Now().Add(30*time.Second).Before(expiresAt), "lease must remain live throughout test")
	return fixture, expiresAt
}

func validateInteropBunkerURI(rawURI, expectedPubkey string) error {
	invalid := errors.New("interop bunker URI must pin the expected service pubkey and use one loopback IP relay")
	if len(rawURI) == 0 || len(rawURI) > 2048 {
		return invalid
	}
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "bunker" || u.User != nil || u.Fragment != "" || u.Path != "" || u.Port() != "" ||
		!strings.EqualFold(u.Hostname(), expectedPubkey) || len(u.Hostname()) != 64 {
		return invalid
	}
	if _, err := nostr.PubKeyFromHex(u.Hostname()); err != nil {
		return invalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["relay"]) != 1 || len(query["secret"]) > 1 {
		return invalid
	}
	for key := range query {
		if key != "relay" && key != "secret" {
			return invalid
		}
	}
	relay, err := url.Parse(query.Get("relay"))
	if err != nil || (relay.Scheme != "ws" && relay.Scheme != "wss") || relay.User != nil || relay.Fragment != "" {
		return invalid
	}
	ip := net.ParseIP(relay.Hostname())
	port, err := strconv.Atoi(relay.Port())
	if err != nil || ip == nil || !ip.IsLoopback() || port < 1 || port > 65535 {
		return invalid
	}
	return nil
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
		OutboundAdmission: generousTestAdmission(),
		EpochLease:        func(context.Context) (WriterLease, error) { return lease, nil },
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatal("failed to connect to the prevalidated local Signet fixture; inspect local daemon logs")
	}
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
	// The vendored NIP-46 client emits "response error: " only after receiving
	// a decrypted bunker response; transport/context errors have other shapes.
	client.mu.Lock()
	bunker := client.bunker
	client.mu.Unlock()
	require.NotNil(t, bunker)
	staleEpoch := strconv.FormatUint(fixture.Epoch-1, 10)
	for _, request := range []struct {
		method string
		params []string
	}{
		{"nip44_encrypt", []string{owner.Public().Hex(), "Bahia Signet text interop"}},
		{"nip44_decrypt", []string{owner.Public().Hex(), textCipher}},
		{"nip44_encrypt_b64", []string{owner.Public().Hex(), base64.StdEncoding.EncodeToString(binaryPlain)}},
		{"nip44_decrypt_b64", []string{owner.Public().Hex(), binaryCipher}},
		{"sign_bahia_sbom_dsse", []string{att.Envelope.Payload}},
	} {
		for _, stale := range []bool{false, true} {
			params := append([]string(nil), request.params...)
			if stale {
				params = append(params, staleEpoch)
			}
			result, callErr := bunker.RPC(ctx, request.method, params)
			require.Empty(t, result, "%s returned a value without current epoch", request.method)
			require.Error(t, callErr, "%s had no authenticated refusal", request.method)
			require.True(t, strings.HasPrefix(callErr.Error(), "response error: ") && len(callErr.Error()) > len("response error: "),
				"%s did not receive an authenticated remote refusal", request.method)
			require.NoError(t, ctx.Err(), "%s context failed during remote refusal", request.method)
			require.True(t, client.IsConnected(), "%s disconnected during remote refusal", request.method)
			if err := bunker.Ping(ctx); err != nil {
				t.Fatalf("%s lost the authenticated bunker after remote refusal", request.method)
			}
		}
	}
}

func TestInteropBunkerURIValidation(t *testing.T) {
	pubkey := nostr.Generate().Public().Hex()
	for name, uri := range map[string]string{
		"valid ipv4": "bunker://" + pubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"valid ipv6": "bunker://" + pubkey + "?relay=ws%3A%2F%2F%5B%3A%3A1%5D%3A7777",
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, validateInteropBunkerURI(uri, pubkey)) })
	}
	for name, uri := range map[string]string{
		"remote relay":     "bunker://" + pubkey + "?relay=wss%3A%2F%2Frelay.example%3A443",
		"lookalike host":   "bunker://" + pubkey + "?relay=ws%3A%2F%2F127.0.0.1.evil%3A7777",
		"wrong bunker":     "bunker://" + nostr.Generate().Public().Hex() + "?relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"extra relay":      "bunker://" + pubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777&relay=wss%3A%2F%2Frelay.example%3A443",
		"malformed secret": "bunker://" + pubkey + "?secret=%zz&relay=ws%3A%2F%2F127.0.0.1%3A7777",
	} {
		t.Run(name, func(t *testing.T) {
			err := validateInteropBunkerURI(uri, pubkey)
			require.Error(t, err)
			require.NotContains(t, err.Error(), uri)
		})
	}
}
