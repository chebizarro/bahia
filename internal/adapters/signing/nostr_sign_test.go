package signing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	gonostr "github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func makeTestEvent(t *testing.T, privkey string, artifact *domain.Artifact, approved bool) *gonostr.Event {
	t.Helper()

	attestation := NostrArtifactAttestation{
		ImageRepo:   artifact.ImageRepo,
		ImageDigest: artifact.ImageDigest,
		Approved:    approved,
	}
	content, err := json.Marshal(attestation)
	if err != nil {
		t.Fatalf("marshaling attestation: %v", err)
	}

	pubkey, err := gonostr.GetPublicKey(privkey)
	if err != nil {
		t.Fatalf("getting public key: %v", err)
	}

	ev := &gonostr.Event{
		Kind:    NostrSignatureKind,
		PubKey:  pubkey,
		Content: string(content),
		Tags:    gonostr.Tags{{"d", artifact.ImageDigest}},
	}
	err = ev.Sign(privkey)
	if err != nil {
		t.Fatalf("signing event: %v", err)
	}
	return ev
}

func TestNostrVerifier_ValidTrustedEvent(t *testing.T) {
	privkey := gonostr.GeneratePrivateKey()
	pubkey, _ := gonostr.GetPublicKey(privkey)

	artifact := &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	}

	ev := makeTestEvent(t, privkey, artifact, true)
	verifier := NewNostrVerifier([]string{pubkey}, zap.NewNop())

	sig, err := verifier.VerifyEvent(context.Background(), ev, artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sig.Verified {
		t.Errorf("expected verified, got error: %s", sig.VerificationError)
	}
	if sig.SignatureType != domain.SignatureNostr {
		t.Errorf("type = %q, want nostr", sig.SignatureType)
	}
	if sig.SignerIdentity != pubkey {
		t.Errorf("signer = %q, want %q", sig.SignerIdentity, pubkey)
	}
}

func TestNostrVerifier_UntrustedPubkey(t *testing.T) {
	privkey := gonostr.GeneratePrivateKey()
	otherPubkey, _ := gonostr.GetPublicKey(gonostr.GeneratePrivateKey())

	artifact := &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	}

	ev := makeTestEvent(t, privkey, artifact, true)
	// Trust a different pubkey.
	verifier := NewNostrVerifier([]string{otherPubkey}, zap.NewNop())

	sig, err := verifier.VerifyEvent(context.Background(), ev, artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Verified {
		t.Error("expected not verified for untrusted pubkey")
	}
	if sig.VerificationError == "" {
		t.Error("expected verification error message")
	}
}

func TestNostrVerifier_DigestMismatch(t *testing.T) {
	privkey := gonostr.GeneratePrivateKey()
	pubkey, _ := gonostr.GetPublicKey(privkey)

	artifact := &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	}

	// Create event with wrong digest.
	wrongArtifact := &domain.Artifact{
		ID:          artifact.ID,
		ImageRepo:   artifact.ImageRepo,
		ImageDigest: "sha256:wrong",
	}
	ev := makeTestEvent(t, privkey, wrongArtifact, true)

	verifier := NewNostrVerifier([]string{pubkey}, zap.NewNop())

	sig, err := verifier.VerifyEvent(context.Background(), ev, artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Verified {
		t.Error("expected not verified for digest mismatch")
	}
}

func TestNostrVerifier_DisapprovedAttestation(t *testing.T) {
	privkey := gonostr.GeneratePrivateKey()
	pubkey, _ := gonostr.GetPublicKey(privkey)

	artifact := &domain.Artifact{
		ID:          uuid.New(),
		ImageRepo:   "myorg/myapp",
		ImageDigest: "sha256:abc123",
	}

	ev := makeTestEvent(t, privkey, artifact, false) // disapproved
	verifier := NewNostrVerifier([]string{pubkey}, zap.NewNop())

	sig, err := verifier.VerifyEvent(context.Background(), ev, artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Verified {
		t.Error("expected not verified for disapproved attestation")
	}
}

func TestNostrVerifier_WrongKind(t *testing.T) {
	ev := &gonostr.Event{Kind: 1}
	verifier := NewNostrVerifier(nil, zap.NewNop())

	_, err := verifier.VerifyEvent(context.Background(), ev, &domain.Artifact{})
	if err == nil {
		t.Fatal("expected error for wrong kind")
	}
}

func TestNostrVerifier_VerifySignatures_ReturnsNil(t *testing.T) {
	verifier := NewNostrVerifier(nil, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sigs != nil {
		t.Error("expected nil sigs from VerifySignatures")
	}
}
