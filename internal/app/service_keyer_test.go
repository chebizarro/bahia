package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/servicesigner"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"golang.org/x/crypto/chacha20poly1305"
)

// remoteTestConfig selects a NIP-46 service signer; tests replace
// openServiceSigner, so no bunker is contacted.
func remoteTestConfig(service nostr.PubKey, client nostr.SecretKey) config.NostrConfig {
	return config.NostrConfig{PublicKey: service.Hex(), Signer: config.NostrSignerConfig{
		Method:          config.NostrSignerNIP46,
		BunkerURI:       "bunker://" + service.Hex() + "?relay=wss://bunker.invalid",
		ClientSecretKey: client.Hex(),
		Timeout:         5 * time.Second,
	}}
}

// fakeRemoteSigner stands in for one remote signer session: it signs with an
// in-process key but, like the NIP-46 and NIP-55L keyers, exposes only
// nostr.Keyer and io.Closer.
type fakeRemoteSigner struct {
	nostr.Keyer
	opener *fakeSignerOpener
}

func (f fakeRemoteSigner) Close() error {
	f.opener.mu.Lock()
	defer f.opener.mu.Unlock()
	f.opener.closes++
	return nil
}

// fakeSignerOpener counts the signer sessions newServiceKeyer opens and
// closes, and keeps each session's lifetime context.
type fakeSignerOpener struct {
	mu        sync.Mutex
	opens     int
	closes    int
	lifetimes []context.Context
	opts      servicesigner.Options
}

func stubServiceSignerOpen(t *testing.T, service nostr.SecretKey) *fakeSignerOpener {
	t.Helper()
	opener := &fakeSignerOpener{}
	previous := openServiceSigner
	t.Cleanup(func() { openServiceSigner = previous })
	openServiceSigner = func(ctx context.Context, _ config.NostrConfig, opts servicesigner.Options) (nostr.Keyer, error) {
		local, err := nostrutil.NewLocalKeyer(service.Hex())
		if err != nil {
			return nil, err
		}
		opener.mu.Lock()
		defer opener.mu.Unlock()
		opener.opens++
		opener.lifetimes = append(opener.lifetimes, ctx)
		opener.opts = opts
		return fakeRemoteSigner{Keyer: local, opener: opener}, nil
	}
	return opener
}

// counts returns the sessions opened and closed so far.
func (o *fakeSignerOpener) counts() (opens, closes int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens, o.closes
}

// live reports, per opened session, whether its lifetime is still running.
func (o *fakeSignerOpener) live() []bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	live := make([]bool, len(o.lifetimes))
	for i, ctx := range o.lifetimes {
		live[i] = ctx.Err() == nil
	}
	return live
}

