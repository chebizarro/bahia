//go:build signetinterop

package signet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
)

// interopFixture is written by scripts/signet_live_interop.py. It
// names only disposable synthetic identities on a loopback relay. The writer
// is the client key currently assigned with agent/writer-acquire; the
// displaced writer was assigned first and then replaced.
type interopFixture struct {
	Disposable                  bool   `json:"disposable"`
	SignetCommit                string `json:"signet_commit"`
	ExpectedBunkerPubkey        string `json:"expected_bunker_pubkey"`
	ExpectedServicePubkey       string `json:"expected_service_pubkey"`
	WriterBunkerURI             string `json:"writer_bunker_uri"`
	WriterSecretKeyHex          string `json:"writer_secret_key_hex"`
	DisplacedBunkerURI          string `json:"displaced_bunker_uri,omitempty"`
	DisplacedWriterSecretKeyHex string `json:"displaced_writer_secret_key_hex,omitempty"`
}

var interopCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadInteropFixture(t *testing.T, wantDisplaced bool) interopFixture {
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
	var fixture interopFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.True(t, fixture.Disposable, "only disposable synthetic service identities are allowed")
	require.True(t, interopCommitPattern.MatchString(fixture.SignetCommit), "fixture must pin the full Signet commit under test")
	require.NotEmpty(t, fixture.ExpectedBunkerPubkey)
	require.NotEmpty(t, fixture.ExpectedServicePubkey)
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), strings.ToLower(fixture.ExpectedBunkerPubkey),
		"Signet bunker and service identities must be distinct")
	require.NoError(t, validateInteropBunkerURI(fixture.WriterBunkerURI, fixture.ExpectedBunkerPubkey))
	writer, err := nostr.SecretKeyFromHex(fixture.WriterSecretKeyHex)
	require.NoError(t, err)
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), writer.Public().Hex())
	if !wantDisplaced {
		require.Empty(t, fixture.DisplacedWriterSecretKeyHex)
		require.Empty(t, fixture.DisplacedBunkerURI)
		return fixture
	}
	require.NoError(t, validateInteropBunkerURI(fixture.DisplacedBunkerURI, fixture.ExpectedBunkerPubkey))
	displaced, err := nostr.SecretKeyFromHex(fixture.DisplacedWriterSecretKeyHex)
	require.NoError(t, err)
	require.NotEqual(t, writer.Public(), displaced.Public(), "writer client keys are single-use")
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), displaced.Public().Hex())
	return fixture
}

func validateInteropBunkerURI(rawURI, expectedBunkerPubkey string) error {
	invalid := errors.New("interop bunker URI must pin the expected bunker pubkey and use one loopback IP relay")
	if len(rawURI) == 0 || len(rawURI) > 2048 {
		return invalid
	}
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "bunker" || u.User != nil || u.Fragment != "" || u.Path != "" || u.Port() != "" ||
		!strings.EqualFold(u.Hostname(), expectedBunkerPubkey) || len(u.Hostname()) != 64 {
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

// connectInteropServiceClient opens a fenced client pinned to the service
// pubkey. Connect errors are not echoed: they may carry the pairing URI.
func connectInteropServiceClient(t *testing.T, ctx context.Context, bunkerURI, clientSecretHex, servicePubkey string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BunkerURI: bunkerURI, ClientSecretKey: clientSecretHex,
		RequireReal: true, ExpectedServicePubkey: servicePubkey,
		OutboundAdmission: generousTestAdmission(),
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Connect(ctx); err != nil {
		t.Fatal("failed to connect to the prevalidated local Signet fixture; inspect local daemon logs")
	}
	return client
}

func interopEvent(content string) nostr.Event {
	return nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"t", "bahia-signet-interop"}}, Content: content}
}

// TestLiveSignetAssignedWriterSigns runs while the first writer is assigned.
// It pairs that client key and proves the fence admits it, so the later
// refusal of the same key is caused by reassignment and nothing else.
func TestLiveSignetAssignedWriterSigns(t *testing.T) {
	fixture := loadInteropFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := connectInteropServiceClient(t, ctx, fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, fixture.ExpectedServicePubkey)
	ev := interopEvent("assigned writer")
	require.NoError(t, client.Sign(ctx, &ev))
	require.Equal(t, fixture.ExpectedServicePubkey, ev.PubKey.Hex())
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
}

