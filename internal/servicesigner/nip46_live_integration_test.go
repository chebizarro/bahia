//go:build signetinterop

package servicesigner

import (
	"bytes"
	"context"
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
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/stretchr/testify/require"
)

// liveFixture is written by scripts/signet_live_interop.py. It names only
// disposable synthetic identities on a loopback relay. The bunker under test
// happens to be Signet: the writer is the client key currently assigned with
// its agent/writer-acquire, and the displaced writer was assigned first and
// then replaced. Bahia itself only sees a standard NIP-46 bunker.
type liveFixture struct {
	Disposable                  bool   `json:"disposable"`
	SignetCommit                string `json:"signet_commit"`
	ExpectedBunkerPubkey        string `json:"expected_bunker_pubkey"`
	ExpectedServicePubkey       string `json:"expected_service_pubkey"`
	WriterBunkerURI             string `json:"writer_bunker_uri"`
	WriterSecretKeyHex          string `json:"writer_secret_key_hex"`
	DisplacedBunkerURI          string `json:"displaced_bunker_uri,omitempty"`
	DisplacedWriterSecretKeyHex string `json:"displaced_writer_secret_key_hex,omitempty"`
}

var liveCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func loadLiveFixture(t *testing.T, wantDisplaced bool) liveFixture {
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
	var fixture liveFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.True(t, fixture.Disposable, "only disposable synthetic service identities are allowed")
	require.True(t, liveCommitPattern.MatchString(fixture.SignetCommit), "fixture must pin the full bunker commit under test")
	require.NotEmpty(t, fixture.ExpectedBunkerPubkey)
	require.NotEmpty(t, fixture.ExpectedServicePubkey)
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), strings.ToLower(fixture.ExpectedBunkerPubkey),
		"bunker and service identities must be distinct")
	require.NoError(t, validateLiveBunkerURI(fixture.WriterBunkerURI, fixture.ExpectedBunkerPubkey))
	writer, err := nostr.SecretKeyFromHex(fixture.WriterSecretKeyHex)
	require.NoError(t, err)
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), writer.Public().Hex())
	if !wantDisplaced {
		require.Empty(t, fixture.DisplacedWriterSecretKeyHex)
		require.Empty(t, fixture.DisplacedBunkerURI)
		return fixture
	}
	require.NoError(t, validateLiveBunkerURI(fixture.DisplacedBunkerURI, fixture.ExpectedBunkerPubkey))
	displaced, err := nostr.SecretKeyFromHex(fixture.DisplacedWriterSecretKeyHex)
	require.NoError(t, err)
	require.NotEqual(t, writer.Public(), displaced.Public(), "writer client keys are single-use")
	require.NotEqual(t, strings.ToLower(fixture.ExpectedServicePubkey), displaced.Public().Hex())
	return fixture
}

func validateLiveBunkerURI(rawURI, expectedBunkerPubkey string) error {
	invalid := errors.New("live bunker URI must pin the expected bunker pubkey and use one loopback IP relay")
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

func liveAdmission() *nostrout.Admission {
	generous := nostrout.PurposeBudget{RatePerMinute: 600_000, Burst: 100_000}
	return nostrout.New(nostrout.Config{
		Aggregate: generous,
		PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
			nostrout.PurposePriority: generous, nostrout.PurposeState: generous, nostrout.PurposeGeneral: generous,
			nostrout.PurposeBulk: generous, nostrout.PurposeSigner: generous,
		},
		RelayWire: generous, RelayWirePriority: generous,
	})
}

func liveNostrConfig(bunkerURI, clientSecretHex, servicePubkey string) config.NostrConfig {
	return config.NostrConfig{PublicKey: servicePubkey, Signer: config.NostrSignerConfig{
		Method: config.NostrSignerNIP46, BunkerURI: bunkerURI, ClientSecretKey: clientSecretHex, Timeout: time.Minute,
	}}
}

// openLive opens Bahia's service signer exactly as startup does. Errors are
// not echoed: they may carry the pairing URI.
func openLive(t *testing.T, ctx context.Context, bunkerURI, clientSecretHex, servicePubkey string) nostr.Keyer {
	t.Helper()
	signer, err := Open(ctx, liveNostrConfig(bunkerURI, clientSecretHex, servicePubkey), Options{Admission: liveAdmission()})
	if err != nil {
		t.Fatal("failed to open the NIP-46 service signer against the prevalidated local bunker; inspect local daemon logs")
	}
	return signer
}

func liveEvent(content string) nostr.Event {
	return nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"t", "bahia-nip46-interop"}}, Content: content}
}

// TestLiveNIP46AssignedWriterSigns runs while the first writer is assigned.
// It proves the bunker admits that client key, so the later refusal of the
// same key is caused by reassignment and nothing else.
func TestLiveNIP46AssignedWriterSigns(t *testing.T) {
	fixture := loadLiveFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	signer := openLive(t, ctx, fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, fixture.ExpectedServicePubkey)
	ev := liveEvent("assigned writer")
	require.NoError(t, signer.SignEvent(ctx, &ev))
	require.Equal(t, fixture.ExpectedServicePubkey, ev.PubKey.Hex())
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
}