func TestServiceKeyerLogsBunkerAuthURLToAppLogger(t *testing.T) {
	service := nostr.Generate()
	opener := stubServiceSignerOpen(t, service)
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Defaults()
	cfg.Nostr = remoteTestConfig(service.Public(), nostr.Generate())

	_, release, err := newServiceKeyer(cfg, nil, zap.New(core), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if opener.opts.OnAuthURL == nil {
		t.Fatal("the app logger is not wired to the signer's authorization callback")
	}
	const authURL = "https://bunker.example/authorize/abc"
	opener.opts.OnAuthURL(authURL)
	entries := logs.FilterField(zap.String("url", authURL)).All()
	if len(entries) != 1 || entries[0].Level != zapcore.WarnLevel || entries[0].Message != servicesigner.AuthURLMessage {
		t.Fatalf("auth URL log entries = %+v; want one warn entry on the app logger", logs.All())
	}
}

func TestServiceKeyerResolvesRotatedClientKeyFile(t *testing.T) {
	service := nostr.Generate()
	opener := stubServiceSignerOpen(t, service)
	path := filepath.Join(t.TempDir(), "client.key")
	writeKey := func() {
		if err := os.WriteFile(path, []byte(nostr.Generate().Hex()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Defaults()
	cfg.Nostr = remoteTestConfig(service.Public(), nostr.Generate())
	cfg.Nostr.Signer.ClientSecretKey, cfg.Nostr.Signer.ClientSecretKeyFile = "", path
	writeKey()

	running, releaseRunning, err := newServiceKeyer(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	same, releaseSame, err := newServiceKeyer(cfg, nil, nil, running)
	if err != nil {
		t.Fatal(err)
	}
	if same != running {
		t.Fatal("an unchanged key file must reuse the running session")
	}
	writeKey()
	rotated, releaseRotated, err := newServiceKeyer(cfg, nil, nil, running)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == running {
		t.Fatal("a rotated key file under the same path must open a new session")
	}
	if opens, _ := opener.counts(); opens != 2 {
		t.Fatalf("opens = %d, want 2", opens)
	}
	releaseRunning()
	releaseSame()
	releaseRotated()
	if opens, closes := opener.counts(); closes != opens {
		t.Fatalf("closes = %d, want %d", closes, opens)
	}
}

func TestServiceKeyerLocalModeKeepsIdentity(t *testing.T) {
	cfg := config.Defaults()
	if k, _, err := newServiceKeyer(cfg, nil, nil, nil); err != nil || k != nil {
		t.Fatalf("unset key: newServiceKeyer = %v, %v; want nil, nil", k, err)
	}
	secret := nostr.Generate()
	cfg.Nostr.PrivateKey = secret.Hex()
	signer, closeServiceKeyer, err := newServiceKeyer(cfg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServiceKeyer()
	serviceKeyer := signer.Keyer()
	// Local mode must keep the raw-key derivations working byte for byte.
	if _, err := nostrutil.RequireServiceKeyMaterial(serviceKeyer, "test"); err != nil {
		t.Fatalf("local service keyer lacks key material: %v", err)
	}
	pubkey, _ := serviceKeyer.GetPublicKey(context.Background())
	if pubkey != secret.Public() {
		t.Fatalf("pubkey = %s, want %s", pubkey.Hex(), secret.Public().Hex())
	}
	ev := &nostr.Event{Kind: 1, CreatedAt: 1, Content: "x"}
	if err := serviceKeyer.SignEvent(context.Background(), ev); err != nil || !ev.VerifySignature() || ev.PubKey != secret.Public() {
		t.Fatalf("local signature invalid: %v", err)
	}
	cfg.Nostr.PrivateKey = "not-hex"
	if _, _, err := newServiceKeyer(cfg, nil, nil, nil); err == nil || strings.Contains(err.Error(), "not-hex") {
		t.Fatalf("invalid key error = %v; must fail without echoing the key", err)
	}
}

// TestRawKeyDerivationsFailClosedForRemoteKeyer covers every (b) consumer in
// internal/app with a signer that holds no in-process key material.
func TestRawKeyDerivationsFailClosedForRemoteKeyer(t *testing.T) {
	remote := keyer.NewPlainKeySigner(nostr.Generate())

	legacy, blocked := legacyO1OrgStateDecryptor(remote)
	if !errors.Is(blocked, nostrutil.ErrServiceKeyMaterialRequired) || legacy == nil {
		t.Fatalf("legacyO1OrgStateDecryptor(remote) = %v, %v", legacy, blocked)
	}
	if _, err := legacy.DecryptOrgState("{}"); !errors.Is(err, nostrutil.ErrServiceKeyMaterialRequired) {
		t.Fatalf("blocked O1 read error = %v", err)
	}
	if legacy, err := legacyO1OrgStateDecryptor(nil); legacy != nil || err != nil {
		t.Fatalf("no identity: legacyO1OrgStateDecryptor = %v, %v; want nil, nil", legacy, err)
	}

	cfg := config.Defaults()
	cfg.Assistant.Enabled = true
	if _, err := assistantTranscriptKeyProvider(cfg); !errors.Is(err, nostrutil.ErrServiceKeyMaterialRequired) {
		t.Fatalf("assistantTranscriptKeyProvider without local key = %v", err)
	}
}

// TestLegacyO1LocalModeReadsDeployedRecords seals a record exactly as the
// deployed O1 writer did and reads it through the local-mode decryptor.
func TestLegacyO1LocalModeReadsDeployedRecords(t *testing.T) {
	key := nostr.Generate().Hex()
	local, err := nostrutil.NewLocalKeyer(key)
	if err != nil {
		t.Fatal(err)
	}
	decryptor, blocked := legacyO1OrgStateDecryptor(local)
	if blocked != nil {
		t.Fatal(blocked)
	}
	sum := sha256.Sum256([]byte("bahia org state key v1\x00" + key))
	aead, err := chacha20poly1305.NewX(sum[:])
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	_, _ = rand.Read(nonce)
	ad := map[string]string{"d": "org-1", "t": "org"}
	adBytes, _ := json.Marshal(ad)
	envelope, _ := json.Marshal(controlplane.OrgStateAEADEnvelope{
		Schema: controlplane.OrgStateCryptoSchema, Envelope: controlplane.OrgStateCryptoEnvelope,
		Algorithm: controlplane.OrgStateCryptoAlgorithm, KeyRef: "org-state/service-nostr-key", KeyVersion: "v1",
		Nonce:          base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext:     base64.RawStdEncoding.EncodeToString(aead.Seal(nil, nonce, []byte("member"), adBytes)),
		AssociatedData: ad,
	})
	plaintext, err := decryptor.DecryptOrgState(string(envelope))
	if err != nil || string(plaintext) != "member" {
		t.Fatalf("DecryptOrgState = %q, %v", plaintext, err)
	}
}

func TestBlossomSignerIsOptionalAndSeparate(t *testing.T) {
	if signer, err := blossomSigner(config.BlossomConfig{}); signer != nil || err != nil {
		t.Fatalf("unset blossom key = %v, %v; want anonymous", signer, err)
	}
	secret := nostr.Generate()
	signer, err := blossomSigner(config.BlossomConfig{PrivateKey: secret.Hex()})
	if err != nil {
		t.Fatal(err)
	}
	if owner := blossomOwnerKey(signer); owner != secret.Public().Hex() {
		t.Fatalf("blossom owner = %s, want %s", owner, secret.Public().Hex())
	}
}
