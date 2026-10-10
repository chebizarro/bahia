package controlplane

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"go.uber.org/zap"
)

// countingRemoteKeyer stands in for a NIP-46/NIP-55L service signer: no
// in-process key material, every operation counted.
type countingRemoteKeyer struct {
	keyer.KeySigner
	signs, encrypts, decrypts atomic.Int64
}

func (k *countingRemoteKeyer) SignEvent(ctx context.Context, ev *nostr.Event) error {
	k.signs.Add(1)
	return k.KeySigner.SignEvent(ctx, ev)
}
func (k *countingRemoteKeyer) Encrypt(ctx context.Context, plaintext string, to nostr.PubKey) (string, error) {
	k.encrypts.Add(1)
	return k.KeySigner.Encrypt(ctx, plaintext, to)
}
func (k *countingRemoteKeyer) Decrypt(ctx context.Context, ciphertext string, from nostr.PubKey) (string, error) {
	k.decrypts.Add(1)
	return k.KeySigner.Decrypt(ctx, ciphertext, from)
}

type responderCapturePublisher struct{ events []nostr.Event }

func (p *responderCapturePublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	p.events = append(p.events, ev)
	return 1, nil
}

func TestEncryptedResponderUsesInjectedKeyerForEveryOperation(t *testing.T) {
	ctx := context.Background()
	service := &countingRemoteKeyer{KeySigner: keyer.NewPlainKeySigner(nostr.Generate())}
	servicePubkey, _ := service.GetPublicKey(ctx)
	requester := keyer.NewPlainKeySigner(nostr.Generate())
	publisher := &responderCapturePublisher{}
	responder := NewEncryptedResponder(publisher, service, zap.NewNop())
	if responder.ServicePubkey() != servicePubkey.Hex() {
		t.Fatalf("ServicePubkey() = %s, want %s", responder.ServicePubkey(), servicePubkey.Hex())
	}

	content, err := requester.Encrypt(ctx, `{"method":"x"}`, servicePubkey)
	if err != nil {
		t.Fatal(err)
	}
	request := &nostr.Event{Kind: 1, CreatedAt: 1, Content: content}
	if err := requester.SignEvent(ctx, request); err != nil {
		t.Fatal(err)
	}
	plaintext, err := responder.DecryptRequestContent(ctx, request)
	if err != nil || string(plaintext) != `{"method":"x"}` {
		t.Fatalf("DecryptRequestContent = %q, %v", plaintext, err)
	}
	if err := responder.PublishEncryptedResult(ctx, request, "ok", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if service.decrypts.Load() != 1 || service.encrypts.Load() != 1 || service.signs.Load() != 1 {
		t.Fatalf("keyer decrypts=%d encrypts=%d signs=%d, want 1 each", service.decrypts.Load(), service.encrypts.Load(), service.signs.Load())
	}
	result := publisher.events[0]
	if result.PubKey != servicePubkey || !result.VerifySignature() {
		t.Fatal("result not signed by the injected keyer")
	}
	opened, err := requester.Decrypt(ctx, result.Content, servicePubkey)
	if err != nil {
		t.Fatal(err)
	}
	var envelope EncryptedResultEnvelope
	if err := json.Unmarshal([]byte(opened), &envelope); err != nil || envelope.RequestEventID != request.ID.Hex() {
		t.Fatalf("result envelope = %+v, %v", envelope, err)
	}
}
