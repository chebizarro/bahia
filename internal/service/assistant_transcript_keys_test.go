package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// transcriptKeyStore delivers assistant-transcript keys by credential id,
// enforcing the system scope by label as Signet does.
type transcriptKeyStore map[string]struct {
	label string
	key   []byte
}

func (s transcriptKeyStore) Resolve(_ context.Context, ref domain.SecretCredentialRef, scope secretstore.Scope) (*secretstore.Value, error) {
	c, ok := s[ref.CredentialID]
	if !ok {
		return nil, secretstore.ErrNotFound
	}
	if !scope.Admits(c.label) {
		return nil, secretstore.ErrDenied
	}
	return secretstore.NewValue(append([]byte(nil), c.key...), time.Now().Add(time.Minute)), nil
}

func (transcriptKeyStore) Inspect(context.Context, string, secretstore.Scope) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (transcriptKeyStore) Health(context.Context) error { return nil }

var testTranscriptSignet = transcriptKeyStore{
	"cred_" + strings.Repeat("1", 64): {"bahia-system/assistant-transcript/k1", bytes.Repeat([]byte{1}, secretstore.SystemKeySize)},
	"cred_" + strings.Repeat("2", 64): {"bahia-system/assistant-transcript/k2", bytes.Repeat([]byte{2}, secretstore.SystemKeySize)},
}

// keyringKeys configures the assistant-transcript keys in order (the first
// is active) and loads them.
func keyringKeys(t *testing.T, keyIDs ...string) KeyringTranscriptKeys {
	t.Helper()
	var refs []secretstore.SystemKeyRef
	for _, id := range keyIDs {
		refs = append(refs, secretstore.SystemKeyRef{Purpose: secretstore.PurposeAssistantTranscript, KeyID: id, Credential: domain.SecretCredentialRef{
			Store: secretstore.BackendSignet, CredentialID: "cred_" + strings.Repeat(id[1:], 64), Version: 1,
		}})
	}
	ring, err := secretstore.NewKeyring(refs)
	if err != nil {
		t.Fatal(err)
	}
	if err := ring.Load(context.Background(), testTranscriptSignet); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ring.Zeroize)
	return KeyringTranscriptKeys{Ring: ring}
}

// A checkpoint chain stays readable across a key rotation as long as the old
// key is still configured (read-only), and fails closed once it is not.
func TestKeyringTranscriptKeysRotateWithVersionedKeyRefs(t *testing.T) {
	ctx := context.Background()
	signer := testAssistantSigner(t)
	pub := &assistantTestPublisher{}
	before := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: signer, KeyProvider: keyringKeys(t, "k1")})
	previous := ""
	for rev := uint64(1); rev <= 2; rev++ {
		var err error
		if previous, err = before.Append(ctx, checkpointTestExecution(rev), previous); err != nil {
			t.Fatal(err)
		}
	}
	rotated := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Publisher: pub, Signer: signer, KeyProvider: keyringKeys(t, "k2", "k1")})
	if _, err := rotated.Append(ctx, checkpointTestExecution(3), previous); err != nil {
		t.Fatal(err)
	}
	events := pub.eventsOfKind(domain.AssistantExecutionCheckpointKind)
	refs := []string{}
	for _, ev := range events {
		refs = append(refs, tagValue(ev.Tags, domain.AssistantTranscriptTagKeyRef)+"@"+tagValue(ev.Tags, domain.AssistantTranscriptTagKeyVersion))
	}
	want := "bahia-system/assistant-transcript/k1@1 bahia-system/assistant-transcript/k1@1 bahia-system/assistant-transcript/k2@1"
	if strings.Join(refs, " ") != want {
		t.Fatalf("key refs = %v, want %s", refs, want)
	}
	rotated.subscriber = newReplayTranscriptSubscriber(events)
	head, err := rotated.Load(ctx, "session-chain", "run-chain")
	if err != nil || head.Execution.Revision != 3 || len(head.Chain) != 3 {
		t.Fatalf("rotated load = %+v, %v", head, err)
	}
	retired := NewAssistantExecutionStore(AssistantExecutionStoreConfig{Signer: signer, KeyProvider: keyringKeys(t, "k2"), Subscriber: newReplayTranscriptSubscriber(events)})
	if _, err := retired.Load(ctx, "session-chain", "run-chain"); err == nil || !strings.Contains(err.Error(), "bahia-system/assistant-transcript/k1") {
		t.Fatalf("load without the old key = %v, want the missing key named", err)
	}
}

func TestKeyringTranscriptKeysRefuseLegacyAndMismatchedKeys(t *testing.T) {
	ctx := context.Background()
	keys := keyringKeys(t, "k1")
	active, err := keys.ActiveTranscriptKey(ctx)
	if err != nil || active.Ref != "bahia-system/assistant-transcript/k1" || active.Version != "1" || !bytes.Equal(active.Key, bytes.Repeat([]byte{1}, secretstore.SystemKeySize)) {
		t.Fatalf("active = %+v, %v", active, err)
	}
	for name, ref := range map[string][2]string{
		"nsec-derived v1":  {"assistant-transcript/service-nostr-key", "v1"},
		"other version":    {"bahia-system/assistant-transcript/k1", "2"},
		"unconfigured key": {"bahia-system/assistant-transcript/k9", "1"},
		"other purpose":    {"bahia-system/confidential-dedupe/k1", "1"},
		"empty key id":     {"bahia-system/assistant-transcript/", "1"},
		"wrapped manifest": {"assistant-transcript/wrapped", "v2"},
	} {
		if _, err := keys.TranscriptKey(ctx, ref[0], ref[1]); err == nil {
			t.Errorf("%s: TranscriptKey(%q, %q) succeeded", name, ref[0], ref[1])
		}
	}
	if _, err := (KeyringTranscriptKeys{}).ActiveTranscriptKey(ctx); err == nil {
		t.Fatal("a provider without a keyring served a key")
	}
}