func TestLiveSignetStandardNIP46ServiceSigner(t *testing.T) {
	fixture := loadInteropFixture(t, true)
	writer, err := nostr.SecretKeyFromHex(fixture.WriterSecretKeyHex)
	require.NoError(t, err)
	servicePubkey, err := nostr.PubKeyFromHex(fixture.ExpectedServicePubkey)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := connectInteropServiceClient(t, ctx, fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, fixture.ExpectedServicePubkey)
	actual, err := client.serviceSigner.GetPublicKey(ctx)
	require.NoError(t, err)
	require.Equal(t, servicePubkey, actual)

	ev := interopEvent("current writer")
	require.NoError(t, client.Sign(ctx, &ev))
	require.Equal(t, servicePubkey, ev.PubKey)
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())

	// SBOM attestations are standard events signed through sign_event.
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	att, err := sbom.NewAttestationBuilder("interop", "1", servicePubkey.Hex()).BuildAttestation(sbom.BuildAttestationInput{
		SubjectName: "disposable-artifact", SubjectDigest: "sha256:" + strings.Repeat("1", 64),
		SBOMData: []byte(`{"spdxVersion":"SPDX-2.3"}`), Format: domain.SBOMFormatSPDX,
		Location:  domain.SBOMLocation{Type: domain.SBOMStorageBlossom, URI: "https://example.invalid/disposable-sbom"},
		Timestamp: &stamp,
	})
	require.NoError(t, err)
	require.NoError(t, sbom.SignAttestation(ctx, att, client.serviceSigner))
	require.Nil(t, att.Envelope)
	require.NotNil(t, att.Event)
	require.Equal(t, servicePubkey.Hex(), att.Event.PubKey)
	require.NoError(t, sbom.VerifyAttestationSignature(att, servicePubkey.Hex()))

	// The peer is the writer's own client identity. Its secret is known to
	// this test, so every Signet result is checked with an independent local
	// NIP-44 implementation over the same service/peer conversation key.
	conversation, err := nip44.GenerateConversationKey(servicePubkey, writer)
	require.NoError(t, err)
	textCipher, err := client.NIP44Encrypt(ctx, writer.Public(), "Bahia Signet text interop")
	require.NoError(t, err)
	textLocal, err := nip44.Decrypt(textCipher, conversation)
	require.NoError(t, err)
	require.Equal(t, "Bahia Signet text interop", textLocal)
	localCipher, err := nip44.Encrypt("sealed locally for Signet", conversation)
	require.NoError(t, err)
	textPlain, err := client.NIP44Decrypt(ctx, writer.Public(), localCipher)
	require.NoError(t, err)
	require.Equal(t, "sealed locally for Signet", textPlain)
	binaryPlain := []byte{0, 0xff, 0x80, 0x7f}
	binaryCipher, err := client.NIP44EncryptBytes(ctx, writer.Public(), binaryPlain)
	require.NoError(t, err)
	binaryLocal, err := nip44.Decrypt(binaryCipher, conversation)
	require.NoError(t, err)
	require.True(t, bytes.Equal(binaryPlain, []byte(binaryLocal)))

	// The displaced writer reconnects through its existing pairing. Bypass
	// only the Bahia adapter's local gate so each refusal is a *remote* one:
	// the vendored NIP-46 client emits "response error: " only after it
	// receives a decrypted bunker response.
	displaced := connectInteropServiceClient(t, ctx, fixture.DisplacedBunkerURI, fixture.DisplacedWriterSecretKeyHex, fixture.ExpectedServicePubkey)
	refused := interopEvent("displaced writer")
	require.Error(t, displaced.Sign(ctx, &refused))
	require.Equal(t, nostr.ZeroPK, refused.PubKey)
	displaced.mu.Lock()
	bunker := displaced.bunker
	displaced.mu.Unlock()
	require.NotNil(t, bunker)
	unsigned := interopEvent("displaced writer raw")
	for _, request := range []struct {
		method string
		params []string
	}{
		{"sign_event", []string{unsigned.String()}},
		{"nip44_encrypt", []string{writer.Public().Hex(), "Bahia Signet text interop"}},
		{"nip44_decrypt", []string{writer.Public().Hex(), textCipher}},
		{"nip44_encrypt_b64", []string{writer.Public().Hex(), base64.StdEncoding.EncodeToString(binaryPlain)}},
	} {
		result, callErr := bunker.RPC(ctx, request.method, request.params)
		require.Empty(t, result, "%s returned a value to a displaced writer", request.method)
		require.Error(t, callErr, "%s had no authenticated refusal", request.method)
		require.True(t, strings.HasPrefix(callErr.Error(), "response error: ") && len(callErr.Error()) > len("response error: "),
			"%s did not receive an authenticated remote refusal", request.method)
		require.NoError(t, ctx.Err(), "%s context failed during remote refusal", request.method)
		require.True(t, displaced.IsConnected(), "%s disconnected during remote refusal", request.method)
		if err := bunker.Ping(ctx); err != nil {
			t.Fatalf("%s lost the authenticated bunker after remote refusal", request.method)
		}
	}
}

func TestInteropBunkerURIValidation(t *testing.T) {
	bunkerPubkey := nostr.Generate().Public().Hex()
	servicePubkey := nostr.Generate().Public().Hex()
	secret := "private-pairing-secret-not-for-output"
	for name, uri := range map[string]string{
		"valid ipv4": "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"valid ipv6": "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F%5B%3A%3A1%5D%3A7777",
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, validateInteropBunkerURI(uri, bunkerPubkey)) })
	}
	for name, uri := range map[string]string{
		"remote relay":      "bunker://" + bunkerPubkey + "?relay=wss%3A%2F%2Frelay.example%3A443",
		"lookalike host":    "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F127.0.0.1.evil%3A7777",
		"service as bunker": "bunker://" + servicePubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"extra relay":       "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777&relay=wss%3A%2F%2Frelay.example%3A443",
		"secret in error":   "bunker://" + servicePubkey + "?secret=" + secret + "&relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"malformed secret":  "bunker://" + bunkerPubkey + "?secret=%zz&relay=ws%3A%2F%2F127.0.0.1%3A7777",
	} {
		t.Run(name, func(t *testing.T) {
			err := validateInteropBunkerURI(uri, bunkerPubkey)
			require.Error(t, err)
			require.NotContains(t, err.Error(), uri)
			require.NotContains(t, err.Error(), secret)
		})
	}
}