func TestLiveNIP46ServiceSigner(t *testing.T) {
	fixture := loadLiveFixture(t, true)
	writer, err := nostr.SecretKeyFromHex(fixture.WriterSecretKeyHex)
	require.NoError(t, err)
	servicePubkey, err := nostr.PubKeyFromHex(fixture.ExpectedServicePubkey)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Startup verification: a bunker that reports a different identity than
	// nostr.public_key is fatal.
	_, err = Open(ctx, liveNostrConfig(fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, nostr.Generate().Public().Hex()), Options{Admission: liveAdmission()})
	require.ErrorContains(t, err, "differs from configured service pubkey")

	signer := openLive(t, ctx, fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, fixture.ExpectedServicePubkey)
	actual, err := signer.GetPublicKey(ctx)
	require.NoError(t, err)
	require.Equal(t, servicePubkey, actual)

	ev := liveEvent("current writer")
	require.NoError(t, signer.SignEvent(ctx, &ev))
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
	require.NoError(t, sbom.SignAttestation(ctx, att, signer))
	require.Nil(t, att.Envelope)
	require.NotNil(t, att.Event)
	require.Equal(t, servicePubkey.Hex(), att.Event.PubKey)
	require.NoError(t, sbom.VerifyAttestationSignature(att, servicePubkey.Hex()))

	// The peer is the writer's own client identity. Its secret is known to
	// this test, so every bunker result is checked with an independent local
	// NIP-44 implementation over the same service/peer conversation key.
	conversation, err := nip44.GenerateConversationKey(servicePubkey, writer)
	require.NoError(t, err)
	textCipher, err := signer.Encrypt(ctx, "Bahia NIP-46 text interop", writer.Public())
	require.NoError(t, err)
	textLocal, err := nip44.Decrypt(textCipher, conversation)
	require.NoError(t, err)
	require.Equal(t, "Bahia NIP-46 text interop", textLocal)
	localCipher, err := nip44.Encrypt("sealed locally for the bunker", conversation)
	require.NoError(t, err)
	textPlain, err := signer.Decrypt(ctx, localCipher, writer.Public())
	require.NoError(t, err)
	require.Equal(t, "sealed locally for the bunker", textPlain)

	binary, ok := signer.(BinaryCipher)
	require.True(t, ok, "NIP-46 service signer offers BinaryCipher")
	binaryPlain := []byte{0, 0xff, 0x80, 0x7f}
	binaryCipher, err := binary.EncryptBytes(ctx, binaryPlain, writer.Public())
	require.NoError(t, err)
	binaryLocal, err := nip44.Decrypt(binaryCipher, conversation)
	require.NoError(t, err)
	require.True(t, bytes.Equal(binaryPlain, []byte(binaryLocal)))
	localBinaryCipher, err := nip44.Encrypt(string(binaryPlain), conversation)
	require.NoError(t, err)
	binaryOpened, err := binary.DecryptBytes(ctx, localBinaryCipher, writer.Public())
	require.NoError(t, err)
	require.True(t, bytes.Equal(binaryPlain, binaryOpened))

	// The displaced writer still pairs and reads the public identity, but the
	// bunker refuses every key operation. Each refusal is a *remote* one: the
	// vendored NIP-46 client emits "response error: " only after it decrypts
	// a bunker response. A refusal is never reported as a missing capability.
	displaced := openLive(t, ctx, fixture.DisplacedBunkerURI, fixture.DisplacedWriterSecretKeyHex, fixture.ExpectedServicePubkey)
	displacedBinary := displaced.(BinaryCipher)
	refused := liveEvent("displaced writer")
	original := refused
	for name, call := range map[string]func() error{
		"sign_event": func() error { return displaced.SignEvent(ctx, &refused) },
		"nip44_encrypt": func() error {
			_, err := displaced.Encrypt(ctx, "Bahia NIP-46 text interop", writer.Public())
			return err
		},
		"nip44_decrypt": func() error { _, err := displaced.Decrypt(ctx, textCipher, writer.Public()); return err },
		"nip44_encrypt_b64": func() error {
			_, err := displacedBinary.EncryptBytes(ctx, binaryPlain, writer.Public())
			return err
		},
		"nip44_decrypt_b64": func() error {
			_, err := displacedBinary.DecryptBytes(ctx, binaryCipher, writer.Public())
			return err
		},
	} {
		err := call()
		require.Error(t, err, "%s had no refusal", name)
		require.Contains(t, err.Error(), "response error: ", "%s did not receive an authenticated remote refusal", name)
		require.NotErrorIs(t, err, errors.ErrUnsupported, "%s refusal reported as unsupported", name)
		require.NoError(t, ctx.Err(), "%s context failed during remote refusal", name)
	}
	require.Equal(t, original, refused, "refused sign_event mutated the request")

	// The assigned writer is unaffected by the displaced writer's attempts.
	after := liveEvent("current writer after refusals")
	require.NoError(t, signer.SignEvent(ctx, &after))
	require.Equal(t, servicePubkey, after.PubKey)

	// The session lives as long as the context it was opened with.
	scoped, closeScoped := context.WithCancel(ctx)
	short := openLive(t, scoped, fixture.WriterBunkerURI, fixture.WriterSecretKeyHex, fixture.ExpectedServicePubkey)
	closeScoped()
	closed := liveEvent("after close")
	require.ErrorIs(t, short.SignEvent(ctx, &closed), errClosed)
}

func TestLiveBunkerURIValidation(t *testing.T) {
	bunkerPubkey := nostr.Generate().Public().Hex()
	servicePubkey := nostr.Generate().Public().Hex()
	secret := "private-pairing-secret-not-for-output"
	for name, uri := range map[string]string{
		"valid ipv4": "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F127.0.0.1%3A7777",
		"valid ipv6": "bunker://" + bunkerPubkey + "?relay=ws%3A%2F%2F%5B%3A%3A1%5D%3A7777",
	} {
		t.Run(name, func(t *testing.T) { require.NoError(t, validateLiveBunkerURI(uri, bunkerPubkey)) })
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
			err := validateLiveBunkerURI(uri, bunkerPubkey)
			require.Error(t, err)
			require.NotContains(t, err.Error(), uri)
			require.NotContains(t, err.Error(), secret)
		})
	}
}
