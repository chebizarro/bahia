package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip44"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type localAssistantWrapFixture struct {
	secret nostr.SecretKey
	pubkey nostr.PubKey
	denied bool
}

func (f localAssistantWrapFixture) GetPublicKey(context.Context) (nostr.PubKey, error) {
	if f.denied {
		return nostr.ZeroPK, errors.New("writer refused")
	}
	return f.pubkey, nil
}

func (f localAssistantWrapFixture) Encrypt(_ context.Context, plain string, peer nostr.PubKey) (string, error) {
	if f.denied {
		return "", errors.New("writer refused")
	}
	key, err := nip44.GenerateConversationKey(peer, f.secret)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plain, key)
}

func (f localAssistantWrapFixture) Decrypt(_ context.Context, cipher string, peer nostr.PubKey) (string, error) {
	if f.denied {
		return "", errors.New("writer refused")
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
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	legacyProvider, err := assistantTranscriptKeyProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := legacyProvider.ActiveTranscriptKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, cfg)
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
	if _, err := provider.ActiveTranscriptKey(ctx); err == nil {
		t.Fatal("unpersisted v2 manifest was accepted for writing")
	}
	active, err := provider.TranscriptKey(ctx, first.Active.Ref, first.Active.Version)
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
	otherActive, err := other.TranscriptKey(ctx, second.Active.Ref, second.Active.Version)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(active.Key, otherActive.Key) {
		t.Fatal("fresh manifests reused the data key")
	}
	if first.Active.Version == second.Active.Version {
		t.Fatal("fresh manifests reused the v2 generation identity")
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
	again, err := provider.TranscriptKey(ctx, first.Active.Ref, first.Active.Version)
	if err != nil || bytes.Equal(active.Key, again.Key) {
		t.Fatal("provider returned mutable key storage")
	}
}

func TestAssistantWrappedManifestRejectsWrongIdentityAndTampering(t *testing.T) {
	ctx := context.Background()
	wrapper := assistantWrapFixture(t)
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	manifest, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wrong := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64)).Public()
	wrongConfig := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("2", 64)}}
	if _, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, wrongConfig); err == nil {
		t.Fatal("legacy config key differs from fenced service pubkey")
	}
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrong, manifest); err == nil {
		t.Fatal("wrong service pubkey accepted")
	}
	if _, err := createAssistantWrappedKeyManifest(ctx, localAssistantWrapFixture{pubkey: wrong}, wrapper.pubkey, cfg); err == nil {
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
		t.Fatal("role swap accepted")
	}
	tampered = manifest
	legacyRecord := *manifest.Legacy
	tampered.Active, legacyRecord = legacyRecord, tampered.Active
	tampered.Legacy = &legacyRecord
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, tampered); err == nil {
		t.Fatal("valid wrapped records swapped between roles")
	}
	tampered = manifest
	tampered.Legacy = nil
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, tampered); err == nil {
		t.Fatal("legacy read key omitted")
	}
}

func TestAssistantWrappedManifestReadsHistoricalTranscriptWithoutRawKey(t *testing.T) {
	ctx := context.Background()
	wrapper := assistantWrapFixture(t)
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	legacy, err := assistantTranscriptKeyProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	relay := newMemoryRelay()
	signer := keyer.NewPlainKeySigner([32]byte(wrapper.secret))
	original := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Publisher: relay, Subscriber: relay, Signer: signer, Identity: service.AssistantIdentity{Pubkey: wrapper.pubkey.Hex()}, KeyProvider: legacy, ServicePubkey: wrapper.pubkey.Hex()})
	_, err = original.AppendMessage(ctx, service.AssistantTranscriptAppend{LogicalID: "historical-1", SessionID: "s-historical", TurnID: "turn-1", Sequence: 1, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser, Content: []domain.AssistantAgentContentBlock{{Type: domain.AssistantAgentContentText, Text: "before cutover"}}}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := createAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := openAssistantWrappedKeyManifest(ctx, wrapper, wrapper.pubkey, manifest)
	if err != nil {
		t.Fatal(err)
	}
	recovered := service.NewAssistantTranscriptStore(service.AssistantTranscriptStoreConfig{Subscriber: relay, Identity: service.AssistantIdentity{Pubkey: wrapper.pubkey.Hex()}, KeyProvider: wrapped, ServicePubkey: wrapper.pubkey.Hex()})
	records, err := recovered.Replay(ctx, service.AssistantTranscriptReplayQuery{SessionID: "s-historical"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Payload.Message.Content) != 1 || records[0].Payload.Message.Content[0].Text != "before cutover" {
		t.Fatalf("historical replay = %+v", records)
	}
	if _, err := recovered.AppendMessage(ctx, service.AssistantTranscriptAppend{SessionID: "s-historical", Sequence: 2, Message: domain.AssistantAgentMessage{Role: domain.AssistantAgentMessageRoleUser}}); err == nil {
		t.Fatal("read-only transition provider permitted a new event")
	}
}
