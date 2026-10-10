package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/service"
)

type localAssistantWrapFixture struct {
	secret nostr.SecretKey
	pubkey nostr.PubKey
	denied bool
}

func (f localAssistantWrapFixture) GetPublicKey(context.Context) (nostr.PubKey, error) {
	if f.denied {
		return nostr.ZeroPK, errors.New("lease refused")
	}
	return f.pubkey, nil
}

func (f localAssistantWrapFixture) Encrypt(_ context.Context, plain string, peer nostr.PubKey) (string, error) {
	if f.denied {
		return "", errors.New("lease refused")
	}
	key, err := nip44.GenerateConversationKey(peer, f.secret)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plain, key)
}

func (f localAssistantWrapFixture) Decrypt(_ context.Context, cipher string, peer nostr.PubKey) (string, error) {
	if f.denied {
		return "", errors.New("lease refused")
	}
	key, err := nip44.GenerateConversationKey(peer, f.secret)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(cipher, key)
}

func assistantWrapFixture(t *testing.T) localAssistantWrapFixture {
	t.Helper()
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	return localAssistantWrapFixture{secret: secret, pubkey: secret.Public()}
}

func TestAssistantWrappedManifestPreservesLegacyAndRandomizesActiveKey(t *testing.T) {
	ctx := context.Background()
	wrapper := assistantWrapFixture(t)
	legacy := service.AssistantTranscriptKey{Ref: "assistant-transcript/service-nostr-key", Version: "v1", Rotation: "service-nostr-key", Key: bytes.Repeat([]byte{0x42}, 32)}
	first, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, &legacy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, &legacy)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, legacy.Key) || bytes.Contains(encoded, []byte(`"key"`)) {
		t.Fatal("manifest exposes a plaintext key field")
	}
	var stored AssistantWrappedKeyManifest
	if err := json.Unmarshal(encoded, &stored); err != nil {
		t.Fatal(err)
	}
	provider, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, stored)
	if err != nil {
		t.Fatal(err)
	}
	active, err := provider.ActiveTranscriptKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.Ref != first.Active.Ref || active.Version != first.Active.Version || bytes.Equal(active.Key, legacy.Key) {
		t.Fatal("active key is not the separately versioned random data key")
	}
	other, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, second)
	if err != nil {
		t.Fatal(err)
	}
	otherActive, err := other.ActiveTranscriptKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(active.Key, otherActive.Key) {
		t.Fatal("fresh manifests reused the data key")
	}
	old, err := provider.TranscriptKey(ctx, legacy.Ref, legacy.Version)
	if err != nil || !bytes.Equal(old.Key, legacy.Key) {
		t.Fatalf("legacy key not recovered: %v", err)
	}
	if _, err := provider.TranscriptKey(ctx, legacy.Ref, "v2"); err == nil {
		t.Fatal("wrong historical version resolved")
	}
	if _, err := provider.TranscriptKey(ctx, active.Ref, ""); err == nil {
		t.Fatal("versionless lookup resolved")
	}
	active.Key[0] ^= 0xff
	again, err := provider.ActiveTranscriptKey(ctx)
	if err != nil || bytes.Equal(active.Key, again.Key) {
		t.Fatal("provider returned mutable key storage")
	}
}

func TestAssistantWrappedManifestRejectsWrongIdentityAndTampering(t *testing.T) {
	ctx := context.Background()
	wrapper := assistantWrapFixture(t)
	manifest, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrong := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64)).Public()
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrong, manifest); err == nil {
		t.Fatal("wrong service pubkey accepted")
	}
	if _, err := createAssistantWrappedKeyManifest(ctx, localAssistantWrapFixture{pubkey: wrong}, wrapper.pubkey, nil); err == nil {
		t.Fatal("wrong wrapper pubkey accepted")
	}
	if _, err := openAssistantWrappedKeyManifest(ctx, localAssistantWrapFixture{pubkey: wrapper.pubkey, denied: true}, wrapper.pubkey, manifest); err == nil {
		t.Fatal("fenced operation refusal accepted")
	}
	tampered := manifest
	tampered.Active.Version = "v3"
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, tampered); err == nil {
		t.Fatal("metadata substitution accepted")
	}
	tampered = manifest
	tampered.Active.Ciphertext = "malformed"
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, tampered); err == nil {
		t.Fatal("ciphertext corruption accepted")
	}
	tampered = manifest
	tampered.Legacy = &tampered.Active
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, tampered); err == nil {
		t.Fatal("duplicate key identity accepted")
	}
}
