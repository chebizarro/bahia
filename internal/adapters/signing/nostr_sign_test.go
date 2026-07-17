package signing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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

	ev := &gonostr.Event{
		Kind:    gonostr.Kind(NostrSignatureKind),
		Content: string(content),
		Tags:    gonostr.Tags{{"d", artifact.ImageDigest}},
	}
	if err := nostrutil.SignEventWithHexKey(ev, privkey); err != nil {
		t.Fatalf("signing event: %v", err)
	}
	return ev
}

func generatedNostrKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privkey := nostrutil.GeneratePrivateKeyHex()
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(privkey)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	return privkey, pubkey
}

func TestNostrVerifier_ValidTrustedEvent(t *testing.T) {
	privkey, pubkey := generatedNostrKeyPair(t)

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
	if sig.VerificationStatus != domain.SignatureStatusVerified {
		t.Errorf("verification_status = %q, want verified", sig.VerificationStatus)
	}
	if sig.VerifiedAt == nil {
		t.Error("expected verified_at for verified attestation")
	}
	if sig.SignatureType != domain.SignatureNostr {
		t.Errorf("type = %q, want nostr", sig.SignatureType)
	}
	if sig.SignerIdentity != pubkey {
		t.Errorf("signer = %q, want %q", sig.SignerIdentity, pubkey)
	}
}

func TestNostrVerifier_UntrustedPubkey(t *testing.T) {
	privkey, _ := generatedNostrKeyPair(t)
	_, otherPubkey := generatedNostrKeyPair(t)

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
	if sig.VerificationStatus != domain.SignatureStatusRejected {
		t.Errorf("verification_status = %q, want rejected", sig.VerificationStatus)
	}
	if sig.VerifiedAt != nil {
		t.Error("expected verified_at to be nil for rejected attestation")
	}
}

func TestNostrVerifier_DigestMismatch(t *testing.T) {
	privkey, pubkey := generatedNostrKeyPair(t)

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
	if sig.VerificationStatus != domain.SignatureStatusRejected {
		t.Errorf("verification_status = %q, want rejected", sig.VerificationStatus)
	}
	if sig.VerifiedAt != nil {
		t.Error("expected verified_at to be nil for rejected attestation")
	}
}

func TestNostrVerifier_DisapprovedAttestation(t *testing.T) {
	privkey, pubkey := generatedNostrKeyPair(t)

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
	if sig.VerificationStatus != domain.SignatureStatusRejected {
		t.Errorf("verification_status = %q, want rejected", sig.VerificationStatus)
	}
	if sig.VerifiedAt != nil {
		t.Error("expected verified_at to be nil for rejected attestation")
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

func TestNostrVerifier_VerifySignaturesFailsClosed(t *testing.T) {
	verifier := NewNostrVerifier(nil, zap.NewNop())
	sigs, err := verifier.VerifySignatures(context.Background(), &domain.Artifact{})
	if sigs != nil {
		t.Fatalf("signatures = %#v, want nil on unavailable verification", sigs)
	}
	if !errors.Is(err, ErrNostrPullVerificationUnavailable) {
		t.Fatalf("error = %v, want ErrNostrPullVerificationUnavailable", err)
	}
}
