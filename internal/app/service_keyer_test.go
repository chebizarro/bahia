package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"golang.org/x/crypto/chacha20poly1305"
)

func TestServiceKeyerLocalModeKeepsIdentity(t *testing.T) {
	cfg := config.Defaults()
	if k, err := newServiceKeyer(cfg); err != nil || k != nil {
		t.Fatalf("unset key: newServiceKeyer = %v, %v; want nil, nil", k, err)
	}
	secret := nostr.Generate()
	cfg.Nostr.PrivateKey = secret.Hex()
	serviceKeyer, err := newServiceKeyer(cfg)
	if err != nil {
		t.Fatal(err)
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
	if _, err := newServiceKeyer(cfg); err == nil || strings.Contains(err.Error(), "not-hex") {
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
