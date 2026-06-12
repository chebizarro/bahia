package controlplane

import (
	"context"
	"errors"
	"testing"

	canonicalnostr "fiatjaf.com/nostr"
	gonostr "fiatjaf.com/nostr"
)

type signerContextKey struct{}

type contextCheckingSigner struct {
	delegate canonicalnostr.Signer
	want     string
}

func (s *contextCheckingSigner) GetPublicKey(ctx context.Context) (canonicalnostr.PubKey, error) {
	return s.delegate.GetPublicKey(ctx)
}

func (s *contextCheckingSigner) SignEvent(ctx context.Context, ev *canonicalnostr.Event) error {
	if got, _ := ctx.Value(signerContextKey{}).(string); got != s.want {
		return errors.New("signer did not receive call context")
	}
	return s.delegate.SignEvent(ctx, ev)
}

func TestSignGoNostrEventUsesCanonicalSigner(t *testing.T) {
	privateKey := gonostr.Generate().Hex()
	wantPubkey := testNostrPubKeyHexFromPrivateKey(t, privateKey)
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	event := &gonostr.Event{
		Kind:      KindDeploymentResult,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"status", "success"}, {"p", wantPubkey}},
		Content:   "ok",
	}

	if err := SignGoNostrEvent(context.Background(), signer, event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	if event.ID == (gonostr.ID{}) || event.Sig == ([64]byte{}) {
		t.Fatalf("expected signed event fields, got id=%x sig=%x", event.ID, event.Sig)
	}
	if event.PubKey.Hex() != wantPubkey {
		t.Fatalf("pubkey = %s, want %s", event.PubKey.Hex(), wantPubkey)
	}
	if !event.VerifySignature() {
		t.Fatalf("signature invalid")
	}
}

func TestReactorSignEventPropagatesCallContext(t *testing.T) {
	baseSigner, err := NewPrivateKeySigner(gonostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	signer := &contextCheckingSigner{delegate: baseSigner, want: "request-ctx"}
	reactor := NewReactor(Config{}, nil, nil, signer, nil)
	ctx := context.WithValue(context.Background(), signerContextKey{}, "request-ctx")
	event := &gonostr.Event{Kind: KindDeploymentResult, CreatedAt: gonostr.Now(), Content: "ok"}

	if err := reactor.signEvent(ctx, event); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	if !event.VerifySignature() {
		t.Fatalf("signature invalid")
	}
}

func TestSignGoNostrEventRequiresSigner(t *testing.T) {
	err := SignGoNostrEvent(context.Background(), nil, &gonostr.Event{Kind: KindDeploymentResult})
	if err == nil {
		t.Fatal("expected missing signer error")
	}
}
